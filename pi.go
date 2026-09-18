// Package pi drives an agent runtime from Go, with your own functions as tools.
//
// This package is the front door and the wiring. The shapes live in
// [core] and the Claude Code protocol lives in external/claudecode. That split
// is the point: [core.Backend] is a two-method port, so another CLI or an API
// this library drives itself can be added without the domain knowing.
//
// # Getting started
//
//	turn, err := pi.Run(ctx, "What is a goroutine?", pi.Options{NoTools: pi.NoToolsAll})
//	fmt.Println(turn.Text)
//
// # Tools
//
// Parameters are a Go struct, and the JSON Schema is derived from it:
//
//	type fillParams struct {
//	    Code string `json:"code" desc:"Replacement text for the region"`
//	}
//
//	tool := pi.DefineTool("submit_fill", "Submit the replacement code",
//	    func(ctx context.Context, p fillParams) (pi.ToolResult, error) {
//	        return pi.Text("applied %d bytes", len(p.Code)), nil
//	    })
package pi

import (
	"context"
	"errors"

	"github.com/TheLazyLemur/pi-claude/core"
	"github.com/TheLazyLemur/pi-claude/external/claudecode"
)

// toolServer is the name custom tools are grouped under. It is an
// implementation detail: tools are reported to you by their bare names.
const toolServer = "pi"

// The domain, re-exported so callers need one import.
type (
	Options     = core.Options
	Tool        = core.Tool
	ToolResult  = core.ToolResult
	Image       = core.Image
	ToolRequest = core.ToolRequest
	Decision    = core.Decision
	ApproveFunc = core.ApproveFunc
	NoParams    = core.NoParams
	NoTools     = core.NoTools

	PermissionMode = core.PermissionMode
	Agent          = core.Agent

	HookEvent   = core.HookEvent
	HookFunc    = core.HookFunc
	HookInput   = core.HookInput
	HookOutput  = core.HookOutput
	HookMatcher = core.HookMatcher

	Event             = core.Event
	ReadyEvent        = core.ReadyEvent
	TextEvent         = core.TextEvent
	ThinkingEvent     = core.ThinkingEvent
	ToolCallEvent     = core.ToolCallEvent
	ToolResultEvent   = core.ToolResultEvent
	ToolProgressEvent = core.ToolProgressEvent
	DeniedEvent       = core.DeniedEvent
	DeltaEvent        = core.DeltaEvent
	UsageEvent        = core.UsageEvent
	StatusEvent       = core.StatusEvent
	CompactEvent      = core.CompactEvent
	AuthEvent         = core.AuthEvent
	RateLimitEvent    = core.RateLimitEvent
	TurnEvent         = core.TurnEvent
	ErrorEvent        = core.ErrorEvent

	Turn   = core.Turn
	Usage  = core.Usage
	Denial = core.Denial

	// Backend is the seam. Implement it to drive something other than Claude
	// Code, and pass it to Open.
	Backend      = core.Backend
	Conversation = core.Conversation
	Runtime      = core.Runtime
	Config       = core.Config
)

const (
	NoToolsNone    = core.NoToolsNone
	NoToolsBuiltin = core.NoToolsBuiltin
	NoToolsAll     = core.NoToolsAll

	PermissionModeDefault     = core.PermissionModeDefault
	PermissionModeAcceptEdits = core.PermissionModeAcceptEdits
	PermissionModePlan        = core.PermissionModePlan
	PermissionModeDontAsk     = core.PermissionModeDontAsk

	HookPreToolUse         = core.HookPreToolUse
	HookPostToolUse        = core.HookPostToolUse
	HookPostToolUseFailure = core.HookPostToolUseFailure
	HookPermissionRequest  = core.HookPermissionRequest
	HookUserPromptSubmit   = core.HookUserPromptSubmit
	HookSessionStart       = core.HookSessionStart
	HookSessionEnd         = core.HookSessionEnd
	HookStop               = core.HookStop
	HookSubagentStart      = core.HookSubagentStart
	HookSubagentStop       = core.HookSubagentStop
	HookPreCompact         = core.HookPreCompact
	HookNotification       = core.HookNotification
)

var (
	// ErrUnsupported is returned by a Claude Code specific method on a session
	// opened against a different backend.
	ErrUnsupported = errors.New("pi: this backend does not support that")

	// ErrSessionClosed is returned once a session has been closed.
	ErrSessionClosed = core.ErrSessionClosed

	// ErrPromptInFlight is returned when a turn is already running.
	ErrPromptInFlight = core.ErrPromptInFlight
)

