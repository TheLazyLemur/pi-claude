package pi

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// readLoop turns protocol frames into events, and answers the control requests
// the CLI sends back to us.
func (s *Session) readLoop() {
	for frame := range s.transport.Frames() {
		if frame.Err != nil {
			s.emit(ErrorEvent{Err: frame.Err})
			continue
		}

		switch frame.Type {
		case "system":
			s.onSystem(frame)
		case "assistant":
			s.onAssistant(frame)
		case "user":
			s.onUser(frame)
		case "tool_progress":
			s.onToolProgress(frame)
		case "control_request":
			s.onControlRequest(frame)
		case "control_response":
			s.onControlResponse(frame)
		case "stream_event":
			s.onStreamEvent(frame)
		case "auth_status":
			s.onAuthStatus(frame)
		case "rate_limit_event":
			s.onRateLimit(frame)
		case "result":
			s.onResult(frame)
		}
	}
}

func (s *Session) onSystem(frame proto.Frame) {
	switch frame.Subtype {
	case "status":
		var msg struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(frame.Raw, &msg) == nil {
			s.emit(StatusEvent{Status: msg.Status})
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
			s.emit(CompactEvent{Trigger: msg.Metadata.Trigger, TokensBefore: msg.Metadata.PreTokens})
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
		s.emit(ErrorEvent{Err: fmt.Errorf("pi: parse init: %w", err)})
		return
	}

	s.mu.Lock()
	s.sessionID = init.SessionID
	s.model = init.Model
	s.offeredTools = init.Tools
	s.mu.Unlock()

	s.emit(ReadyEvent{
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

func (s *Session) onAssistant(frame proto.Frame) {
	var msg struct {
		Message struct {
			Content []contentBlock `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(frame.Raw, &msg); err != nil {
		s.emit(ErrorEvent{Err: fmt.Errorf("pi: parse assistant: %w", err)})
		return
	}

	for _, block := range msg.Message.Content {
		switch block.Type {
		case "text":
			if block.Text == "" {
				continue
			}
			s.appendText(block.Text)
			s.emit(TextEvent{Text: block.Text})
		case "thinking":
			if block.Thinking != "" {
				s.emit(ThinkingEvent{Text: block.Thinking})
			}
		case "tool_use":
			s.emit(ToolCallEvent{
				ID:    block.ID,
				Name:  s.tools.bare(block.Name),
				Input: block.Input,
				Mine:  s.tools.owns(block.Name),
			})
		}
	}
}

func (s *Session) onUser(frame proto.Frame) {
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
		s.emit(ToolResultEvent{
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

func (s *Session) onToolProgress(frame proto.Frame) {
	var msg struct {
		ToolUseID string  `json:"tool_use_id"`
		ToolName  string  `json:"tool_name"`
		Elapsed   float64 `json:"elapsed_time_seconds"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}
	s.emit(ToolProgressEvent{
		ID:      msg.ToolUseID,
		Name:    s.tools.bare(msg.ToolName),
		Elapsed: time.Duration(msg.Elapsed * float64(time.Second)),
	})
}

func (s *Session) onResult(frame proto.Frame) {
	var res struct {
		Subtype      string          `json:"subtype"`
		Result       string          `json:"result"`
		IsError      bool            `json:"is_error"`
		Errors       []string        `json:"errors"`
		NumTurns     int             `json:"num_turns"`
		TotalCostUSD float64         `json:"total_cost_usd"`
		Usage        Usage           `json:"usage"`
		Denials      []Denial        `json:"permission_denials"`
		Structured   json.RawMessage `json:"structured_output"`
	}
	if err := json.Unmarshal(frame.Raw, &res); err != nil {
		s.emit(ErrorEvent{Err: fmt.Errorf("pi: parse result: %w", err)})
		return
	}

	s.mu.Lock()
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()

	turn := Turn{
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
	if pending != nil {
		turn.Text = pending.text.String()
		turn.Duration = time.Since(pending.started)
	}

	s.emit(TurnEvent{Turn: turn})

	if pending != nil {
		select {
		case pending.done <- turn:
		default:
		}
	}
}

func (s *Session) appendText(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending != nil {
		s.pending.text.WriteString(text)
	}
}

// onStreamEvent turns partial-message chunks into deltas. Requires
// Options.IncludePartialMessages.
func (s *Session) onStreamEvent(frame proto.Frame) {
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
			s.emit(DeltaEvent{Text: msg.Event.Delta.Text})
		}
	case "thinking_delta":
		if msg.Event.Delta.Thinking != "" {
			s.emit(DeltaEvent{Text: msg.Event.Delta.Thinking, Thinking: true})
		}
	}
}

func (s *Session) onAuthStatus(frame proto.Frame) {
	var msg struct {
		IsAuthenticating bool     `json:"isAuthenticating"`
		Output           []string `json:"output"`
		Error            string   `json:"error"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}
	s.emit(AuthEvent{
		Authenticating: msg.IsAuthenticating,
		Output:         msg.Output,
		Error:          msg.Error,
	})
}

func (s *Session) onRateLimit(frame proto.Frame) {
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

	ev := RateLimitEvent{
		Status:      msg.Info.Status,
		Window:      msg.Info.RateLimitType,
		Utilization: usage,
	}
	if msg.Info.ResetsAt > 0 {
		ev.ResetsAt = time.Unix(msg.Info.ResetsAt, 0)
	}
	s.emit(ev)
}
