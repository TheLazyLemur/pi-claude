package core

import (
	"encoding/json"
	"time"
)

// Event is something that happened during a turn. Handle the ones you care
// about with a type switch:
//
//	sess.Subscribe(func(ev pi.Event) {
//	    switch e := ev.(type) {
//	    case pi.TextEvent:
//	        fmt.Print(e.Text)
//	    case pi.ToolCallEvent:
//	        log.Printf("calling %s", e.Name)
//	    }
//	})
type Event interface{ isEvent() }

// ReadyEvent reports the CLI's opening handshake, including the tools it is
// actually offering the model. Worth asserting on if you meant to lock tools
// down.
type ReadyEvent struct {
	SessionID string
	Model     string
	Tools     []string
	CWD       string
}

// TextEvent is assistant text.
type TextEvent struct{ Text string }

// ThinkingEvent is a block of assistant reasoning. Text is empty when the
// runtime withholds it, as Claude Code does for Opus 5 and Sonnet 5; the model
// still thought.
type ThinkingEvent struct{ Text string }

// ToolCallEvent is the model asking to run a tool. Name is the bare name.
type ToolCallEvent struct {
	ID    string
	Name  string
	Input map[string]any
	Mine  bool
}

// ToolResultEvent is the outcome of a tool call.
type ToolResultEvent struct {
	ID      string
	Text    string
	IsError bool
}

// ToolProgressEvent reports a long-running tool still working.
type ToolProgressEvent struct {
	ID      string
	Name    string
	Elapsed time.Duration
}

// DeniedEvent reports a tool call your approver refused.
type DeniedEvent struct {
	Name   string
	Reason string
}

// DeltaEvent is a streamed fragment of the assistant's answer. Only emitted
// when Options.IncludePartialMessages is set. A thinking fragment's Text is
// empty when the runtime withholds the reasoning.
type DeltaEvent struct {
	Text     string
	Thinking bool
}

// UsageEvent reports the tokens of one model call. A turn with tool calls makes
// several calls, and Turn.Usage is their sum; the last call's usage is the size
// of the context. Only emitted when Options.IncludePartialMessages is set.
//
// Each call reports twice. When it starts, the input and cache counts are
// complete but the output is not, and Final is false. When it ends, all counts
// are complete and Final is true. A tool the call asks for can run before the
// end is reported.
type UsageEvent struct {
	Usage Usage
	Final bool
}

// StatusEvent reports a session status change, such as "compacting".
type StatusEvent struct{ Status string }

// CompactEvent reports that the conversation was compacted.
type CompactEvent struct {
	Trigger      string
	TokensBefore int
}

// AuthEvent reports authentication progress.
type AuthEvent struct {
	Authenticating bool
	Output         []string
	Error          string
}

// RateLimitEvent reports usage against the account's rate limit windows.
type RateLimitEvent struct {
	// Status is "allowed" or "rejected".
	Status string

	// Window is the limit that applies, e.g. "five_hour".
	Window string

	// ResetsAt is when the current window resets.
	ResetsAt time.Time

	// Utilization is the fraction used per window, keyed by window name.
	Utilization map[string]float64
}

// TurnEvent closes a turn and carries its accounting.
type TurnEvent struct{ Turn Turn }

// ErrorEvent reports a protocol or transport failure.
type ErrorEvent struct{ Err error }

func (ReadyEvent) isEvent()        {}
func (TextEvent) isEvent()         {}
func (ThinkingEvent) isEvent()     {}
func (ToolCallEvent) isEvent()     {}
func (ToolResultEvent) isEvent()   {}
func (ToolProgressEvent) isEvent() {}
func (DeniedEvent) isEvent()       {}
func (DeltaEvent) isEvent()        {}
func (UsageEvent) isEvent()        {}
func (StatusEvent) isEvent()       {}
func (CompactEvent) isEvent()      {}
func (AuthEvent) isEvent()         {}
func (RateLimitEvent) isEvent()    {}
func (TurnEvent) isEvent()         {}
func (ErrorEvent) isEvent()        {}

// Usage counts tokens for a turn.
type Usage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens"`
}

// Denial records a tool call that was refused.
type Denial struct {
	ToolName  string `json:"tool_name"`
	ToolUseID string `json:"tool_use_id"`
}

// Turn is the outcome of one Prompt.
type Turn struct {
	// Text is every assistant text block in the turn, concatenated.
	Text string

	// Result is the CLI's own summary of the turn.
	Result string

	// IsError reports whether the turn failed.
	IsError bool

	// Subtype distinguishes success from error_max_turns, error_max_budget_usd
	// and friends.
	Subtype string

	// Errors carries failure detail when IsError is set.
	Errors []string

	// Turns counts model round trips.
	Turns int

	// CostUSD is what the turn cost.
	CostUSD float64

	// Usage counts tokens.
	Usage Usage

	// Denials lists tool calls that were refused.
	Denials []Denial

	// Duration is wall-clock time for the turn.
	Duration time.Duration

	// StructuredOutput is the validated JSON answer when Options.OutputSchema
	// was set. Decode it into your own type.
	StructuredOutput json.RawMessage
}
