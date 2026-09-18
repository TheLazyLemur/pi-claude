// Package core holds the domain: tools, events, turns, policy, and the port a
// backend plugs into. It knows nothing about any particular agent runtime.
//
// The seam is [Backend]. Claude Code is one implementation, in
// external/claudecode. A different CLI, or an API this library drives itself,
// is another implementation of the same two interfaces. Everything a backend
// needs from the host it gets through [Runtime], so a backend never reaches
// into the host's tools or policy directly.
package core

import (
	"context"
	"encoding/json"
)

// Backend opens conversations with an agent runtime.
type Backend interface {
	// Open starts a conversation. The Runtime is the host: a backend calls
	// into it for tools, permission, hooks, and to publish events.
	//
	// Do not emit during Open. The host has had no chance to subscribe yet, so
	// anything published here is lost. Wait for the first Prompt.
	Open(ctx context.Context, cfg Config, rt Runtime) (Conversation, error)
}

// Conversation is one live exchange with a backend.
type Conversation interface {
	// Prompt runs one turn to completion. Text and Duration are filled in by
	// the caller from the events seen, so a backend need not track them.
	//
	// A backend that cannot send images must refuse them with an error rather
	// than drop them.
	Prompt(ctx context.Context, text string, images ...Image) (Turn, error)

	// Interrupt stops the turn in flight.
	Interrupt() error

	// Close ends the conversation and releases whatever it holds.
	Close() error
}

// Runtime is what a backend calls back into. It is deliberately domain-shaped:
// no JSON-RPC, no wire formats, nothing a second backend would have to pretend
// to understand.
type Runtime interface {
	// Tools the model may call.
	Tools() []Tool

	// Owns reports whether a name is one of the host's tools, as opposed to
	// one the backend provides itself.
	Owns(name string) bool

	// CallTool runs one by name. A tool that fails returns a result carrying
	// the failure rather than an error, because the model can act on it.
	CallTool(ctx context.Context, name string, args json.RawMessage) ToolResult

	// Approve decides whether a tool call may proceed.
	Approve(ctx context.Context, req ToolRequest) Decision

	// Hooks the host registered, so a backend able to run them can declare
	// them at startup.
	Hooks() map[HookEvent][]HookMatcher

	// RunHook invokes one by the id the backend was given.
	RunHook(ctx context.Context, callbackID string, in HookInput) (HookOutput, error)

	// Emit publishes an event to the host's subscribers.
	Emit(ev Event)
}

// Config is the policy a backend is opened with.
type Config struct {
	Options

	// ToolServer is the name the host's tools are grouped under, for backends
	// that need one.
	ToolServer string
}
