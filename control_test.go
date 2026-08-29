package pi

import (
	"context"
	"testing"
	"time"
)

// replyTo answers the most recent control_request of the given subtype.
func replyTo(t *testing.T, f *fakeTransport, subtype string, body map[string]any) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		for _, m := range f.sent(t) {
			if m["type"] != "control_request" {
				continue
			}
			req := m["request"].(map[string]any)
			if req["subtype"] != subtype {
				continue
			}
			f.push(t, map[string]any{
				"type": "control_response",
				"response": map[string]any{
					"subtype": "success", "request_id": m["request_id"], "response": body,
				},
			})
			return
		}
		select {
		case <-deadline:
			t.Errorf("no control_request with subtype %q was sent", subtype)
			return
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func TestSession_MCPStatusReturnsTheResponse(t *testing.T) {
	// given
	// ... a session and a CLI that will report its MCP servers
	f := newFake()
	sess := newTestSession(t, f, Options{})

	go replyTo(t, f, "mcp_status", map[string]any{
		"servers": []any{map[string]any{"name": "fs", "status": "connected"}},
	})

	// when
	// ... status is requested
	status, err := sess.MCPStatus(context.Background())

	// then
	// ... the decoded response comes back to the caller
	if err != nil {
		t.Fatalf("mcp status: %v", err)
	}
	servers := status["servers"].([]any)
	if servers[0].(map[string]any)["name"] != "fs" {
		t.Fatalf("servers = %v", servers)
	}
}

func TestSession_RewindFilesReturnsWhatWouldChange(t *testing.T) {
	// given
	// ... a session with checkpointing on
	f := newFake()
	sess := newTestSession(t, f, Options{EnableFileCheckpointing: true})

	go replyTo(t, f, "rewind_files", map[string]any{"files": []any{"main.go"}})

	// when
	// ... a dry-run rewind is requested
	result, err := sess.RewindFiles(context.Background(), "msg-1", true)

	// then
	// ... the CLI's answer reaches the caller
	if err != nil {
		t.Fatalf("rewind: %v", err)
	}
	if result["files"].([]any)[0] != "main.go" {
		t.Fatalf("files = %v", result["files"])
	}

	var sent map[string]any
	for _, m := range f.sent(t) {
		if m["type"] == "control_request" {
			if req := m["request"].(map[string]any); req["subtype"] == "rewind_files" {
				sent = req
			}
		}
	}
	if sent["user_message_id"] != "msg-1" || sent["dry_run"] != true {
		t.Fatalf("rewind request = %v", sent)
	}
}

func TestSession_ControlErrorIsReturned(t *testing.T) {
	// given
	// ... a CLI that rejects the request
	f := newFake()
	sess := newTestSession(t, f, Options{})

	go func() {
		deadline := time.After(2 * time.Second)
		for {
			for _, m := range f.sent(t) {
				if m["type"] != "control_request" {
					continue
				}
				f.push(t, map[string]any{
					"type": "control_response",
					"response": map[string]any{
						"subtype": "error", "request_id": m["request_id"], "error": "not supported",
					},
				})
				return
			}
			select {
			case <-deadline:
				return
			case <-time.After(5 * time.Millisecond):
			}
		}
	}()

	// when
	// ... a control request is made
	_, err := sess.MCPStatus(context.Background())

	// then
	// ... the CLI's error reaches the caller rather than a timeout
	if err == nil || err.Error() != "pi: not supported" {
		t.Fatalf("err = %v", err)
	}
}

func TestSession_SetMaxThinkingTokens(t *testing.T) {
	// given
	// ... an open session
	f := newFake()
	sess := newTestSession(t, f, Options{})

	// when
	// ... a thinking budget is set and then cleared
	sess.SetMaxThinkingTokens(2048)
	sess.SetMaxThinkingTokens(0)
	f.awaitWrites(t, 2)

	// then
	// ... the budget is sent, and clearing sends an explicit null
	sent := f.sent(t)
	first := sent[0]["request"].(map[string]any)
	if first["subtype"] != "set_max_thinking_tokens" || first["max_thinking_tokens"].(float64) != 2048 {
		t.Fatalf("first = %v", first)
	}
	second := sent[1]["request"].(map[string]any)
	if second["max_thinking_tokens"] != nil {
		t.Fatalf("clearing sent %v, want null", second["max_thinking_tokens"])
	}
}

func TestSession_ControlRequestOnClosedSessionFails(t *testing.T) {
	// given
	// ... a closed session
	f := newFake()
	sess := newSession(context.Background(), f, Options{})
	sess.Close()

	// when
	// ... a control request is attempted
	_, err := sess.MCPStatus(context.Background())

	// then
	// ... it fails immediately rather than waiting for a reply that cannot come
	if err == nil {
		t.Fatal("expected an error on a closed session")
	}
}

func TestSession_ToolRequestCarriesTheCLIsReasoning(t *testing.T) {
	// given
	// ... an approver that inspects why it is being asked
	f := newFake()
	var seen ToolRequest
	sess := newTestSession(t, f, Options{
		PermissionMode: PermissionModeDefault,
		ApproveTool: func(_ context.Context, req ToolRequest) Decision {
			seen = req
			return Allow()
		},
	})

	// when
	// ... the CLI asks about a tool call it has already flagged
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{
			"type": "control_request", "request_id": "p1",
			"request": map[string]any{
				"subtype": "can_use_tool", "tool_name": "Read", "tool_use_id": "t1",
				"input":           map[string]any{"file_path": "/etc/passwd"},
				"blocked_path":    "/etc/passwd",
				"decision_reason": "Path outside allowed directories",
				"agent_id":        "sub-agent-123",
			},
		})
		f.awaitWrites(t, 1)
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the approver can see what the CLI objected to, and which agent asked
	if seen.BlockedPath != "/etc/passwd" {
		t.Fatalf("blocked path = %q", seen.BlockedPath)
	}
	if seen.DecisionReason != "Path outside allowed directories" {
		t.Fatalf("decision reason = %q", seen.DecisionReason)
	}
	if seen.AgentID != "sub-agent-123" {
		t.Fatalf("agent id = %q", seen.AgentID)
	}
}
