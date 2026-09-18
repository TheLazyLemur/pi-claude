package anthropic_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	pi "github.com/TheLazyLemur/pi-claude"
	"github.com/TheLazyLemur/pi-claude/external/anthropic"
)

type greetParams struct {
	Name string `json:"name" desc:"Who to greet"`
}

func greetTool() pi.Tool {
	return pi.DefineTool("greet", "Greet someone by name",
		func(_ context.Context, p greetParams) (pi.ToolResult, error) {
			return pi.Text("hello %s", p.Name), nil
		})
}

// open starts a session against the fake endpoint, with everything else left
// at the hardcoded defaults.
func open(t *testing.T, api *fakeAPI, opts pi.Options) *pi.Session {
	t.Helper()

	cfg := anthropic.Defaults()
	cfg.BaseURL = api.url()

	sess, err := pi.Open(context.Background(), anthropic.NewWith(cfg), opts)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

func TestDefaults_PointAtTheOllamaAnthropicEndpoint(t *testing.T) {
	// given
	// ... the hardcoded configuration
	cfg := anthropic.Defaults()

	// when
	// ... it is read
	models := cfg.Models

	// then
	// ... it names Ollama's Anthropic endpoint and the DeepSeek flash model
	if cfg.BaseURL != "http://localhost:11434" {
		t.Fatalf("base url = %q", cfg.BaseURL)
	}
	if cfg.AuthToken != "ollama" || cfg.APIKey != "" {
		t.Fatalf("auth token / api key = %q / %q", cfg.AuthToken, cfg.APIKey)
	}
	if models.Opus != "deepseek-v4-flash:cloud" || models.Sonnet != "deepseek-v4-flash:cloud" ||
		models.Haiku != "deepseek-v4-flash:cloud" || models.Subagent != "deepseek-v4-flash:cloud" {
		t.Fatalf("models = %+v", models)
	}
}

func TestModels_ResolveMapsAliasesAndPassesIdsThrough(t *testing.T) {
	// given
	// ... a distinct model per alias
	models := anthropic.Models{
		Opus:     "big",
		Sonnet:   "middling",
		Haiku:    "small",
		Subagent: "delegate",
	}

	// when
	// ... each alias, the empty default, and an unknown id are resolved
	got := []string{
		models.Resolve("opus"),
		models.Resolve("sonnet"),
		models.Resolve("haiku"),
		models.Resolve("subagent"),
		models.Resolve(""),
		models.Resolve("qwen3:32b"),
	}

	// then
	// ... aliases map, empty falls back to sonnet, and anything else is a literal id
	want := []string{"big", "middling", "small", "delegate", "middling", "qwen3:32b"}
	for i, name := range want {
		if got[i] != name {
			t.Fatalf("resolved[%d] = %q, want %q", i, got[i], name)
		}
	}
}

func TestPrompt_SpeaksTheAnthropicWireProtocol(t *testing.T) {
	// given
	// ... a session against an endpoint that answers with one text block
	api := newFakeAPI(t, replyText)
	sess := open(t, api, pi.Options{})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the request went to /v1/messages, authenticated, carrying the prompt
	if path := api.path(0); path != "/v1/messages" {
		t.Fatalf("path = %q", path)
	}
	if auth := api.header(0, "authorization"); auth != "Bearer ollama" {
		t.Fatalf("authorization = %q", auth)
	}
	if version := api.header(0, "anthropic-version"); version != "2023-06-01" {
		t.Fatalf("anthropic-version = %q", version)
	}
	if key := api.header(0, "x-api-key"); key != "" {
		t.Fatalf("x-api-key = %q, want it left off when no key is configured", key)
	}

	call := api.call(0)
	if call["model"] != "deepseek-v4-flash:cloud" {
		t.Fatalf("model = %v", call["model"])
	}
	sent := messages(t, call)
	if len(sent) != 1 || sent[0]["role"] != "user" {
		t.Fatalf("messages = %v", sent)
	}
	if text := blocks(t, sent[0])[0]["text"]; text != "greet dan" {
		t.Fatalf("prompt text = %v", text)
	}
	if turn.Text != "hello dan" || turn.Subtype != "success" {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.Usage.InputTokens != 11 || turn.Usage.OutputTokens != 3 {
		t.Fatalf("usage = %+v", turn.Usage)
	}
}

func TestPrompt_ThinkingBlocksBecomeThinkingEvents(t *testing.T) {
	// given
	// ... an endpoint that answers with a thinking block before its text
	api := newFakeAPI(t, replyThinking)
	sess := open(t, api, pi.Options{})

	var thoughts []string
	sess.Subscribe(func(ev pi.Event) {
		if e, ok := ev.(pi.ThinkingEvent); ok {
			thoughts = append(thoughts, e.Text)
		}
	})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the reasoning was reported separately from the answer
	if len(thoughts) != 1 || thoughts[0] != "they want a greeting" {
		t.Fatalf("thinking = %v", thoughts)
	}
	if turn.Text != "hello dan" {
		t.Fatalf("text = %q", turn.Text)
	}
}

func TestPrompt_OffersTheHostsToolsAndFeedsResultsBack(t *testing.T) {
	// given
	// ... an endpoint that calls the host's tool, then answers
	api := newFakeAPI(t, replyText, replyToolUse, replyText)
	sess := open(t, api, pi.Options{CustomTools: []pi.Tool{greetTool()}})

	var saw []string
	sess.Subscribe(func(ev pi.Event) {
		switch e := ev.(type) {
		case pi.ToolCallEvent:
			saw = append(saw, "call:"+e.Name)
		case pi.ToolResultEvent:
			saw = append(saw, "result:"+e.Text)
		}
	})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the tool was declared, run, and its result sent back as a tool_result
	declared := api.call(0)["tools"].([]any)
	if len(declared) != 1 {
		t.Fatalf("tools = %v", declared)
	}
	if name := declared[0].(map[string]any)["name"]; name != "greet" {
		t.Fatalf("declared tool = %v", name)
	}
	if _, found := declared[0].(map[string]any)["input_schema"]; !found {
		t.Fatalf("declared tool has no input_schema: %v", declared[0])
	}

	second := messages(t, api.call(1))
	if len(second) != 3 {
		t.Fatalf("second call carried %d messages, want prompt, assistant and result", len(second))
	}
	result := blocks(t, second[2])[0]
	if result["type"] != "tool_result" || result["tool_use_id"] != "call_1" {
		t.Fatalf("tool result block = %v", result)
	}
	if !strings.Contains(result["content"].(string), "hello dan") {
		t.Fatalf("tool result content = %v", result["content"])
	}

	if len(saw) != 2 || saw[0] != "call:greet" || saw[1] != "result:hello dan" {
		t.Fatalf("events = %v", saw)
	}
	if turn.Turns != 2 {
		t.Fatalf("turns = %d, want one round trip per model call", turn.Turns)
	}
	if turn.Usage.InputTokens != 31 || turn.Usage.OutputTokens != 10 {
		t.Fatalf("usage = %+v, want it accumulated across the loop", turn.Usage)
	}
}

func TestPrompt_RefusesImagesItCannotSend(t *testing.T) {
	// given
	// ... a session on this backend, which does not speak image blocks yet
	api := newFakeAPI(t, replyText)
	sess := open(t, api, pi.Options{})
	png := pi.Image{MediaType: "image/png", Data: []byte{1, 2, 3}}

	// when
	// ... a prompt carries an image
	_, err := sess.Prompt(context.Background(), "what is this?", png)

	// then
	// ... it is refused before anything goes on the wire, not silently dropped
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err = %v, want an image refusal", err)
	}
	if len(api.calls()) != 0 {
		t.Fatalf("made %d calls, want none", len(api.calls()))
	}
}

func TestPrompt_ToolResultWithImagesIsAnError(t *testing.T) {
	// given
	// ... an endpoint that calls a tool whose result carries a picture
	picture := pi.DefineTool("greet", "Greet someone with a picture",
		func(_ context.Context, _ greetParams) (pi.ToolResult, error) {
			return pi.ToolResult{Text: "hello", Images: []pi.Image{{MediaType: "image/png", Data: []byte{1}}}}, nil
		})
	api := newFakeAPI(t, replyText, replyToolUse, replyText)
	sess := open(t, api, pi.Options{CustomTools: []pi.Tool{picture}})

	// when
	// ... the prompt runs the tool
	_, err := sess.Prompt(context.Background(), "greet dan")

	// then
	// ... the turn fails rather than sending the result without its image
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("err = %v, want an image refusal", err)
	}
	if len(api.calls()) != 1 {
		t.Fatalf("made %d calls, want only the one that asked for the tool", len(api.calls()))
	}
}

