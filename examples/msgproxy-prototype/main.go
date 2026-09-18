// msgproxy-prototype is where claude-code-wire-proxy started, and is kept in
// step with it. It serves the Anthropic Messages API in front of the real
// claude binary, so a harness that speaks that API runs on your own claude
// login. The client declares and runs the tools; claude never touches files.
//
// POST /v1/messages, streaming or not. The request's tools are handed to claude
// as custom tools. When the model calls one, the tool handler parks and the
// endpoint replies stop_reason "tool_use". The next request carries the
// tool_result, which unparks it, and claude carries on from there.
//
// A request is split into the history before it and its input: the run of
// user messages at the end. When that history continues a live conversation
// and the input is what the conversation is waiting for, it carries on in the
// same claude process. Otherwise, as after a client prunes or compacts its
// history, the history is written as a Claude Code session file and a new
// claude resumes it. A rebuild supersedes older processes started from the same
// system prompt and first message, and idle conversations are closed.
//
// The model, and the effort (output_config.effort, passed as --effort), are
// fixed per claude process: a request that changes either rebuilds the
// conversation. Thinking is logged, never streamed:
// claude withholds its text for Opus 5 and Sonnet 5.
//
// Dropped on the floor: max_tokens, temperature, tool_choice, stop_sequences,
// cache_control. Refused: URL images, and any block other than text,
// image, tool_use and tool_result. Images lose their order relative to the
// prompt text. Each reply carries the usage of the model call that produced
// it, which is what a client reads as the size of its context. A tool claude
// does not accept fails the request. An empty system prompt gets Claude Code's
// default one.
//
// Loopback only: this runs on your own claude login.
//
//	go run ./examples/msgproxy-prototype
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8787", "loopback address to serve on")
	idle := flag.Duration("idle", 30*time.Minute, "close a conversation left idle this long")
	flag.Parse()

	host, _, err := net.SplitHostPort(*addr)
	if err != nil {
		log.Fatal(err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		log.Fatalf("refusing %s: this serves your own claude login, so loopback only", *addr)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// A stable, empty working directory: no CLAUDE.md or project settings reach
	// the prompt, claude's environment note is the same every run, and the
	// session files written for --resume land in one place.
	cache, err := os.UserCacheDir()
	if err != nil {
		log.Fatal(err)
	}
	cwd := filepath.Join(cache, "msgproxy-prototype", "cwd")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		log.Fatal(err)
	}
	// claude files sessions under the resolved path.
	if cwd, err = filepath.EvalSymlinks(cwd); err != nil {
		log.Fatal(err)
	}
	projects := projectsDir(cwd)
	if err := os.MkdirAll(projects, 0o700); err != nil {
		log.Fatal(err)
	}
	// Session files left by a run that did not shut down cleanly.
	leftover, _ := filepath.Glob(filepath.Join(projects, "*.jsonl"))
	for _, path := range leftover {
		os.Remove(path)
	}

	s := &server{ctx: ctx, cwd: cwd, projects: projects, convs: map[string]*conv{}, all: map[*conv]struct{}{}}
	go s.expire(*idle)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/messages", s.messages)
	mux.HandleFunc("/", unknown)
	srv := &http.Server{Addr: *addr, Handler: mux}
	go func() {
		<-ctx.Done()
		srv.Close()
	}()

	log.Printf("msgproxy-prototype on http://%s/v1/messages — ctrl-c to stop", *addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	s.closeAll()
}

// ---- wire ------------------------------------------------------------------

type request struct {
	Model        string          `json:"model"`
	System       json.RawMessage `json:"system"`
	Messages     []wireMessage   `json:"messages"`
	Tools        []wireTool      `json:"tools"`
	Stream       bool            `json:"stream"`
	Thinking     *wireThinking   `json:"thinking"`
	OutputConfig *wireOutput     `json:"output_config"`
}

type wireThinking struct {
	Type string `json:"type"`
}

type wireOutput struct {
	Effort string `json:"effort"`
}

// effortOf reads the effort a request asks for, as claude --effort takes it.
// Empty leaves Claude Code's default. Claude Code has no switch that turns
// thinking off, so a request that disables it gets the lowest effort.
func effortOf(req request) (string, error) {
	if req.Thinking != nil && req.Thinking.Type == "disabled" {
		return "low", nil
	}
	if req.OutputConfig == nil {
		return "", nil
	}
	switch effort := req.OutputConfig.Effort; effort {
	case "", "low", "medium", "high", "xhigh", "max":
		return effort, nil
	case "minimal":
		return "low", nil
	default:
		return "", fmt.Errorf("unknown effort %q", effort)
	}
}

func orDefault(effort string) string {
	if effort == "" {
		return "default"
	}
	return effort
}

type wireTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type wireMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type wireBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     any             `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
	Source    *imageSource    `json:"source"`
}

type imageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

type response struct {
	ID           string   `json:"id"`
	Type         string   `json:"type"`
	Role         string   `json:"role"`
	Model        string   `json:"model"`
	Content      []block  `json:"content"`
	StopReason   string   `json:"stop_reason"`
	StopSequence *string  `json:"stop_sequence"`
	Usage        pi.Usage `json:"usage"`
}

// message and block are the normalised history, and a reply is built from the
// same shape, so a client replaying the reply hashes to the same key.
type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

type block struct {
	Type      string `json:"type"`
	Text      string `json:"text,omitempty"`
	ID        string `json:"id,omitempty"`
	Name      string `json:"name,omitempty"`
	Input     any    `json:"input,omitempty"`
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`

	// Source is an image block's picture; Images are a tool_result's.
	Source *imageSource  `json:"source,omitempty"`
	Images []imageSource `json:"images,omitempty"`
}

