package claudecode

import (
	"context"
	"sync"
	"testing"

	"github.com/TheLazyLemur/pi-claude/core"
)

func TestSession_InitRequestCarriesHooks(t *testing.T) {
	// given
	// ... a session with a PreToolUse hook scoped to a matcher
	f := newFake()
	sess := newTestSession(t, f, core.Options{
		Hooks: map[core.HookEvent][]core.HookMatcher{
			core.HookPreToolUse: {{
				Matcher: "Bash",
				Timeout: 15,
				Hooks: []core.HookFunc{func(context.Context, core.HookInput) (core.HookOutput, error) {
					return core.HookContinue(), nil
				}},
			}},
		},
	})

	// when
	// ... the session initialises
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the initialize request declares the hook, its matcher and its timeout
	init := f.sent(t)[0]["request"].(map[string]any)
	matchers := init["hooks"].(map[string]any)["PreToolUse"].([]any)
	first := matchers[0].(map[string]any)
	if first["matcher"] != "Bash" {
		t.Fatalf("matcher = %v", first["matcher"])
	}
	if first["timeout"].(float64) != 15 {
		t.Fatalf("timeout = %v", first["timeout"])
	}
	if len(first["hookCallbackIds"].([]any)) != 1 {
		t.Fatalf("callback ids = %v", first["hookCallbackIds"])
	}
}

func TestSession_HookCallbackRuns(t *testing.T) {
	// given
	// ... a hook that records what it saw and adds context
	f := newFake()
	var mu sync.Mutex
	var seen core.HookInput
	sess := newTestSession(t, f, core.Options{
		Hooks: map[core.HookEvent][]core.HookMatcher{
			core.HookPreToolUse: {{Hooks: []core.HookFunc{
				func(_ context.Context, in core.HookInput) (core.HookOutput, error) {
					mu.Lock()
					seen = in
					mu.Unlock()
					out := core.HookContinue()
					out.AdditionalContext = "prefer the existing helper"
					return out, nil
				},
			}}},
		},
	})

	// when
	// ... the CLI fires the hook
	go func() {
		f.awaitWrites(t, 2)
		callbackID := core.HookCallbackID(core.HookPreToolUse, 0, 0)
		f.push(t, map[string]any{
			"type": "control_request", "request_id": "h1",
			"request": map[string]any{
				"subtype": "hook_callback", "callback_id": callbackID,
				"input": map[string]any{
					"hook_event_name": "PreToolUse",
					"tool_name":       "Bash",
					"tool_input":      map[string]any{"command": "ls"},
					"session_id":      "s1",
					"cwd":             "/tmp",
				},
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the hook saw a typed input and its output reached the CLI
	mu.Lock()
	defer mu.Unlock()
	if seen.Event != core.HookPreToolUse || seen.ToolName != "Bash" {
		t.Fatalf("hook input = %+v", seen)
	}
	if seen.ToolInput["command"] != "ls" {
		t.Fatalf("tool input = %v", seen.ToolInput)
	}

	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	body := reply["response"].(map[string]any)["response"].(map[string]any)
	if body["continue"] != true {
		t.Fatalf("continue = %v", body["continue"])
	}
	specific := body["hookSpecificOutput"].(map[string]any)
	if specific["additionalContext"] != "prefer the existing helper" {
		t.Fatalf("hookSpecificOutput = %v", specific)
	}
}

func TestSession_HookCanBlockAToolCall(t *testing.T) {
	// given
	// ... a hook that refuses the call
	f := newFake()
	sess := newTestSession(t, f, core.Options{
		Hooks: map[core.HookEvent][]core.HookMatcher{
			core.HookPreToolUse: {{Hooks: []core.HookFunc{
				func(context.Context, core.HookInput) (core.HookOutput, error) {
					return core.HookDeny("no shelling out"), nil
				},
			}}},
		},
	})

	// when
	// ... the CLI fires it
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type": "control_request", "request_id": "h2",
			"request": map[string]any{
				"subtype": "hook_callback", "callback_id": core.HookCallbackID(core.HookPreToolUse, 0, 0),
				"input": map[string]any{"hook_event_name": "PreToolUse", "tool_name": "Bash"},
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the denial and its reason reach the CLI
	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	body := reply["response"].(map[string]any)["response"].(map[string]any)
	specific := body["hookSpecificOutput"].(map[string]any)
	if specific["permissionDecision"] != "deny" {
		t.Fatalf("decision = %v", specific)
	}
	if specific["permissionDecisionReason"] != "no shelling out" {
		t.Fatalf("reason = %v", specific)
	}
}

func TestSession_UnknownHookCallbackIsAnError(t *testing.T) {
	// given
	// ... a session with no hooks registered
	f := newFake()
	sess := newTestSession(t, f, core.Options{})

	// when
	// ... the CLI fires a callback that does not exist
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type": "control_request", "request_id": "h3",
			"request": map[string]any{"subtype": "hook_callback", "callback_id": "nope"},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... an error response goes back rather than silence
	var reply map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_response" {
			reply = m
		}
	}
	if reply == nil {
		t.Fatal("no response to an unknown hook callback")
	}
	body := reply["response"].(map[string]any)
	if body["subtype"] != "error" {
		t.Fatalf("response = %v", body)
	}
}
