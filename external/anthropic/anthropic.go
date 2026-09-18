// Package anthropic is a backend that speaks the Anthropic Messages API over
// HTTP, with no CLI in the way.
//
// It is pointed at Ollama's Anthropic-compatible endpoint, and everything about
// that deployment — host, credentials, models — is hardcoded in [Defaults].
// Nothing here reads the environment. When configuration arrives it fills the
// same [Config] and reaches this package through [NewWith]; the backend itself
// does not change.
//
// The agent loop lives here, which is the difference between this and the
// Claude Code backend: there is no process to ask, so this package sends the
// request, runs whatever tools the model asked for through [core.Runtime], and
// sends the results back until the model stops asking.
//
// What it does not do yet: streaming, hooks, subagents, structured output,
// resuming. Those are refused at Open rather than quietly ignored — see
// [unsupported].
package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/TheLazyLemur/pi-claude/core"
)

// Backend opens conversations against an Anthropic-shaped API.
type Backend struct{ cfg Config }

// New returns a backend on the hardcoded configuration: Ollama on this machine.
func New() *Backend { return NewWith(Defaults()) }

// NewWith returns a backend on a configuration you supply. This is the seam a
// config layer plugs into.
func NewWith(cfg Config) *Backend { return &Backend{cfg: cfg} }

// Open starts a conversation. It refuses up front rather than at some later
// surprise: an option this backend cannot honour is an error here.
func (b *Backend) Open(_ context.Context, cfg core.Config, rt core.Runtime) (core.Conversation, error) {
	if err := unsupported(cfg.Options); err != nil {
		return nil, err
	}
	if err := b.cfg.validate(); err != nil {
		return nil, err
	}

	model := b.cfg.Models.Resolve(cfg.Model)
	if model == "" {
		return nil, fmt.Errorf("pi: anthropic: no model configured for %q", cfg.Model)
	}

	return &Conversation{
		cfg:       b.cfg,
		opts:      cfg.Options,
		rt:        rt,
		client:    &client{cfg: b.cfg},
		model:     model,
		sessionID: fmt.Sprintf("anthropic-%d", time.Now().UnixNano()),
	}, nil
}

// Conversation is one exchange. It owns the message history and the agent loop;
// everything about the host reaches it through core.Runtime.
type Conversation struct {
	cfg    Config
	opts   core.Options
	rt     core.Runtime
	client *client
	model  string

	// history is written and read only by Prompt, which core.Session
	// serialises, so it needs no lock of its own.
	history   []message
	sessionID string
	ready     bool

	mu     sync.Mutex
	closed bool
	cancel context.CancelFunc
}

// Prompt runs one turn: send, run the tools the model asks for, send again,
// until it stops asking.
func (c *Conversation) Prompt(ctx context.Context, text string, images ...core.Image) (core.Turn, error) {
	if len(images) > 0 {
		return core.Turn{}, errors.New("pi: anthropic: this backend cannot send images yet")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return core.Turn{}, core.ErrSessionClosed
	}
	c.cancel = cancel
	c.mu.Unlock()

	// The handshake waits for the first prompt, because nobody can be
	// subscribed while Open is still running.
	if !c.ready {
		c.ready = true
		c.rt.Emit(core.ReadyEvent{
			SessionID: c.sessionID,
			Model:     c.model,
			Tools:     toolNames(c.rt.Tools()),
			CWD:       c.opts.CWD,
		})
	}

	c.history = append(c.history, userText(text))
	return c.loop(ctx)
}

// Interrupt cancels the request in flight. The turn it belongs to returns the
// context's error.
func (c *Conversation) Interrupt() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// Close ends the conversation. There is no process to reap.
func (c *Conversation) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	return nil
}

// ID is this conversation's session id.
func (c *Conversation) ID() string { return c.sessionID }

// loop is the agent loop. Text and Duration are filled in by core.Session from
// the events seen, so they are not tracked here.
func (c *Conversation) loop(ctx context.Context) (core.Turn, error) {
	turn := core.Turn{Subtype: "success"}

	for {
		if c.opts.MaxTurns > 0 && turn.Turns >= c.opts.MaxTurns {
			turn.Subtype = "error_max_turns"
			turn.IsError = true
			return turn, nil
		}

		res, err := c.client.send(ctx, c.request())
		if err != nil {
			return core.Turn{}, err
		}

		turn.Turns++
		addUsage(&turn.Usage, res.Usage)
		turn.Result = res.text()

		c.report(res)
		if history, ok := res.assistantTurn(); ok {
			c.history = append(c.history, history)
		}

		calls := res.toolCalls()
		if len(calls) == 0 {
			return turn, nil
		}

		results, interrupted, err := c.runTools(ctx, calls, &turn)
		if err != nil {
			return core.Turn{}, err
		}
		c.history = append(c.history, message{Role: "user", Content: results})

		if interrupted {
			turn.Subtype = "error_interrupted"
			turn.IsError = true
			return turn, nil
		}
	}
}

// request is the next call on the wire.
func (c *Conversation) request() request {
	return request{
		Model:     c.model,
		MaxTokens: c.cfg.MaxTokens,
		System:    c.system(),
		Messages:  c.history,
		Tools:     declareTools(c.rt.Tools()),
	}
}

