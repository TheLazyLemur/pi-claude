package claudecode

import (
	"context"
	"encoding/json"
	"sync"
	"testing"

	"github.com/TheLazyLemur/pi-claude/core"
)

func collect(sess *core.Session) (*[]core.Event, *sync.Mutex) {
	var mu sync.Mutex
	var events []core.Event
	sess.Subscribe(func(ev core.Event) {
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
	})
	return &events, &mu
}

func TestSession_PartialMessagesBecomeDeltas(t *testing.T) {
	// given
	// ... a session subscribed to streaming deltas
	f := newFake()
	sess := newTestSession(t, f, core.Options{IncludePartialMessages: true})
	events, mu := collect(sess)

	// when
	// ... the CLI streams a text delta and a thinking delta
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "delta": map[string]any{"type": "text_delta", "text": "hel"},
		}})
		f.push(t, map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "delta": map[string]any{"type": "thinking_delta", "thinking": "hmm"},
		}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... both arrive as deltas, tagged by kind
	mu.Lock()
	defer mu.Unlock()
	var deltas []core.DeltaEvent
	for _, ev := range *events {
		if d, ok := ev.(core.DeltaEvent); ok {
			deltas = append(deltas, d)
		}
	}
	if len(deltas) != 2 {
		t.Fatalf("deltas = %v", deltas)
	}
	if deltas[0].Text != "hel" || deltas[0].Thinking {
		t.Fatalf("text delta = %+v", deltas[0])
	}
	if deltas[1].Text != "hmm" || !deltas[1].Thinking {
		t.Fatalf("thinking delta = %+v", deltas[1])
	}
}

func TestSession_WithheldThinkingIsStillReported(t *testing.T) {
	// given
	// ... a session subscribed to streaming deltas
	f := newFake()
	sess := newTestSession(t, f, core.Options{IncludePartialMessages: true})
	events, mu := collect(sess)

	// when
	// ... the CLI streams thinking with its text withheld, as it does for Opus 5
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "content_block_delta", "delta": map[string]any{"type": "thinking_delta", "thinking": ""},
		}})
		f.push(t, map[string]any{"type": "assistant", "message": map[string]any{
			"role": "assistant", "content": []any{map[string]any{"type": "thinking", "thinking": "", "signature": "sig"}},
		}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... both still arrive, so a subscriber can tell the model thought
	mu.Lock()
	defer mu.Unlock()
	var deltas []core.DeltaEvent
	var thoughts []core.ThinkingEvent
	for _, ev := range *events {
		switch e := ev.(type) {
		case core.DeltaEvent:
			deltas = append(deltas, e)
		case core.ThinkingEvent:
			thoughts = append(thoughts, e)
		}
	}
	if len(deltas) != 1 || !deltas[0].Thinking || deltas[0].Text != "" {
		t.Fatalf("deltas = %+v, want one empty thinking delta", deltas)
	}
	if len(thoughts) != 1 || thoughts[0].Text != "" {
		t.Fatalf("thinking = %+v, want one block with its text withheld", thoughts)
	}
}

func TestSession_EachModelCallReportsItsUsage(t *testing.T) {
	// given
	// ... a session subscribed to streaming events
	f := newFake()
	sess := newTestSession(t, f, core.Options{IncludePartialMessages: true})
	events, mu := collect(sess)

	// when
	// ... the CLI finishes one model call and reports its final usage
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "message_delta",
			"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 120,
				"cache_read_input_tokens": 3000, "cache_creation_input_tokens": 400,
			},
		}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... that call's usage arrives on its own, separate from the turn's total
	mu.Lock()
	defer mu.Unlock()
	var calls []core.UsageEvent
	for _, ev := range *events {
		if u, ok := ev.(core.UsageEvent); ok {
			calls = append(calls, u)
		}
	}
	want := core.Usage{InputTokens: 10, OutputTokens: 120, CacheReadInputTokens: 3000, CacheCreationInputTokens: 400}
	if len(calls) != 1 || calls[0].Usage != want || !calls[0].Final {
		t.Fatalf("usage events = %+v, want one final with %+v", calls, want)
	}
}