func TestPrompt_DeniedToolIsRefusedInTheHostAndReportedToTheModel(t *testing.T) {
	// given
	// ... a host that refuses every tool call
	api := newFakeAPI(t, replyText, replyToolUse, replyText)
	sess := open(t, api, pi.Options{
		CustomTools: []pi.Tool{greetTool()},
		ApproveTool: func(context.Context, pi.ToolRequest) pi.Decision {
			return pi.Deny("not today")
		},
	})

	var denied []string
	sess.Subscribe(func(ev pi.Event) {
		if e, ok := ev.(pi.DeniedEvent); ok {
			denied = append(denied, e.Name+":"+e.Reason)
		}
	})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the tool never ran, and the model was told why
	if len(denied) != 1 || denied[0] != "greet:not today" {
		t.Fatalf("denied = %v", denied)
	}
	if len(turn.Denials) != 1 || turn.Denials[0].ToolName != "greet" {
		t.Fatalf("denials = %+v", turn.Denials)
	}

	result := blocks(t, messages(t, api.call(1))[2])[0]
	if result["is_error"] != true {
		t.Fatalf("tool result block = %v, want it marked an error", result)
	}
	if !strings.Contains(result["content"].(string), "not today") {
		t.Fatalf("tool result content = %v", result["content"])
	}
}