func normalise(in []wireMessage) ([]message, error) {
	out := make([]message, 0, len(in))
	for i, m := range in {
		blocks, err := blocksOf(m.Content)
		if err != nil {
			return nil, fmt.Errorf("messages[%d]: %w", i, err)
		}
		out = append(out, message{Role: m.Role, Content: blocks})
	}
	return out, nil
}

// blocksOf takes content as a plain string or a block list, and refuses what it
// cannot pass through rather than dropping it.
func blocksOf(raw json.RawMessage) ([]block, error) {
	if len(raw) == 0 {
		return []block{}, nil
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return []block{{Type: "text", Text: text}}, nil
	}
	var wire []wireBlock
	if err := json.Unmarshal(raw, &wire); err != nil {
		return nil, err
	}

	out := make([]block, 0, len(wire))
	for _, w := range wire {
		switch w.Type {
		case "text":
			out = append(out, block{Type: "text", Text: w.Text})
		case "tool_use":
			out = append(out, block{Type: "tool_use", ID: w.ID, Name: w.Name, Input: w.Input})
		case "image":
			if w.Source == nil || w.Source.Type != "base64" {
				return nil, errors.New("only base64 images are supported by this proxy")
			}
			out = append(out, block{Type: "image", Source: w.Source})
		case "tool_result":
			inner, err := blocksOf(w.Content)
			if err != nil {
				return nil, fmt.Errorf("tool_result %s: %w", w.ToolUseID, err)
			}
			var texts []string
			var images []imageSource
			for _, b := range inner {
				switch b.Type {
				case "text":
					texts = append(texts, b.Text)
				case "image":
					images = append(images, *b.Source)
				default:
					return nil, fmt.Errorf("tool_result %s: a %s block cannot go in a tool result", w.ToolUseID, b.Type)
				}
			}
			out = append(out, block{
				Type: "tool_result", ToolUseID: w.ToolUseID, Content: strings.Join(texts, "\n\n"),
				IsError: w.IsError, Images: images,
			})
		default:
			return nil, fmt.Errorf("%s blocks are not supported by this proxy", w.Type)
		}
	}
	return out, nil
}

func joinText(blocks []block) (string, error) {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			return "", fmt.Errorf("expected text, got a %s block", b.Type)
		}
		parts = append(parts, b.Text)
	}
	return strings.Join(parts, "\n\n"), nil
}

// splitInput separates a history into what came before and the input: the run
// of user messages at the end. A client can send several, such as tool results
// followed by a reminder it adds only for this request.
func splitInput(history []message) (prior, input []message) {
	i := len(history)
	for i > 0 && history[i-1].Role == "user" {
		i--
	}
	return history[:i], history[i:]
}

// promptOf turns an input into a prompt: its text and its images. The order
// between them is lost: images go first, as the API recommends.
func promptOf(input []message) (string, []pi.Image, error) {
	var texts []string
	var images []pi.Image
	for _, m := range input {
		for _, b := range m.Content {
			switch b.Type {
			case "text":
				texts = append(texts, b.Text)
			case "image":
				image, err := decodeImage(*b.Source)
				if err != nil {
					return "", nil, err
				}
				images = append(images, image)
			default:
				return "", nil, fmt.Errorf("expected a text or image prompt, got a %s block", b.Type)
			}
		}
	}
	return strings.Join(texts, "\n\n"), images, nil
}

