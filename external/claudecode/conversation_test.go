package claudecode

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/TheLazyLemur/pi-claude/core"
	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// fakeTransport stands in for the claude subprocess.
type fakeTransport struct {
	mu     sync.Mutex
	writes [][]byte
	closed int
	waited int

	frames chan proto.Frame
	wrote  chan struct{}
}

func newFake() *fakeTransport {
	return &fakeTransport{
		frames: make(chan proto.Frame, 64),
		wrote:  make(chan struct{}, 64),
	}
}

func (f *fakeTransport) Write(data []byte) error {
	f.mu.Lock()
	cp := make([]byte, len(data))
	copy(cp, data)
	f.writes = append(f.writes, cp)
	f.mu.Unlock()

	select {
	case f.wrote <- struct{}{}:
	default:
	}
	return nil
}

func (f *fakeTransport) Frames() <-chan proto.Frame { return f.frames }

func (f *fakeTransport) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeTransport) Wait() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waited++
	return nil
}

// push sends a frame as the CLI would.
func (f *fakeTransport) push(t *testing.T, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Errorf("marshal frame: %v", err)
		return
	}
	var head struct {
		Type      string `json:"type"`
		Subtype   string `json:"subtype"`
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(raw, &head)
	f.frames <- proto.Frame{Type: head.Type, Subtype: head.Subtype, SessionID: head.SessionID, Raw: raw}
}

// awaitWrites blocks until n writes have been recorded.
func (f *fakeTransport) awaitWrites(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-f.wrote:
		case <-time.After(2 * time.Second):
			t.Errorf("timed out waiting for %d writes", n)
			return
		}
	}
}

// sent decodes everything written to the CLI.
func (f *fakeTransport) sent(t *testing.T) []map[string]any {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]map[string]any, 0, len(f.writes))
	for _, w := range f.writes {
		var m map[string]any
		if err := json.Unmarshal(w, &m); err != nil {
			t.Errorf("decode write: %v", err)
			continue
		}
		out = append(out, m)
	}
	return out
}

func assistantText(text string) map[string]any {
	return map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "text", "text": text}},
		},
	}
}

func assistantToolUse(id, name string, input map[string]any) map[string]any {
	return map[string]any{
		"type": "assistant",
		"message": map[string]any{
			"role":    "assistant",
			"content": []any{map[string]any{"type": "tool_use", "id": id, "name": name, "input": input}},
		},
	}
}

func successResult() map[string]any {
	return map[string]any{
		"type": "result", "subtype": "success", "result": "done",
		"total_cost_usd": 0.25, "num_turns": 3, "is_error": false,
		"usage": map[string]any{"input_tokens": 100, "output_tokens": 20},
	}
}

// backendFunc lets a test stand in as a backend without a process.
type backendFunc func(context.Context, core.Config, core.Runtime) (core.Conversation, error)

func (f backendFunc) Open(ctx context.Context, cfg core.Config, rt core.Runtime) (core.Conversation, error) {
	return f(ctx, cfg, rt)
}

// newTestSession wires a real core.core session onto a fake transport, so the tests
// exercise the port as well as the protocol.
func newTestSession(t *testing.T, f *fakeTransport, opts core.Options) *core.Session {
	t.Helper()
	sess, _ := openOver(t, f, opts)
	return sess
}

// openOver also hands back the conversation, for the CLI-only methods.
func openOver(t *testing.T, f *fakeTransport, opts core.Options) (*core.Session, *Conversation) {
	t.Helper()

	var conv *Conversation
	backend := backendFunc(func(ctx context.Context, cfg core.Config, rt core.Runtime) (core.Conversation, error) {
		conv = Start(ctx, f, cfg, rt)
		return conv, nil
	})

	sess, err := core.Open(context.Background(), backend, opts, "pi")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, conv
}

// runtimeWith is a minimal Runtime for tests that only need the tool list.
func runtimeWith(tools ...core.Tool) core.Runtime {
	sess, err := core.Open(context.Background(),
		backendFunc(func(context.Context, core.Config, core.Runtime) (core.Conversation, error) {
			return nil, nil
		}),
		core.Options{CustomTools: tools}, "pi")
	if err != nil {
		panic(err)
	}
	return sess
}

