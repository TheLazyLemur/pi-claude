// Package scripted is a backend that answers from a function you supply,
// without a model or a subprocess.
//
// It exists for two reasons. It lets a host exercise its own tools, policy and
// event handling in a test at full speed and no cost. And it is the proof that
// core.Backend is a real seam rather than a shape drawn around one CLI: nothing
// here spawns a process, speaks a protocol, or knows what MCP is.
//
// A backend that drove a model API directly would look like this too. The loop
// lives in the backend; the host's tools reach it through core.Runtime.
package scripted

import (
	"context"
	"encoding/json"

	"github.com/TheLazyLemur/pi-claude/core"
)

// Reply decides what happens for one prompt. Call rt.CallTool to run the
// host's tools and rt.Emit to report progress, exactly as a real backend would.
type Reply func(ctx context.Context, prompt string, rt core.Runtime) (core.Turn, error)

// Backend answers every prompt with Reply.
type Backend struct{ Reply Reply }

// New returns a backend driven by fn.
func New(fn Reply) *Backend { return &Backend{Reply: fn} }

// Open starts a conversation. There is nothing to spawn.
func (b *Backend) Open(_ context.Context, cfg core.Config, rt core.Runtime) (core.Conversation, error) {
	return &conversation{reply: b.Reply, rt: rt, cwd: cfg.CWD}, nil
}

type conversation struct {
	reply Reply
	rt    core.Runtime
	cwd   string
	ready bool
}

func (c *conversation) Prompt(ctx context.Context, text string) (core.Turn, error) {
	// The handshake waits for the first prompt, because nobody can be
	// subscribed while Open is still running.
	if !c.ready {
		c.ready = true
		names := make([]string, 0, len(c.rt.Tools()))
		for _, tool := range c.rt.Tools() {
			names = append(names, tool.Name)
		}
		c.rt.Emit(core.ReadyEvent{Model: "scripted", Tools: names, CWD: c.cwd})
	}

	if c.reply == nil {
		return core.Turn{Subtype: "success"}, nil
	}
	return c.reply(ctx, text, c.rt)
}

func (c *conversation) Interrupt() error { return nil }
func (c *conversation) Close() error     { return nil }

// Say is the common case: answer with some text and nothing else.
func Say(text string) Reply {
	return func(_ context.Context, _ string, rt core.Runtime) (core.Turn, error) {
		rt.Emit(core.TextEvent{Text: text})
		return core.Turn{Subtype: "success", Result: text, Turns: 1}, nil
	}
}

// Call runs one of the host's tools, reports it, and answers with its output.
// It goes through Approve first, so a host's permission policy is exercised too.
func Call(name string, args any) Reply {
	return func(ctx context.Context, _ string, rt core.Runtime) (core.Turn, error) {
		encoded, err := json.Marshal(args)
		if err != nil {
			return core.Turn{}, err
		}

		req := core.ToolRequest{Name: name, Qualified: name, ID: "scripted-1", Mine: rt.Owns(name)}
		if json.Unmarshal(encoded, &req.Input) != nil {
			req.Input = map[string]any{}
		}

		if decision := rt.Approve(ctx, req); decision.Behavior == "deny" {
			rt.Emit(core.DeniedEvent{Name: name, Reason: decision.Reason})
			return core.Turn{Subtype: "success", IsError: true, Turns: 1,
				Denials: []core.Denial{{ToolName: name, ToolUseID: req.ID}}}, nil
		}

		rt.Emit(core.ToolCallEvent{ID: req.ID, Name: name, Input: req.Input, Mine: req.Mine})
		result := rt.CallTool(ctx, name, encoded)
		rt.Emit(core.ToolResultEvent{ID: req.ID, Text: result.Text, IsError: result.IsError})
		rt.Emit(core.TextEvent{Text: result.Text})

		return core.Turn{Subtype: "success", Result: result.Text, IsError: result.IsError, Turns: 1}, nil
	}
}