func hasToolResults(input []message) bool {
	for _, m := range input {
		if slices.ContainsFunc(m.Content, func(b block) bool { return b.Type == "tool_result" }) {
			return true
		}
	}
	return false
}

func decodeImage(src imageSource) (pi.Image, error) {
	data, err := base64.StdEncoding.DecodeString(src.Data)
	if err != nil {
		return pi.Image{}, fmt.Errorf("image data is not base64: %w", err)
	}
	return pi.Image{MediaType: src.MediaType, Data: data}, nil
}

// answers pulls the tool results out of an input, and reports whether they
// answer exactly the tool_use blocks the model is parked on. Anything sent
// alongside them, such as a reminder the client adds per request, rides on the
// last result: a model parked on a tool call can be reached no other way.
func answers(input []message, asked []string) (map[string]pi.ToolResult, bool) {
	results := map[string]pi.ToolResult{}
	var last string
	var texts []string
	var images []pi.Image
	for _, m := range input {
		for _, b := range m.Content {
			switch b.Type {
			case "tool_result":
				result := pi.ToolResult{Text: b.Content, IsError: b.IsError}
				for _, src := range b.Images {
					image, err := decodeImage(src)
					if err != nil {
						return nil, false
					}
					result.Images = append(result.Images, image)
				}
				results[b.ToolUseID] = result
				last = b.ToolUseID
			case "text":
				if strings.TrimSpace(b.Text) != "" {
					texts = append(texts, b.Text)
				}
			case "image":
				image, err := decodeImage(*b.Source)
				if err != nil {
					return nil, false
				}
				images = append(images, image)
			default:
				return nil, false
			}
		}
	}
	if len(results) != len(asked) {
		return nil, false
	}
	for _, id := range asked {
		if _, ok := results[id]; !ok {
			return nil, false
		}
	}

	if len(texts) > 0 || len(images) > 0 {
		result := results[last]
		parts := texts
		if result.Text != "" {
			parts = append([]string{result.Text}, texts...)
		}
		result.Text = strings.Join(parts, "\n\n")
		result.Images = append(result.Images, images...)
		results[last] = result
	}
	return results, true
}

// key identifies a conversation by its history, hashed the way clients replay
// it: pi drops whitespace-only text, and a streamed reply can come back with
// adjacent text blocks joined into one.
func key(history []message) string {
	data, err := json.Marshal(canonicalise(history))
	if err != nil {
		panic(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func canonicalise(history []message) []message {
	canon := make([]message, 0, len(history))
	for _, m := range history {
		var blocks []block
		for _, b := range m.Content {
			if b.Type == "text" && strings.TrimSpace(b.Text) == "" {
				continue
			}
			if n := len(blocks); n > 0 && b.Type == "text" && blocks[n-1].Type == "text" {
				blocks[n-1].Text += b.Text
				continue
			}
			blocks = append(blocks, b)
		}
		canon = append(canon, message{Role: m.Role, Content: blocks})
	}
	return canon
}

// canonical re-encodes JSON so two spellings of the same value compare equal.
func canonical(v any) string {
	if raw, ok := v.(json.RawMessage); ok {
		if len(raw) == 0 {
			return "{}"
		}
		v = nil
		if err := json.Unmarshal(raw, &v); err != nil {
			panic(err)
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(data)
}

// unknown answers any other endpoint, such as /v1/messages/count_tokens, in
// the API's error shape, and logs it so a client relying on it shows up.
func unknown(w http.ResponseWriter, r *http.Request) {
	fail(w, http.StatusNotFound, "not_found_error", fmt.Sprintf("%s %s is not served by this proxy", r.Method, r.URL.Path))
}

func fail(w http.ResponseWriter, status int, kind, msg string) {
	log.Printf("%d %s: %s", status, kind, msg)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"type":  "error",
		"error": map[string]string{"type": kind, "message": msg},
	})
}

// ---- streaming ---------------------------------------------------------------

// sse writes one reply as Anthropic stream events while claude produces it.
// Text goes out as claude's deltas arrive; each finished text block from claude
// confirms what was streamed, and closes the SSE block once all of it is.
type sse struct {
	w           http.ResponseWriter
	flusher     http.Flusher
	index       int
	open        bool
	unconfirmed int // bytes streamed as deltas that no finished block has covered yet
}

func startSSE(w http.ResponseWriter, id, model string) *sse {
	flusher, ok := w.(http.Flusher)
	if !ok {
		panic("response writer cannot flush")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	s := &sse{w: w, flusher: flusher}
	s.send("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id": id, "type": "message", "role": "assistant", "model": model,
			"content": []any{}, "stop_reason": nil, "stop_sequence": nil,
			"usage": map[string]int{"input_tokens": 0, "output_tokens": 0},
		},
	})
	return s
}

func (s *sse) send(event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(s.w, "event: %s\ndata: %s\n\n", event, payload)
	s.flusher.Flush()
}

func (s *sse) write(text string) {
	if !s.open {
		s.send("content_block_start", map[string]any{
			"type": "content_block_start", "index": s.index,
			"content_block": map[string]any{"type": "text", "text": ""},
		})
		s.open = true
	}
	s.send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index,
		"delta": map[string]any{"type": "text_delta", "text": text},
	})
}