func TestSession_PromptSendsInitializeThenUserMessage(t *testing.T) {
	// given
	// ... a session with a fake CLI behind it
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... the first prompt is sent and the CLI answers
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, successResult())
	}()
	if _, err := sess.Prompt(context.Background(), "hello"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... an initialize control request precedes the user message
	sent := f.sent(t)
	if len(sent) < 2 {
		t.Fatalf("wrote %d messages, want 2", len(sent))
	}
	if sent[0]["type"] != "control_request" {
		t.Fatalf("first write = %v, want control_request", sent[0]["type"])
	}
	if sent[0]["request"].(map[string]any)["subtype"] != "initialize" {
		t.Fatalf("first write subtype = %v", sent[0]["request"])
	}
	if sent[1]["type"] != "user" {
		t.Fatalf("second write = %v, want user", sent[1]["type"])
	}
}

func TestSession_PromptWithImagesSendsContentBlocks(t *testing.T) {
	// given
	// ... a session with a fake CLI behind it, and a PNG to attach
	f := newFake()
	sess := newTestSession(t, f, core.Options{})
	png := core.Image{MediaType: "image/png", Data: []byte{1, 2, 3}}

	// when
	// ... a prompt goes out with the image attached
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, successResult())
	}()
	if _, err := sess.Prompt(context.Background(), "what is this?", png); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the user message carries the image as a base64 block, then the text
	content := f.sent(t)[1]["message"].(map[string]any)["content"].([]any)
	if len(content) != 2 {
		t.Fatalf("content = %v, want image then text", content)
	}
	image := content[0].(map[string]any)
	source := image["source"].(map[string]any)
	if image["type"] != "image" || source["type"] != "base64" || source["media_type"] != "image/png" || source["data"] != "AQID" {
		t.Fatalf("image block = %v", image)
	}
	text := content[1].(map[string]any)
	if text["type"] != "text" || text["text"] != "what is this?" {
		t.Fatalf("text block = %v", text)
	}
}

func TestSession_InitializeRegistersCustomTools(t *testing.T) {
	// given
	// ... a session carrying one custom tool
	f := newFake()
	sess := newTestSession(t, f, core.Options{CustomTools: []core.Tool{greetTool()}})

	// when
	// ... the session initialises
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "hi")

	// then
	// ... the SDK MCP server is declared in the initialize request
	init := f.sent(t)[0]["request"].(map[string]any)
	servers := init["sdkMcpServers"].([]any)
	if len(servers) != 1 || servers[0] != "pi" {
		t.Fatalf("sdkMcpServers = %v", servers)
	}
}

func TestSession_PromptReturnsTurnFromResult(t *testing.T) {
	// given
	// ... a CLI that answers with text and then a result
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... a prompt runs to completion
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, assistantText("the answer"))
		f.push(t, successResult())
	}()
	turn, err := sess.Prompt(context.Background(), "question")

	// then
	// ... the turn carries the text, the result and the accounting
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if turn.Text != "the answer" {
		t.Fatalf("text = %q", turn.Text)
	}
	if turn.Result != "done" || turn.Turns != 3 {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.CostUSD != 0.25 || turn.Usage.InputTokens != 100 {
		t.Fatalf("accounting = %+v", turn)
	}
}

func TestSession_SubscribeReceivesTextAndToolCalls(t *testing.T) {
	// given
	// ... a subscriber collecting events
	f := newFake()
	sess := newTestSession(t, f, core.Options{CustomTools: []core.Tool{greetTool()}})

	var mu sync.Mutex
	var kinds []string
	var toolName string
	sess.Subscribe(func(ev core.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch e := ev.(type) {
		case core.TextEvent:
			kinds = append(kinds, "text")
		case core.ToolCallEvent:
			kinds = append(kinds, "tool")
			toolName = e.Name
		case core.TurnEvent:
			kinds = append(kinds, "turn")
		}
	})

	// when
	// ... the CLI streams text, a tool call, and a result
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, assistantText("thinking out loud"))
		f.push(t, assistantToolUse("t1", "mcp__pi__greet", map[string]any{"name": "dan"}))
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... every event arrived in order, with our tool reported by its bare name
	mu.Lock()
	defer mu.Unlock()
	if len(kinds) != 3 || kinds[0] != "text" || kinds[1] != "tool" || kinds[2] != "turn" {
		t.Fatalf("events = %v", kinds)
	}
	if toolName != "greet" {
		t.Fatalf("tool name = %q, want the bare name", toolName)
	}
}

