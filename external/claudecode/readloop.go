package claudecode

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/TheLazyLemur/pi-claude/core"
	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// readLoop turns protocol frames into events, and answers the control requests
// the CLI sends back to us.
func (c *Conversation) readLoop() {
	for frame := range c.transport.Frames() {
		if frame.Err != nil {
			c.rt.Emit(core.ErrorEvent{Err: frame.Err})
			continue
		}

		switch frame.Type {
		case "system":
			c.onSystem(frame)
		case "assistant":
			c.onAssistant(frame)
		case "user":
			c.onUser(frame)
		case "tool_progress":
			c.onToolProgress(frame)
		case "control_request":
			c.onControlRequest(frame)
		case "control_response":
			c.onControlResponse(frame)
		case "stream_event":
			c.onStreamEvent(frame)
		case "auth_status":
			c.onAuthStatus(frame)
		case "rate_limit_event":
			c.onRateLimit(frame)
		case "result":
			c.onResult(frame)
		}
	}
}

func (c *Conversation) onSystem(frame proto.Frame) {
	switch frame.Subtype {
	case "status":
		var msg struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(frame.Raw, &msg) == nil {
			c.rt.Emit(core.StatusEvent{Status: msg.Status})
		}
		return
	case "compact_boundary":
		var msg struct {
			Metadata struct {
				Trigger   string `json:"trigger"`
				PreTokens int    `json:"pre_tokens"`
			} `json:"compact_metadata"`
		}
		if json.Unmarshal(frame.Raw, &msg) == nil {
			c.rt.Emit(core.CompactEvent{Trigger: msg.Metadata.Trigger, TokensBefore: msg.Metadata.PreTokens})
		}
		return
	case "init":
	default:
		return
	}

	var init struct {
		SessionID string   `json:"session_id"`
		Model     string   `json:"model"`
		CWD       string   `json:"cwd"`
		Tools     []string `json:"tools"`
	}
	if err := json.Unmarshal(frame.Raw, &init); err != nil {
		c.rt.Emit(core.ErrorEvent{Err: fmt.Errorf("pi: parse init: %w", err)})
		return
	}

	c.mu.Lock()
	c.sessionID = init.SessionID
	c.model = init.Model
	c.offeredTools = init.Tools
	c.mu.Unlock()

	c.rt.Emit(core.ReadyEvent{
		SessionID: init.SessionID,
		Model:     init.Model,
		CWD:       init.CWD,
		Tools:     init.Tools,
	})
}

type contentBlock struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	ID       string          `json:"id"`
	Name     string          `json:"name"`
	Input    map[string]any  `json:"input"`
	ToolUse  string          `json:"tool_use_id"`
	Content  json.RawMessage `json:"content"`
	IsError  bool            `json:"is_error"`
}

func (c *Conversation) onAssistant(frame proto.Frame) {
	var msg struct {
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(frame.Raw, &msg); err != nil {
		c.rt.Emit(core.ErrorEvent{Err: fmt.Errorf("pi: parse assistant: %w", err)})
		return
	}

	for _, block := range msg.Message.Content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			c.rt.Emit(core.TextEvent{Text: block.Text})
		case "thinking":
			if block.Thinking != "" {
				c.rt.Emit(core.ThinkingEvent{Text: block.Thinking})
			}
		case "tool_use":
			c.rt.Emit(core.ToolCallEvent{
				ID:    block.ID,
				Name:  c.tools.bare(block.Name),
				Input: block.Input,
				Mine:  c.tools.owns(block.Name),
			})
		}
	}
}

func (c *Conversation) onUser(frame proto.Frame) {
	// Content is a plain string when the CLI echoes our prompt, and a block
	// list when it carries tool results, so it is decoded leniently.
	var msg struct {
		UUID    string `json:"uuid"`
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}

	var blocks []contentBlock
	json.Unmarshal(msg.Message.Content, &blocks)

	for _, block := range blocks {
		if block.Type != "tool_result" {
			continue
		}
		c.rt.Emit(core.ToolResultEvent{
			ID:      block.ToolUse,
			Text:    decodeToolResultContent(block.Content),
			IsError: block.IsError,
		})
	}
}