func (s *sse) delta(text string) {
	s.write(text)
	s.unconfirmed += len(text)
}

func (s *sse) text(full string) {
	if len(full) > s.unconfirmed {
		s.write(full[s.unconfirmed:])
	}
	s.unconfirmed = max(0, s.unconfirmed-len(full))
	if s.unconfirmed == 0 {
		s.closeText()
	}
}

func (s *sse) closeText() {
	if !s.open {
		return
	}
	s.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index})
	s.open = false
	s.unconfirmed = 0
	s.index++
}

// toolUse sends the input as one input_json_delta: pi rebuilds the arguments
// from the deltas, not from content_block_start.
func (s *sse) toolUse(b block) {
	s.closeText()
	input, err := json.Marshal(b.Input)
	if err != nil {
		panic(err)
	}
	s.send("content_block_start", map[string]any{
		"type": "content_block_start", "index": s.index,
		"content_block": map[string]any{"type": "tool_use", "id": b.ID, "name": b.Name, "input": map[string]any{}},
	})
	s.send("content_block_delta", map[string]any{
		"type": "content_block_delta", "index": s.index,
		"delta": map[string]any{"type": "input_json_delta", "partial_json": string(input)},
	})
	s.send("content_block_stop", map[string]any{"type": "content_block_stop", "index": s.index})
	s.index++
}

func (s *sse) finish(st step) {
	s.closeText()
	s.send("message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": st.stop, "stop_sequence": nil},
		"usage": st.usage,
	})
	s.send("message_stop", map[string]any{"type": "message_stop"})
}

func (s *sse) fail(err error) {
	log.Printf("stream error: %v", err)
	s.send("error", map[string]any{
		"type":  "error",
		"error": map[string]string{"type": "api_error", "message": err.Error()},
	})
}

// ---- server ----------------------------------------------------------------

// restoredPrompt starts the turn when a rebuilt conversation ends on tool
// results. claude needs a user message to begin a turn, and the results have to
// be in the session file: a tool call left open at resume is dropped.
const restoredPrompt = "Continue."

type server struct {
	ctx      context.Context
	cwd      string
	projects string // where claude looks for sessions resumed in cwd
	nextID   atomic.Int64
	replies  atomic.Int64

	mu    sync.Mutex
	convs map[string]*conv   // keyed by the history each has answered so far
	all   map[*conv]struct{} // every live conversation
}

