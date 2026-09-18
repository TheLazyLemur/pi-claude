package anthropic_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeAPI is an Anthropic-shaped endpoint that answers with canned bodies and
// remembers what it was sent, so a test can assert on the wire without a
// network or a model.
type fakeAPI struct {
	t *testing.T

	mu       sync.Mutex
	replies  []string
	requests []map[string]any
	headers  []http.Header
	paths    []string
	fallback string
	status   int
	server   *httptest.Server
}

// newFakeAPI serves replies in order. Once they run out, fallback is served
// for every further call, which is how a loop that will not stop is tested.
func newFakeAPI(t *testing.T, fallback string, replies ...string) *fakeAPI {
	t.Helper()

	api := &fakeAPI{t: t, replies: replies, fallback: fallback, status: http.StatusOK}
	api.server = httptest.NewServer(http.HandlerFunc(api.serve))
	t.Cleanup(api.server.Close)
	return api
}

func (a *fakeAPI) serve(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		a.t.Errorf("decode request: %v", err)
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	a.mu.Lock()
	a.requests = append(a.requests, body)
	a.headers = append(a.headers, r.Header.Clone())
	a.paths = append(a.paths, r.URL.Path)

	reply := a.fallback
	if len(a.replies) > 0 {
		reply, a.replies = a.replies[0], a.replies[1:]
	}
	a.mu.Unlock()

	w.Header().Set("content-type", "application/json")
	w.WriteHeader(a.status)
	w.Write([]byte(reply))
}

func (a *fakeAPI) url() string {
	return a.server.URL
}

func (a *fakeAPI) calls() []map[string]any {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]map[string]any(nil), a.requests...)
}

func (a *fakeAPI) call(i int) map[string]any {
	a.t.Helper()

	calls := a.calls()
	if i >= len(calls) {
		a.t.Fatalf("wanted call %d, only %d were made", i, len(calls))
	}
	return calls[i]
}

func (a *fakeAPI) path(i int) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.paths) {
		a.t.Fatalf("wanted call %d, only %d were made", i, len(a.paths))
	}
	return a.paths[i]
}

func (a *fakeAPI) header(i int, name string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	if i >= len(a.headers) {
		a.t.Fatalf("wanted call %d, only %d were made", i, len(a.headers))
	}
	return a.headers[i].Get(name)
}

// messages digs the request's message list out, decoded far enough to assert on.
func messages(t *testing.T, call map[string]any) []map[string]any {
	t.Helper()

	raw, found := call["messages"].([]any)
	if !found {
		t.Fatalf("request has no messages: %v", call)
	}

	out := make([]map[string]any, 0, len(raw))
	for _, msg := range raw {
		out = append(out, msg.(map[string]any))
	}
	return out
}

// blocks digs the content blocks out of one message.
func blocks(t *testing.T, msg map[string]any) []map[string]any {
	t.Helper()

	raw, found := msg["content"].([]any)
	if !found {
		t.Fatalf("message has no content blocks: %v", msg)
	}

	out := make([]map[string]any, 0, len(raw))
	for _, block := range raw {
		out = append(out, block.(map[string]any))
	}
	return out
}

// Canned replies, written as wire JSON so the protocol this backend speaks is
// visible in the tests rather than only in the code.
const (
	replyText = `{
		"id": "msg_1", "type": "message", "role": "assistant", "model": "deepseek-v4-flash",
		"content": [{"type": "text", "text": "hello dan"}],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 11, "output_tokens": 3}
	}`

	replyThinking = `{
		"id": "msg_2", "type": "message", "role": "assistant", "model": "deepseek-v4-flash",
		"content": [
			{"type": "thinking", "thinking": "they want a greeting"},
			{"type": "text", "text": "hello dan"}
		],
		"stop_reason": "end_turn",
		"usage": {"input_tokens": 11, "output_tokens": 3}
	}`

	replyToolUse = `{
		"id": "msg_3", "type": "message", "role": "assistant", "model": "deepseek-v4-flash",
		"content": [{"type": "tool_use", "id": "call_1", "name": "greet", "input": {"name": "dan"}}],
		"stop_reason": "tool_use",
		"usage": {"input_tokens": 20, "output_tokens": 7}
	}`
)