func (c *Conversation) system() string {
	parts := make([]string, 0, 2)
	for _, part := range []string{c.opts.SystemPrompt, c.opts.AppendSystemPrompt} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	return strings.Join(parts, "\n\n")
}

// report turns one response into events.
func (c *Conversation) report(res response) {
	for _, b := range res.Content {
		switch b.Type {
		case "text":
			if b.Text != "" {
				c.rt.Emit(core.TextEvent{Text: b.Text})
			}
		case "thinking":
			if b.Thinking != "" {
				c.rt.Emit(core.ThinkingEvent{Text: b.Thinking})
			}
		case "tool_use":
			c.rt.Emit(core.ToolCallEvent{
				ID:    b.ID,
				Name:  b.Name,
				Input: decodeInput(b.Input),
				Mine:  c.rt.Owns(b.Name),
			})
		}
	}
}

// runTools puts each call past the host's approver and then runs it, returning
// the tool_result blocks to send back. A denial is reported to the model rather
// than hidden from it, so the model can choose something else.
func (c *Conversation) runTools(ctx context.Context, calls []block, turn *core.Turn) (results []block, interrupted bool, err error) {
	for _, call := range calls {
		req := core.ToolRequest{
			Name:      call.Name,
			Qualified: call.Name,
			Input:     decodeInput(call.Input),
			ID:        call.ID,
			Mine:      c.rt.Owns(call.Name),
		}

		decision := c.rt.Approve(ctx, req)
		if decision.Behavior == "deny" {
			c.rt.Emit(core.DeniedEvent{Name: call.Name, Reason: decision.Reason})
			turn.Denials = append(turn.Denials, core.Denial{ToolName: call.Name, ToolUseID: call.ID})
			results = append(results, toolResult(call.ID, denialText(decision.Reason), true))
			interrupted = interrupted || decision.Interrupt
			continue
		}

		result := c.rt.CallTool(ctx, call.Name, call.Input)
		if len(result.Images) > 0 {
			return nil, false, fmt.Errorf("pi: anthropic: tool %s returned images, which this backend cannot send yet", call.Name)
		}
		c.rt.Emit(core.ToolResultEvent{ID: call.ID, Text: result.Text, IsError: result.IsError})
		results = append(results, toolResult(call.ID, result.Text, result.IsError))
	}
	return results, interrupted, nil
}

func denialText(reason string) string {
	if reason == "" {
		return "denied by the host"
	}
	return "denied: " + reason
}

// decodeInput makes the model's arguments readable to an event subscriber. A
// tool still receives the raw JSON, so nothing is lost to this.
func decodeInput(raw json.RawMessage) map[string]any {
	input := map[string]any{}
	if len(raw) > 0 {
		json.Unmarshal(raw, &input)
	}
	return input
}

func declareTools(tools []core.Tool) []tool {
	out := make([]tool, 0, len(tools))
	for _, t := range tools {
		out = append(out, tool{
			Name:        t.Name,
			Description: t.Description,
			InputSchema: t.Schema,
		})
	}
	return out
}

func toolNames(tools []core.Tool) []string {
	out := make([]string, 0, len(tools))
	for _, t := range tools {
		out = append(out, t.Name)
	}
	return out
}

func addUsage(total *core.Usage, seen core.Usage) {
	total.InputTokens += seen.InputTokens
	total.OutputTokens += seen.OutputTokens
	total.CacheCreationInputTokens += seen.CacheCreationInputTokens
	total.CacheReadInputTokens += seen.CacheReadInputTokens
}

// unsupported names the options this backend would otherwise accept and then
// silently drop. Failing here is the point: an option that changes what a
// session may reach must not be ignored quietly.
func unsupported(opts core.Options) error {
	checks := []struct {
		set  bool
		name string
	}{
		{len(opts.Hooks) > 0, "Hooks"},
		{len(opts.Agents) > 0, "Agents"},
		{opts.OutputSchema != nil, "OutputSchema"},
		{len(opts.MCPConfig) > 0, "MCPConfig"},
		{opts.Resume != "", "Resume"},
		{opts.Continue, "Continue"},
		{opts.ForkSession, "ForkSession"},
		{opts.ResumeSessionAt != "", "ResumeSessionAt"},
		{opts.EnableFileCheckpointing, "EnableFileCheckpointing"},
		{opts.MaxBudgetUSD > 0, "MaxBudgetUSD"},
		{opts.IncludePartialMessages, "IncludePartialMessages"},
		{len(opts.Tools) > 0, "Tools"},
		{opts.FallbackModel != "", "FallbackModel"},
		{opts.Effort != "", "Effort"},
		{len(opts.Betas) > 0, "Betas"},
		{opts.PermissionMode != "" && opts.PermissionMode != core.PermissionModeDefault, "PermissionMode"},
	}

	var rejected []string
	for _, check := range checks {
		if check.set {
			rejected = append(rejected, check.name)
		}
	}
	if len(rejected) == 0 {
		return nil
	}
	return fmt.Errorf("pi: anthropic backend cannot honour these options: %s", strings.Join(rejected, ", "))
}