func (s *server) messages(w http.ResponseWriter, r *http.Request) {
	var req request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	history, err := normalise(req.Messages)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	if len(history) == 0 || history[len(history)-1].Role != "user" {
		fail(w, http.StatusBadRequest, "invalid_request_error", "the last message must be from the user")
		return
	}
	effort, err := effortOf(req)
	if err != nil {
		fail(w, http.StatusBadRequest, "invalid_request_error", err.Error())
		return
	}
	prior, input := splitInput(history)

	s.mu.Lock()
	c := s.convs[key(prior)]
	if c != nil {
		c.busy = true
	}
	s.mu.Unlock()

	// A live conversation carries on when the input is what it is waiting for,
	// at the effort it was started with.
	var advance func()
	switch {
	case c != nil && c.model != req.Model:
		log.Printf("conv %d: model %s, asked for %s", c.id, c.model, req.Model)
	case c != nil && c.effort != effort:
		log.Printf("conv %d: effort %s, asked for %s", c.id, orDefault(c.effort), orDefault(effort))
	case c != nil && c.parked:
		if results, ok := answers(input, c.asked); ok {
			advance = func() { c.results <- results }
		}
	case c != nil:
		if text, images, err := promptOf(input); err == nil {
			advance = func() { go c.run(s.ctx, text, images) }
		}
	}

	// Anything else starts a claude process from the history as sent.
	if advance == nil {
		if c != nil {
			log.Printf("conv %d: cannot carry on; rebuilding", c.id)
			s.close(c)
		}
		var status int
		if c, advance, status, err = s.start(req, effort, prior, input); err != nil {
			fail(w, status, "invalid_request_error", err.Error())
			return
		}
	}

	id := fmt.Sprintf("msg_proxy_%d", s.replies.Add(1))
	var stream *sse
	if req.Stream {
		stream = startSSE(w, id, req.Model)
		c.setSink(stream)
	}
	advance()

	// Not tied to the request: if the client hangs up, the step is still taken
	// so the conversation stays consistent, and the client's retry is rebuilt.
	st := <-c.steps
	c.setSink(nil)

	if dropped := c.droppedTools(); len(dropped) > 0 {
		s.close(c)
		msg := fmt.Sprintf("claude did not accept tool(s) %s; check their input_schema", strings.Join(dropped, ", "))
		if stream != nil {
			stream.fail(errors.New(msg))
		} else {
			fail(w, http.StatusBadRequest, "invalid_request_error", msg)
		}
		return
	}

	if st.err != nil {
		s.close(c)
		if stream != nil {
			stream.fail(st.err)
		} else {
			fail(w, http.StatusBadGateway, "api_error", st.err.Error())
		}
		return
	}

	c.parked = st.stop == "tool_use"
	c.asked = nil
	for _, b := range st.blocks {
		if b.Type == "tool_use" {
			c.asked = append(c.asked, b.ID)
		}
	}

	// One key per prefix of the input. A client that added a reminder only for
	// this request replays the history without it, and still lands here.
	reply := message{Role: "assistant", Content: st.blocks}
	s.mu.Lock()
	for _, k := range c.keys {
		delete(s.convs, k)
	}
	c.keys = c.keys[:0]
	for i := 1; i <= len(input); i++ {
		k := key(slices.Concat(prior, input[:i], []message{reply}))
		s.convs[k] = c
		c.keys = append(c.keys, k)
	}
	c.busy = false
	c.lastUsed = time.Now()
	s.mu.Unlock()

	log.Printf("conv %d: %s, effort %s, thinking %d block(s) / %d chunk(s), tool_use %v",
		c.id, st.stop, orDefault(c.effort), st.thoughts, st.chunks, c.asked)
	if st.stop == "end_turn" {
		log.Printf("conv %d: turn used %d output tokens, thinking included", c.id, st.turnOutput)
	}
	if stream != nil {
		stream.finish(st)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response{
		ID:         id,
		Type:       "message",
		Role:       "assistant",
		Model:      req.Model,
		Content:    st.blocks,
		StopReason: st.stop,
		Usage:      st.usage,
	})
}

// start opens a conversation for a history no live one continues, and returns
// what sends it the input. On failure it returns the HTTP status to answer.
func (s *server) start(req request, effort string, prior, input []message) (*conv, func(), int, error) {
	blocks, err := blocksOf(req.System)
	if err != nil {
		return nil, nil, http.StatusBadRequest, fmt.Errorf("system: %w", err)
	}
	system, err := joinText(blocks)
	if err != nil {
		return nil, nil, http.StatusBadRequest, fmt.Errorf("system: %w", err)
	}
	first := slices.Concat(prior, input)[0]

	// A new conversation takes its input as the prompt.
	if len(prior) == 0 {
		text, images, err := promptOf(input)
		if err != nil {
			return nil, nil, http.StatusBadRequest, err
		}
		c, err := s.open(req, effort, system, first, "", "")
		if err != nil {
			return nil, nil, http.StatusBadGateway, err
		}
		return c, func() { go c.run(s.ctx, text, images) }, 0, nil
	}

	// Otherwise claude resumes the history from a session file. Tool results
	// go in the file with it; any other input is the prompt.
	transcript, prompt, images := prior, restoredPrompt, []pi.Image(nil)
	if hasToolResults(input) {
		transcript = slices.Concat(prior, input)
	} else if prompt, images, err = promptOf(input); err != nil {
		return nil, nil, http.StatusBadRequest, err
	}

	sessionID := newUUID()
	path := filepath.Join(s.projects, sessionID+".jsonl")
	if err := writeTranscript(path, sessionID, s.cwd, req.Model, transcript); err != nil {
		return nil, nil, http.StatusBadRequest, err
	}
	c, err := s.open(req, effort, system, first, sessionID, path)
	if err != nil {
		os.Remove(path)
		return nil, nil, http.StatusBadGateway, err
	}
	log.Printf("conv %d: rebuilt from %d message(s)", c.id, len(transcript))
	return c, func() { go c.run(s.ctx, prompt, images) }, 0, nil
}