func TestSession_UnsubscribeStopsDelivery(t *testing.T) {
	// given
	// ... a subscriber that immediately cancels
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	var mu sync.Mutex
	count := 0
	cancel := sess.Subscribe(func(core.Event) {
		mu.Lock()
		count++
		mu.Unlock()
	})
	cancel()

	// when
	// ... events flow
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, assistantText("ignored"))
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... nothing was delivered after cancelling
	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Fatalf("received %d events after unsubscribe", count)
	}
}

func TestSession_CustomToolIsExecuted(t *testing.T) {
	// given
	// ... a session exposing the greet tool
	f := newFake()
	sess := newTestSession(t, f, core.Options{CustomTools: []core.Tool{greetTool()}})

	// when
	// ... the CLI asks the toolset to run it
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type":       "control_request",
			"request_id": "r1",
			"request": map[string]any{
				"subtype":     "mcp_message",
				"server_name": "pi",
				"message": map[string]any{
					"jsonrpc": "2.0", "id": 7, "method": "tools/call",
					"params": map[string]any{"name": "greet", "arguments": map[string]any{"name": "dan"}},
				},
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "greet dan")

	// then
	// ... the answer went back as a JSON-RPC result carrying the tool's text
	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	if reply == nil {
		t.Fatal("no control_response was written")
	}
	body := reply["response"].(map[string]any)["response"].(map[string]any)
	mcp := body["mcp_response"].(map[string]any)
	if mcp["id"].(float64) != 7 {
		t.Fatalf("jsonrpc id = %v, want 7", mcp["id"])
	}
	content := mcp["result"].(map[string]any)["content"].([]any)
	if content[0].(map[string]any)["text"] != "hello dan" {
		t.Fatalf("tool text = %v", content[0])
	}
}

func TestSession_ApproveToolDeniesByBareName(t *testing.T) {
	// given
	// ... an approver that refuses everything and records what it saw
	f := newFake()
	var seen string
	sess := newTestSession(t, f, core.Options{
		CustomTools: []core.Tool{greetTool()},
		ApproveTool: func(_ context.Context, req core.ToolRequest) core.Decision {
			seen = req.Name
			return core.Deny("not today")
		},
	})

	// when
	// ... the CLI asks permission for our tool
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type":       "control_request",
			"request_id": "r2",
			"request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "mcp__pi__greet",
				"tool_use_id": "t9", "input": map[string]any{"name": "dan"},
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the approver saw the bare name and the denial reached the CLI
	if seen != "greet" {
		t.Fatalf("approver saw %q, want the bare name", seen)
	}
	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	body := reply["response"].(map[string]any)["response"].(map[string]any)
	if body["behavior"] != "deny" || body["message"] != "not today" {
		t.Fatalf("decision = %v", body)
	}
}

func TestSession_ApproveToolDefaultsToAllow(t *testing.T) {
	// given
	// ... a session with no approver configured
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... the CLI asks permission
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type": "control_request", "request_id": "r3",
			"request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "Read",
				"tool_use_id": "t1", "input": map[string]any{"path": "x"},
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... it is allowed, and the original input is echoed back as the CLI requires
	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	body := reply["response"].(map[string]any)["response"].(map[string]any)
	if body["behavior"] != "allow" {
		t.Fatalf("behavior = %v", body["behavior"])
	}
	if body["updatedInput"].(map[string]any)["path"] != "x" {
		t.Fatalf("updatedInput = %v", body["updatedInput"])
	}
}