func TestPrompt_MaxTurnsStopsALoopThatWillNotEnd(t *testing.T) {
	// given
	// ... an endpoint that asks for the same tool forever
	api := newFakeAPI(t, replyToolUse)
	sess := open(t, api, pi.Options{CustomTools: []pi.Tool{greetTool()}, MaxTurns: 2})

	// when
	// ... a prompt runs
	turn, err := sess.Prompt(context.Background(), "greet dan")
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the loop stopped at the bound and said so
	if turn.Subtype != "error_max_turns" || !turn.IsError {
		t.Fatalf("turn = %+v", turn)
	}
	if turn.Turns != 2 {
		t.Fatalf("turns = %d, want the loop bounded at MaxTurns", turn.Turns)
	}
	if len(api.calls()) != 2 {
		t.Fatalf("calls = %d, want no request past the bound", len(api.calls()))
	}
}

func TestPrompt_CarriesTheConversationForward(t *testing.T) {
	// given
	// ... a session that has already had one exchange
	api := newFakeAPI(t, replyText)
	sess := open(t, api, pi.Options{})
	if _, err := sess.Prompt(context.Background(), "greet dan"); err != nil {
		t.Fatalf("first prompt: %v", err)
	}

	// when
	// ... a second prompt runs
	if _, err := sess.Prompt(context.Background(), "and again"); err != nil {
		t.Fatalf("second prompt: %v", err)
	}

	// then
	// ... the earlier exchange went with it
	sent := messages(t, api.call(1))
	if len(sent) != 3 {
		t.Fatalf("second call carried %d messages, want the whole history", len(sent))
	}
	if sent[1]["role"] != "assistant" {
		t.Fatalf("second message = %v, want the previous answer", sent[1])
	}
	if text := blocks(t, sent[2])[0]["text"]; text != "and again" {
		t.Fatalf("third message = %v", text)
	}
}