// open starts a claude process, resuming sessionID when it is set. It closes
// any idle conversation started from the same system prompt and first message:
// the client has moved on from it, to this one.
func (s *server) open(req request, effort, system string, first message, sessionID, transcript string) (*conv, error) {
	c := &conv{
		id:         s.nextID.Add(1),
		model:      req.Model,
		effort:     effort,
		steps:      make(chan step, 1),
		results:    make(chan map[string]pi.ToolResult, 1),
		answered:   map[string]pi.ToolResult{},
		root:       key([]message{{Role: "system", Content: []block{{Type: "text", Text: system}}}, first}),
		transcript: transcript,
		busy:       true,
		lastUsed:   time.Now(),
	}

	tools := make([]pi.Tool, 0, len(req.Tools))
	for _, t := range req.Tools {
		c.tools = append(c.tools, t.Name)
		tools = append(tools, pi.Tool{
			Name:        t.Name,
			Description: t.Description,
			Schema:      t.InputSchema,
			Execute: func(ctx context.Context, args json.RawMessage) (pi.ToolResult, error) {
				return c.execute(ctx, t.Name, args)
			},
		})
	}

	sess, err := pi.New(s.ctx, pi.Options{
		CWD:          s.cwd,
		Model:        req.Model,
		Effort:       effort,
		SystemPrompt: system,
		Resume:       sessionID,
		NoTools:      pi.NoToolsAll,
		CustomTools:  tools,
		// One empty source: no user, project or local settings, so nothing on
		// this machine auto-approves a tool or adds to the prompt.
		SettingSources:         []string{""},
		NoSessionPersistence:   true,
		IncludePartialMessages: true,
		Stderr:                 func(line string) { log.Printf("conv %d stderr: %s", c.id, line) },
	})
	if err != nil {
		return nil, err
	}
	sess.Subscribe(c.collect)
	c.sess = sess

	s.mu.Lock()
	var superseded []*conv
	for other := range s.all {
		if other.root == c.root && !other.busy {
			superseded = append(superseded, other)
		}
	}
	s.all[c] = struct{}{}
	s.mu.Unlock()
	for _, other := range superseded {
		log.Printf("conv %d: superseded by conv %d, closing", other.id, c.id)
		s.close(other)
	}
	return c, nil
}

// close ends a conversation: its keys, its process and its session file.
func (s *server) close(c *conv) {
	s.mu.Lock()
	for _, k := range c.keys {
		if s.convs[k] == c {
			delete(s.convs, k)
		}
	}
	c.keys = nil
	delete(s.all, c)
	s.mu.Unlock()

	c.sess.Close()
	if c.transcript != "" {
		os.Remove(c.transcript)
	}
}

// expire closes conversations left idle, such as one a client abandoned
// mid-tool-call. Each holds a claude process until then.
func (s *server) expire(idle time.Duration) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}

		s.mu.Lock()
		var stale []*conv
		for c := range s.all {
			if !c.busy && time.Since(c.lastUsed) > idle {
				stale = append(stale, c)
			}
		}
		s.mu.Unlock()
		for _, c := range stale {
			log.Printf("conv %d: idle for %s, closing", c.id, idle)
			s.close(c)
		}
	}
}

func (s *server) closeAll() {
	s.mu.Lock()
	all := slices.Collect(maps.Keys(s.all))
	s.mu.Unlock()
	for _, c := range all {
		s.close(c)
	}
}

// ---- conversation ----------------------------------------------------------

// step is what one HTTP reply carries: the blocks since the last reply, and why
// the model stopped.
type step struct {
	stop       string
	blocks     []block
	thoughts   int      // thinking blocks, text withheld or not
	chunks     int      // streamed thinking fragments
	usage      pi.Usage // the model call that produced this step
	turnOutput int      // output tokens of the whole turn, set at end_turn
	err        error
}

