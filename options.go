package pi

import "context"

// toolServerName is the SDK MCP server your tools are registered under. It is
// an implementation detail: tools are reported to you by their bare names.
const toolServerName = "pi"

// NoTools drops tools wholesale, for sessions that should reach only the tools
// you supply.
type NoTools string

const (
	// NoToolsNone leaves the CLI's tool defaults alone.
	NoToolsNone NoTools = ""

	// NoToolsBuiltin removes every built-in tool but leaves MCP servers
	// configured on this machine in place.
	NoToolsBuiltin NoTools = "builtin"

	// NoToolsAll removes every built-in tool and every MCP server configured on
	// this machine, so the only tools offered are the ones you register.
	//
	// Prefer this when embedding an agent in your own product: a developer
	// machine commonly has dozens of MCP tools configured, and without it they
	// are all in reach.
	NoToolsAll NoTools = "all"
)

// PermissionMode controls how the CLI treats tool calls it is not told about.
type PermissionMode string

const (
	PermissionModeDefault     PermissionMode = "default"
	PermissionModeAcceptEdits PermissionMode = "acceptEdits"
	PermissionModePlan        PermissionMode = "plan"
	PermissionModeDontAsk     PermissionMode = "dontAsk"
)

// ToolRequest is a tool the model wants to run.
type ToolRequest struct {
	// Name is the bare tool name: "greet" for one of yours, "Read" for a
	// built-in.
	Name string

	// Qualified is the name the CLI used, e.g. "mcp__pi__greet".
	Qualified string

	// Input is the arguments the model chose.
	Input map[string]any

	// ID identifies this call.
	ID string

	// Mine reports whether this is one of the tools you registered.
	Mine bool
}

// Decision is the answer to a ToolRequest.
type Decision struct {
	Behavior  string
	Reason    string
	Interrupt bool
}

// Allow permits the tool call.
func Allow() Decision { return Decision{Behavior: "allow"} }

// Deny refuses the tool call and tells the model why.
func Deny(reason string) Decision { return Decision{Behavior: "deny", Reason: reason} }

// ApproveFunc decides whether a tool may run. Nil allows everything.
type ApproveFunc func(ctx context.Context, req ToolRequest) Decision

// Options configures a session. The zero value is usable: it starts the CLI
// with its normal defaults.
type Options struct {
	// CWD is the working directory for the session.
	CWD string

	// Model to use, e.g. "sonnet" or a full model id.
	Model string

	// MaxTurns bounds the agent loop.
	MaxTurns int

	// MaxBudgetUSD stops the session once it has spent this much.
	MaxBudgetUSD float64

	// SystemPrompt replaces the CLI's system prompt entirely.
	SystemPrompt string

	// AppendSystemPrompt adds to the CLI's system prompt.
	AppendSystemPrompt string

	// Tools is an allowlist of built-in tools, e.g. []string{"Read", "Grep"}.
	// Empty leaves the CLI's default set alone. Ignored when NoTools is set.
	Tools []string

	// NoTools drops built-in tools, and optionally every MCP server configured
	// on this machine.
	NoTools NoTools

	// CustomTools are your own tools, registered over the control protocol.
	// No MCP server process is involved.
	CustomTools []Tool

	// ApproveTool is consulted before each tool call. Nil allows everything.
	ApproveTool ApproveFunc

	// PermissionMode controls the CLI's own permission behaviour.
	PermissionMode PermissionMode

	// Resume continues a previous session by id.
	Resume string

	// Continue resumes the most recent session in CWD.
	Continue bool

	// Executable is the claude binary to run. Defaults to "claude" on PATH.
	Executable string

	// Env adds environment variables to the subprocess.
	Env []string

	// Stderr receives the CLI's stderr, line by line.
	Stderr func(string)
}