func TestPrompt_SendsTheSystemPrompt(t *testing.T) {
	// given
	// ... a session with a system prompt and an addition to it
	api := newFakeAPI(t, replyText)
	sess := open(t, api, pi.Options{
		SystemPrompt:       "You are terse.",
		AppendSystemPrompt: "Answer in English.",
	})

	// when
	// ... a prompt runs
	if _, err := sess.Prompt(context.Background(), "greet dan"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... both reached the wire as one system field
	system, found := api.call(0)["system"].(string)
	if !found {
		t.Fatalf("request has no system field: %v", api.call(0))
	}
	if !strings.Contains(system, "You are terse.") || !strings.Contains(system, "Answer in English.") {
		t.Fatalf("system = %q", system)
	}
}

func TestOpen_RefusesOptionsThisBackendCannotHonour(t *testing.T) {
	// given
	// ... options that only mean something to the Claude Code CLI
	unsupported := map[string]pi.Options{
		"hooks":       {Hooks: map[pi.HookEvent][]pi.HookMatcher{pi.HookPreToolUse: {{}}}},
		"agents":      {Agents: map[string]pi.Agent{"scout": {}}},
		"schema":      {OutputSchema: map[string]any{"type": "object"}},
		"resume":      {Resume: "abc"},
		"builtins":    {Tools: []string{"Read"}},
		"partials":    {IncludePartialMessages: true},
		"budget":      {MaxBudgetUSD: 1},
		"permissions": {PermissionMode: pi.PermissionModeAcceptEdits},
		"effort":      {Effort: "high"},
	}

	for name, opts := range unsupported {
		t.Run(name, func(t *testing.T) {
			// when
			// ... a session is opened with them
			sess, err := pi.Open(context.Background(), anthropic.New(), opts)

			// then
			// ... it fails at the seam rather than quietly ignoring them
			if err == nil {
				sess.Close()
				t.Fatalf("open succeeded, want it to refuse %s", name)
			}
			if !strings.Contains(err.Error(), "pi: ") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestPrompt_ReadyEventNamesTheResolvedModel(t *testing.T) {
	// given
	// ... a session asking for the opus alias
	api := newFakeAPI(t, replyText)
	sess := open(t, api, pi.Options{Model: "opus", CustomTools: []pi.Tool{greetTool()}})

	var ready pi.ReadyEvent
	sess.Subscribe(func(ev pi.Event) {
		if e, ok := ev.(pi.ReadyEvent); ok {
			ready = e
		}
	})

	// when
	// ... a prompt runs
	if _, err := sess.Prompt(context.Background(), "greet dan"); err != nil {
		t.Fatalf("prompt: %v", err)
	}

	// then
	// ... the handshake reported the id the alias resolved to, and the tools on offer
	if ready.Model != "deepseek-v4-flash:cloud" {
		t.Fatalf("ready model = %q", ready.Model)
	}
	if len(ready.Tools) != 1 || ready.Tools[0] != "greet" {
		t.Fatalf("ready tools = %v", ready.Tools)
	}
}

func TestPrompt_ApiFailureIsAnError(t *testing.T) {
	// given
	// ... an endpoint that rejects the request
	api := newFakeAPI(t, `{"type":"error","error":{"type":"not_found_error","message":"model not found"}}`)
	api.status = http.StatusNotFound
	sess := open(t, api, pi.Options{})

	// when
	// ... a prompt runs
	_, err := sess.Prompt(context.Background(), "greet dan")

	// then
	// ... the caller is told, rather than getting an empty turn
	if err == nil {
		t.Fatalf("prompt succeeded, want the endpoint's error surfaced")
	}
	if !strings.Contains(err.Error(), "model not found") {
		t.Fatalf("error = %v", err)
	}
}