// conv is one claude process. Only the request holding it touches parked and
// asked; answered is only touched on pi-claude's read loop.
type conv struct {
	id      int64
	sess    *pi.Session
	steps   chan step
	results chan map[string]pi.ToolResult

	parked   bool
	asked    []string
	answered map[string]pi.ToolResult

	model      string   // the --model it was started with, as the client named it
	effort     string   // the --effort it was started with, empty for the default
	tools      []string // the tools the client declared, by bare name
	root       string   // hash of the system prompt and first message
	transcript string   // the session file it resumed, removed on close

	// Guarded by server.mu.
	keys     []string
	busy     bool
	lastUsed time.Time

	mu        sync.Mutex
	sink      *sse     // the streaming reply in flight, if any
	dropped   []string // declared tools claude did not offer the model
	usage     pi.Usage // the latest model call's
	pending   []block  // emitted since the last step
	thoughts  int      // thinking blocks since the last step
	chunks    int      // thinking fragments since the last step
	unclaimed []block  // tool_use blocks no tool call has matched yet
}

func (c *conv) droppedTools() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dropped
}

func (c *conv) setSink(s *sse) {
	c.mu.Lock()
	c.sink = s
	c.mu.Unlock()
}

func (c *conv) collect(ev pi.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()

	switch e := ev.(type) {
	case pi.ReadyEvent:
		log.Printf("conv %d ready: model %s, tools %v", c.id, e.Model, e.Tools)
		// claude leaves out a tool it does not accept, such as one whose schema
		// is invalid, and says nothing. The model would carry on without it.
		c.dropped = nil
		for _, name := range c.tools {
			if !slices.Contains(e.Tools, toolPrefix+name) {
				c.dropped = append(c.dropped, name)
			}
		}
	case pi.DeltaEvent:
		if e.Thinking {
			c.chunks++
		} else if c.sink != nil {
			c.sink.delta(e.Text)
		}
	case pi.ThinkingEvent:
		c.thoughts++
	case pi.UsageEvent:
		c.usage = e.Usage
	case pi.TextEvent:
		c.pending = append(c.pending, block{Type: "text", Text: e.Text})
		if c.sink != nil {
			c.sink.text(e.Text)
		}
	case pi.ToolCallEvent:
		input := map[string]any{}
		if e.Input != nil {
			input = e.Input
		}
		b := block{Type: "tool_use", ID: e.ID, Name: e.Name, Input: input}
		c.pending = append(c.pending, b)
		c.unclaimed = append(c.unclaimed, b)
		if c.sink != nil {
			c.sink.toolUse(b)
		}
	}
}

// drain takes what was emitted since the last step.
func (c *conv) drain(stop string) step {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := step{stop: stop, blocks: c.pending, thoughts: c.thoughts, chunks: c.chunks, usage: c.usage}
	if st.blocks == nil {
		st.blocks = []block{}
	}
	c.pending, c.thoughts, c.chunks = nil, 0, 0
	return st
}

// claim finds the tool_use a tool call belongs to. The MCP call does not carry
// the tool_use id, so it is matched on name and arguments. The assistant frame
// is always read before the call it causes, so the block is already here.
func (c *conv) claim(name string, args json.RawMessage) string {
	want := canonical(args)

	c.mu.Lock()
	defer c.mu.Unlock()
	for i, b := range c.unclaimed {
		if b.Name == name && canonical(b.Input) == want {
			c.unclaimed = slices.Delete(c.unclaimed, i, i+1)
			return b.ID
		}
	}
	panic(fmt.Sprintf("conv %d: %s called with %s, but no matching tool_use was seen", c.id, name, want))
}

// execute runs on pi-claude's read loop when the model calls a tool. Parking
// here parks the loop too: nothing more is read from claude until the client
// answers, which is what keeps the steps in order.
func (c *conv) execute(ctx context.Context, name string, args json.RawMessage) (pi.ToolResult, error) {
	id := c.claim(name, args)

	if _, ok := c.answered[id]; !ok {
		c.steps <- c.drain("tool_use")
		select {
		case results := <-c.results:
			maps.Copy(c.answered, results)
		case <-ctx.Done():
			return pi.ToolResult{}, ctx.Err()
		}
	}

	result, ok := c.answered[id]
	if !ok {
		panic(fmt.Sprintf("conv %d: the client answered, but not %s", c.id, id))
	}
	delete(c.answered, id)
	return result, nil
}