// decodeToolResultContent copes with tool_result content being either a plain
// string or a list of content blocks.
func decodeToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}

	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}

	var blocks []contentBlock
	if json.Unmarshal(raw, &blocks) != nil {
		return string(raw)
	}

	var out string
	for _, b := range blocks {
		out += b.Text
	}
	return out
}

func (c *Conversation) onToolProgress(frame proto.Frame) {
	var msg struct {
		ToolUseID string  `json:"tool_use_id"`
		ToolName  string  `json:"tool_name"`
		Elapsed   float64 `json:"elapsed_time_seconds"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}
	c.rt.Emit(core.ToolProgressEvent{
		ID:      msg.ToolUseID,
		Name:    c.tools.bare(msg.ToolName),
		Elapsed: time.Duration(msg.Elapsed * float64(time.Second)),
	})
}

func (c *Conversation) onResult(frame proto.Frame) {
	var res struct {
		Subtype      string          `json:"subtype"`
		Result       string          `json:"result"`
		IsError      bool            `json:"is_error"`
		Errors       []string        `json:"errors"`
		NumTurns     int             `json:"num_turns"`
		TotalCostUSD float64         `json:"total_cost_usd"`
		Usage        core.Usage      `json:"usage"`
		Denials      []core.Denial   `json:"permission_denials"`
		Structured   json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(frame.Raw, &res); err != nil {
		c.rt.Emit(core.ErrorEvent{Err: fmt.Errorf("pi: parse result: %w", err)})
		return
	}

	// Text and Duration are filled in by the caller from the events it saw, so
	// every backend does not have to keep its own tally.
	turn := core.Turn{
		StructuredOutput: res.Structured,
		Subtype:          res.Subtype,
		Result:           res.Result,
		IsError:          res.IsError,
		Errors:           res.Errors,
		Turns:            res.NumTurns,
		CostUSD:          res.TotalCostUSD,
		Usage:            res.Usage,
		Denials:          res.Denials,
	}

	c.mu.Lock()
	pending := c.pending
	c.pending = nil
	c.mu.Unlock()

	if pending == nil {
		return
	}
	select {
	case pending <- turn:
	default:
	}
}

func (c *Conversation) onStreamEvent(frame proto.Frame) {
	var msg struct {
		Event struct {
			Type  string `json:"type"`
			Delta struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
			} `json:"delta"`
		} `json:"event"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}

	switch msg.Event.Delta.Type {
	case "text_delta":
		if msg.Event.Delta.Text != "" {
			c.rt.Emit(core.DeltaEvent{Text: msg.Event.Delta.Text})
		}
	case "thinking_delta":
		if msg.Event.Delta.Thinking != "" {
			c.rt.Emit(core.DeltaEvent{Text: msg.Event.Delta.Thinking, Thinking: true})
		}
	}
}

func (c *Conversation) onAuthStatus(frame proto.Frame) {
	var msg struct {
		IsAuthenticating bool     `json:"isAuthenticating"`
		Output           []string `json:"output"`
		Error            string   `json:"error"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}
	c.rt.Emit(core.AuthEvent{
		Authenticating: msg.IsAuthenticating,
		Output:         msg.Output,
		Error:          msg.Error,
	})
}

func (c *Conversation) onRateLimit(frame proto.Frame) {
	var msg struct {
		Info struct {
			Status         string `json:"status"`
			RateLimitType  string `json:"rateLimitType"`
			ResetsAt       int64  `json:"resetsAt"`
			UnifiedWindows map[string]struct {
				Utilization float64 `json:"utilization"`
			} `json:"unifiedWindows"`
		} `json:"rate_limit_info"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}

	usage := make(map[string]float64, len(msg.Info.UnifiedWindows))
	for name, window := range msg.Info.UnifiedWindows {
		usage[name] = window.Utilization
	}

	ev := core.RateLimitEvent{
		Status:      msg.Info.Status,
		Window:      msg.Info.RateLimitType,
		Utilization: usage,
	}
	if msg.Info.ResetsAt > 0 {
		ev.ResetsAt = time.Unix(msg.Info.ResetsAt, 0)
	}
	c.rt.Emit(ev)
}