func TestSession_ModelCallUsageIsReportedWhenTheCallStarts(t *testing.T) {
	// given
	// ... a session subscribed to streaming events
	f := newFake()
	sess := newTestSession(t, f, core.Options{IncludePartialMessages: true})
	events, mu := collect(sess)

	// when
	// ... the CLI starts a model call, whose input side is already known
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "stream_event", "event": map[string]any{
			"type": "message_start",
			"message": map[string]any{"usage": map[string]any{
				"input_tokens": 10, "output_tokens": 1,
				"cache_read_input_tokens": 3000, "cache_creation_input_tokens": 400,
			}},
		}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... its usage arrives before any tool runs, marked as not final
	mu.Lock()
	defer mu.Unlock()
	var calls []core.UsageEvent
	for _, ev := range *events {
		if u, ok := ev.(core.UsageEvent); ok {
			calls = append(calls, u)
		}
	}
	want := core.Usage{InputTokens: 10, OutputTokens: 1, CacheReadInputTokens: 3000, CacheCreationInputTokens: 400}
	if len(calls) != 1 || calls[0].Usage != want || calls[0].Final {
		t.Fatalf("usage events = %+v, want one not final with %+v", calls, want)
	}
}

func TestSession_StatusAndCompactionEvents(t *testing.T) {
	// given
	// ... a subscriber watching lifecycle events
	f := newFake()
	sess := newTestSession(t, f, core.Options{})
	events, mu := collect(sess)

	// when
	// ... the CLI reports compacting, then a compaction boundary
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "system", "subtype": "status", "status": "compacting"})
		f.push(t, map[string]any{"type": "system", "subtype": "compact_boundary",
			"compact_metadata": map[string]any{"trigger": "auto"}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... both surface as typed events instead of being dropped
	mu.Lock()
	defer mu.Unlock()
	var status *core.StatusEvent
	var compact *core.CompactEvent
	for _, ev := range *events {
		switch e := ev.(type) {
		case core.StatusEvent:
			status = &e
		case core.CompactEvent:
			compact = &e
		}
	}
	if status == nil || status.Status != "compacting" {
		t.Fatalf("status event = %v", status)
	}
	if compact == nil || compact.Trigger != "auto" {
		t.Fatalf("compact event = %v", compact)
	}
}

func TestSession_AuthStatusEvent(t *testing.T) {
	// given
	// ... a subscriber watching authentication
	f := newFake()
	sess := newTestSession(t, f, core.Options{})
	events, mu := collect(sess)

	// when
	// ... the CLI reports authentication progress
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "auth_status", "isAuthenticating": true,
			"output": []any{"Authenticating..."}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... it arrives as an core.AuthEvent
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *events {
		if e, ok := ev.(core.AuthEvent); ok {
			if !e.Authenticating || len(e.Output) != 1 {
				t.Fatalf("auth event = %+v", e)
			}
			return
		}
	}
	t.Fatal("no auth event")
}

func TestSession_StructuredOutputReachesTheTurn(t *testing.T) {
	// given
	// ... a session constrained to a JSON answer shape
	type verdict struct {
		Pass bool `json:"pass"`
	}
	f := newFake()
	sess := newTestSession(t, f, core.Options{OutputSchema: core.SchemaFor[verdict]()})

	// when
	// ... the CLI returns a structured result
	go func() {
		f.awaitWrites(t, 2)
		result := successResult()
		result["structured_output"] = map[string]any{"pass": true}
		f.push(t, result)
	}()
	turn, err := sess.Prompt(context.Background(), "judge it")

	// then
	// ... the structured output is available and decodes into the caller's type
	if err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if len(turn.StructuredOutput) == 0 {
		t.Fatal("structured output missing")
	}
	var got verdict
	if err := json.Unmarshal(turn.StructuredOutput, &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !got.Pass {
		t.Fatalf("verdict = %+v", got)
	}
}

func TestSession_RateLimitEvent(t *testing.T) {
	// given
	// ... a subscriber watching for rate limit reports
	f := newFake()
	sess := newTestSession(t, f, core.Options{})
	events, mu := collect(sess)

	// when
	// ... the CLI reports the current window
	go func() {
		f.awaitWrites(t, 2)
		f.push(t, map[string]any{"type": "rate_limit_event", "rate_limit_info": map[string]any{
			"status": "allowed", "resetsAt": 1788034200, "rateLimitType": "five_hour",
			"unifiedWindows": map[string]any{
				"five_hour": map[string]any{"utilization": 0.05},
				"seven_day": map[string]any{"utilization": 0.1},
			},
		}})
		f.push(t, successResult())
	}()
	sess.Prompt(context.Background(), "go")

	// then
	// ... the window and utilisation are available without parsing raw JSON
	mu.Lock()
	defer mu.Unlock()
	for _, ev := range *events {
		if e, ok := ev.(core.RateLimitEvent); ok {
			if e.Status != "allowed" || e.Window != "five_hour" {
				t.Fatalf("rate limit = %+v", e)
			}
			if e.Utilization["seven_day"] != 0.1 {
				t.Fatalf("utilization = %v", e.Utilization)
			}
			if e.ResetsAt.IsZero() {
				t.Fatal("resetsAt not decoded")
			}
			return
		}
	}
	t.Fatal("no rate limit event")
}