func (c *conv) run(ctx context.Context, text string, images []pi.Image) {
	turn, err := c.sess.Prompt(ctx, text, images...)
	if err == nil && turn.IsError {
		err = fmt.Errorf("claude ended the turn with %s: %s %s", turn.Subtype, turn.Result, strings.Join(turn.Errors, "; "))
	}
	st := c.drain("end_turn")
	st.turnOutput, st.err = turn.Usage.OutputTokens, err
	c.steps <- st
}

// ---- transcripts -------------------------------------------------------------

// toolPrefix is how claude names a tool from pi-claude's in-process MCP server.
const toolPrefix = "mcp__pi__"

// transcriptVersion is the Claude Code version the session file claims. Taken
// from a real session written by claude 2.1.276.
const transcriptVersion = "2.1.276"

var unsafePath = regexp.MustCompile(`[^a-zA-Z0-9]`)

// projectsDir is where claude keeps the sessions of a working directory.
func projectsDir(cwd string) string {
	config := os.Getenv("CLAUDE_CONFIG_DIR")
	if config == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		config = filepath.Join(home, ".claude")
	}
	return filepath.Join(config, "projects", unsafePath.ReplaceAllString(cwd, "-"))
}

// writeTranscript writes a history as a Claude Code session file, the shape
// claude --resume reads: one record per message, chained by parentUuid.
// Consecutive messages from one role are merged, as the API would.
func writeTranscript(path, sessionID, cwd, model string, history []message) error {
	if len(history) == 0 || history[0].Role != "user" {
		return errors.New("a history to restore must start with a user message")
	}

	var buf bytes.Buffer
	var parent *string
	now := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	for i, m := range mergeRoles(history) {
		content := transcriptContent(m)
		if len(content) == 0 {
			continue
		}
		msg := map[string]any{"role": m.Role, "content": content}
		if m.Role == "assistant" {
			stop := "end_turn"
			if slices.ContainsFunc(m.Content, func(b block) bool { return b.Type == "tool_use" }) {
				stop = "tool_use"
			}
			msg["id"] = fmt.Sprintf("msg_restored_%d", i)
			msg["type"] = "message"
			msg["model"] = model
			msg["stop_reason"] = stop
			msg["stop_sequence"] = nil
			msg["usage"] = map[string]int{"input_tokens": 0, "output_tokens": 0}
		}

		id := newUUID()
		line, err := json.Marshal(map[string]any{
			"parentUuid": parent, "isSidechain": false, "type": m.Role, "message": msg,
			"uuid": id, "timestamp": now, "userType": "external", "cwd": cwd,
			"sessionId": sessionID, "version": transcriptVersion,
		})
		if err != nil {
			return err
		}
		buf.Write(line)
		buf.WriteByte('\n')
		parent = &id
	}
	return os.WriteFile(path, buf.Bytes(), 0o600)
}

func mergeRoles(history []message) []message {
	var out []message
	for _, m := range history {
		if n := len(out); n > 0 && out[n-1].Role == m.Role {
			out[n-1].Content = slices.Concat(out[n-1].Content, m.Content)
			continue
		}
		out = append(out, message{Role: m.Role, Content: slices.Clone(m.Content)})
	}
	return out
}

// transcriptContent renders a message's blocks the way the API takes them.
// Whitespace-only text is dropped: the API rejects it.
func transcriptContent(m message) []any {
	var out []any
	for _, b := range m.Content {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) != "" {
				out = append(out, map[string]any{"type": "text", "text": b.Text})
			}
		case "image":
			out = append(out, map[string]any{"type": "image", "source": b.Source})
		case "tool_use":
			input := b.Input
			if input == nil {
				input = map[string]any{}
			}
			out = append(out, map[string]any{"type": "tool_use", "id": b.ID, "name": toolPrefix + b.Name, "input": input})
		case "tool_result":
			result := map[string]any{"type": "tool_result", "tool_use_id": b.ToolUseID, "is_error": b.IsError}
			if len(b.Images) == 0 {
				result["content"] = b.Content
			} else {
				var inner []any
				if b.Content != "" {
					inner = append(inner, map[string]any{"type": "text", "text": b.Content})
				}
				for _, src := range b.Images {
					inner = append(inner, map[string]any{"type": "image", "source": src})
				}
				result["content"] = inner
			}
			out = append(out, result)
		}
	}
	return out
}

func newUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