// Text builds a successful tool result.
func Text(format string, args ...any) ToolResult { return core.Text(format, args...) }

// Errorf builds a failed tool result the model can read and react to.
func Errorf(format string, args ...any) ToolResult { return core.Errorf(format, args...) }

// Allow permits a tool call.
func Allow() Decision { return core.Allow() }

// Deny refuses a tool call and tells the model why.
func Deny(reason string) Decision { return core.Deny(reason) }

// HookContinue lets the session carry on unchanged.
func HookContinue() HookOutput { return core.HookContinue() }

// HookDeny blocks the tool call a hook fired for.
func HookDeny(reason string) HookOutput { return core.HookDeny(reason) }

// DefineTool builds a Tool from a typed parameter struct.
func DefineTool[P any](name, description string, exec func(ctx context.Context, params P) (ToolResult, error)) Tool {
	return core.DefineTool(name, description, exec)
}

// SchemaFor derives a JSON Schema from a Go type, for Options.OutputSchema.
func SchemaFor[T any]() map[string]any { return core.SchemaFor[T]() }

// New starts a Claude Code session.
func New(ctx context.Context, opts Options) (*Session, error) {
	return Open(ctx, claudecode.New(), opts)
}

// Open starts a session on any backend. Use it to drive something other than
// Claude Code.
func Open(ctx context.Context, backend Backend, opts Options) (*Session, error) {
	inner, err := core.Open(ctx, backend, opts, toolServer)
	if err != nil {
		return nil, err
	}
	return &Session{Session: inner}, nil
}

// Run is the one-shot form: start a session, send one prompt, return its turn.
func Run(ctx context.Context, prompt string, opts Options) (Turn, error) {
	sess, err := New(ctx, opts)
	if err != nil {
		return Turn{}, err
	}
	defer sess.Close()
	return sess.Prompt(ctx, prompt)
}

// Session is a running conversation. Prompt, Subscribe, Interrupt and Close
// work on any backend; the methods below only do anything on Claude Code, and
// report ErrUnsupported elsewhere.
type Session struct {
	*core.Session
}

// cli returns the Claude Code conversation, or nil on another backend.
func (s *Session) cli() *claudecode.Conversation {
	c, _ := s.Backend().(*claudecode.Conversation)
	return c
}

// ID returns the CLI's session id, empty until the first handshake.
func (s *Session) ID() string {
	if c := s.cli(); c != nil {
		return c.ID()
	}
	return ""
}

// OfferedTools returns the tools the CLI reported at startup.
func (s *Session) OfferedTools() []string {
	if c := s.cli(); c != nil {
		return c.OfferedTools()
	}
	return nil
}

// Wait blocks until the subprocess exits.
func (s *Session) Wait() error {
	if c := s.cli(); c != nil {
		return c.Wait()
	}
	return nil
}

// SetModel switches model mid-session.
func (s *Session) SetModel(model string) error {
	return s.onCLI(func(c *claudecode.Conversation) error { return c.SetModel(model) })
}

// SetPermissionMode changes permission behaviour mid-session.
func (s *Session) SetPermissionMode(mode PermissionMode) error {
	return s.onCLI(func(c *claudecode.Conversation) error { return c.SetPermissionMode(mode) })
}

// SetMaxThinkingTokens caps extended thinking. Zero or less clears the cap.
func (s *Session) SetMaxThinkingTokens(tokens int) error {
	return s.onCLI(func(c *claudecode.Conversation) error { return c.SetMaxThinkingTokens(tokens) })
}

// MCPStatus reports the connection state of external MCP servers.
func (s *Session) MCPStatus(ctx context.Context) (map[string]any, error) {
	c := s.cli()
	if c == nil {
		return nil, ErrUnsupported
	}
	return c.MCPStatus(ctx)
}

// SetMCPServers replaces the external MCP servers for this session.
func (s *Session) SetMCPServers(ctx context.Context, servers map[string]any) (map[string]any, error) {
	c := s.cli()
	if c == nil {
		return nil, ErrUnsupported
	}
	return c.SetMCPServers(ctx, servers)
}

// RewindFiles undoes file edits back to a message id. Read canRewind in the
// reply rather than assuming it worked.
func (s *Session) RewindFiles(ctx context.Context, userMessageID string, dryRun bool) (map[string]any, error) {
	c := s.cli()
	if c == nil {
		return nil, ErrUnsupported
	}
	return c.RewindFiles(ctx, userMessageID, dryRun)
}

func (s *Session) onCLI(fn func(*claudecode.Conversation) error) error {
	c := s.cli()
	if c == nil {
		return ErrUnsupported
	}
	return fn(c)
}