func TestSession_ReadyEventCarriesOfferedTools(t *testing.T) {
	// given
	// ... a subscriber watching for the init handshake
	f := newFake()
	sess, conv := openOver(t, f, core.Options{})

	ready := make(chan core.ReadyEvent, 1)
	sess.Subscribe(func(ev core.Event) {
		if e, ok := ev.(core.ReadyEvent); ok {
			ready <- e
		}
	})

	// when
	// ... the CLI reports what it is offering
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type": "system", "subtype": "init", "session_id": "sess-1",
			"model": "claude-x", "tools": []any{"Read", "Grep"},
		})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the tool list and session id are visible without parsing anything
	select {
	case e := <-ready:
		if e.SessionID != "sess-1" || e.Model != "claude-x" {
			t.Fatalf("ready = %+v", e)
		}
		if len(e.Tools) != 2 || e.Tools[0] != "Read" {
			t.Fatalf("tools = %v", e.Tools)
		}
	case <-time.After(time.Second):
		t.Fatal("no ready event")
	}
	if conv.ID() != "sess-1" {
		t.Fatalf("session id = %q", conv.ID())
	}
	if len(conv.OfferedTools()) != 2 {
		t.Fatalf("offered tools = %v", conv.OfferedTools())
	}
}

func TestSession_CloseIsIdempotent(t *testing.T) {
	// given
	// ... an open session
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... it is closed twice
	first := sess.Close()
	second := sess.Close()

	// then
	// ... neither call errors and the transport is closed once
	if first != nil || second != nil {
		t.Fatalf("close errors: %v %v", first, second)
	}
	if f.closed != 1 {
		t.Fatalf("transport closed %d times, want 1", f.closed)
	}
}

func TestSession_PromptAfterCloseFails(t *testing.T) {
	// given
	// ... a closed session
	f := newFake()
	sess := newTestSession(t, f, core.Options{})
	sess.Close()

	// when
	// ... a prompt is attempted
	_, err := sess.Prompt(context.Background(), "hello")

	// then
	// ... it fails loudly rather than hanging
	if err == nil {
		t.Fatal("expected an error prompting a closed session")
	}
}

func TestSession_CancelledPromptInterruptsTheCLI(t *testing.T) {
	// given
	// ... a turn in flight that the caller is about to give up on
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		f.awaitWrites(t, 2) // initialize, then the prompt
		cancel()
	}()

	// when
	// ... the caller's context is cancelled before any result arrives
	_, err := sess.Prompt(ctx, "long running")

	// then
	// ... an interrupt is sent, so the CLI stops rather than billing on alone
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	f.awaitWrites(t, 1)
	var interrupted bool
	for _, m := range f.sent(t) {
		if m["type"] != "control_request" {
			continue
		}
		if m["request"].(map[string]any)["subtype"] == "interrupt" {
			interrupted = true
		}
	}
	if !interrupted {
		t.Fatal("no interrupt was sent for the abandoned turn")
	}
}

func TestSession_CloseReapsTheSubprocess(t *testing.T) {
	// given
	// ... an open session
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... it is closed
	if err := sess.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// then
	// ... the process was waited on, so it does not linger as a zombie
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.waited != 1 {
		t.Fatalf("transport waited %d times, want 1", f.waited)
	}
}

func TestSession_SecondPromptWhileOneIsInFlight(t *testing.T) {
	// given
	// ... a session with a turn already under way, whose result is held back
	// ... until the second prompt has been answered: sent any earlier, the
	// ... first turn can finish first and the second prompt is not refused
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	started := make(chan struct{})
	release := make(chan struct{})
	go func() {
		f.awaitWrites(t, 2)
		close(started)
		<-release
		f.push(t, successResult())
	}()

	inFlight := make(chan error, 1)
	go func() {
		_, err := sess.Prompt(context.Background(), "first")
		inFlight <- err
	}()
	<-started

	// when
	// ... a second prompt is sent before the first has finished
	_, err := sess.Prompt(context.Background(), "second")
	close(release)

	// then
	// ... it is refused rather than queued, so the caller decides what to do
	if !errors.Is(err, core.ErrPromptInFlight) {
		t.Fatalf("err = %v, want core.ErrPromptInFlight", err)
	}
	if first := <-inFlight; first != nil {
		t.Fatalf("first prompt: %v", first)
	}
}
