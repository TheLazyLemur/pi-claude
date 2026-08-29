# pi-claude

Drive the Claude Code CLI from Go.

You get one `claude` subprocess per session, a typed API, and your own Go
functions as tools the model can call. No MCP server, no port, no JSON-RPC in
your code.

Use it when you want an agent inside your own program and you want to decide
what it can reach. If you just want a chat completion, use the Anthropic SDK
instead. This is for when you want the CLI's agent loop but not its tools.

The API shape is borrowed from the [pi agent harness](https://pi.dev): one
options struct, one factory, tools you declare instead of wire up, and a
`Subscribe` that hands back its own unsubscribe.

## Install

```bash
go get github.com/TheLazyLemur/pi-claude
```

Needs Go 1.25 or later, and the `claude` CLI on your `PATH`.

The module is `pi-claude` but the package is `pi`, so import it like this:

```go
import pi "github.com/TheLazyLemur/pi-claude"
```

## First program

This compiles and runs as it stands.

```go
package main

import (
	"context"
	"fmt"
	"log"

	pi "github.com/TheLazyLemur/pi-claude"
)

func main() {
	turn, err := pi.Run(context.Background(), "In one sentence, what is a goroutine?", pi.Options{
		NoTools:  pi.NoToolsAll, // give the model nothing but words
		MaxTurns: 1,
	})
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println(turn.Text)
	fmt.Printf("%d in, %d out, $%.4f\n",
		turn.Usage.InputTokens, turn.Usage.OutputTokens, turn.CostUSD)
}
```

`pi.Run` is the one-shot form. It starts a session, sends one prompt, waits for
the answer, and shuts down.

## Sessions and turns

`pi.New` keeps the subprocess alive so context and cost carry across prompts.
Each `Prompt` blocks until that turn is done and gives you a `Turn`.

```go
sess, err := pi.New(ctx, pi.Options{Tools: []string{"Read", "Grep"}})
if err != nil {
	log.Fatal(err)
}
defer sess.Close()

first, err := sess.Prompt(ctx, "What Go files are in this directory?")
if err != nil {
	log.Fatal(err)
}

// Still knows what the first turn found.
second, err := sess.Prompt(ctx, "Which of those is the largest?")
```

A `Turn` holds:

| field | what it is |
|---|---|
| `Text` | every assistant text block, joined |
| `Result` | the CLI's own summary of the turn |
| `IsError` | whether the turn failed |
| `Subtype` | `success`, `error_max_turns`, `error_max_budget_usd`, and friends |
| `Errors` | detail when `IsError` is set |
| `Turns` | model round trips |
| `CostUSD` | what it cost |
| `Usage` | token counts, cache included |
| `Denials` | tool calls that were refused |
| `Duration` | wall clock |
| `StructuredOutput` | raw JSON, when you asked for a shape |

One session runs one prompt at a time. Call `Prompt` again while a turn is in
flight and you get `pi.ErrPromptInFlight`, not a queue. If you want ten agents,
start ten sessions.

`Resume` and `Continue` pick up an earlier session. `Interrupt`, `SetModel`,
`SetPermissionMode` and `SetMaxThinkingTokens` work mid-session. Cancel the
context you gave `Prompt` and the CLI is interrupted too, so an abandoned turn
stops costing you money.

## Your own tools

Parameters are a Go struct. The schema comes from the struct, so you write the
shape once:

```go
type fillParams struct {
	Code string `json:"code" desc:"Replacement text for the region"`
	Line int    `json:"line,omitempty" desc:"Where it goes"`
}

fill := pi.DefineTool("submit_fill", "Submit the code that replaces the region",
	func(ctx context.Context, p fillParams) (pi.ToolResult, error) {
		return pi.Text("applied %d bytes", len(p.Code)), nil
	})

sess, err := pi.New(ctx, pi.Options{CustomTools: []pi.Tool{fill}})
```

`desc` is what the model reads, so write it for the model and not for yourself.
A field is required unless it is a pointer or has `omitempty`. `json:"-"` is
skipped. For a tool that takes nothing, use `pi.NoParams`.

Return `pi.Errorf(...)` when the model can do something about the failure, and a
Go `error` when it cannot. A refusal it can read is worth more than a stack
trace it cannot.

No MCP server runs anywhere. The tool is registered during the handshake and the
CLI calls back down the same pipe.

## Turning tools off

Worth more attention than it usually gets. Your laptop probably has more tools
wired into Claude Code than you remember. On the machine this was built on, a
default session offered 134: thirty built-ins, and another hundred and four from
Gmail, Calendar, Drive and friends. Embed an agent in your product and that is
what it can reach.

```go
pi.Options{NoTools: pi.NoToolsAll}          // built-ins and machine MCP servers, both gone
pi.Options{NoTools: pi.NoToolsBuiltin}      // built-ins gone, machine MCP servers kept
pi.Options{Tools: []string{"Read", "Grep"}} // only these built-ins
```

Then check it, rather than hoping:

```go
sess.Subscribe(func(ev pi.Event) {
	if e, ok := ev.(pi.ReadyEvent); ok {
		log.Printf("model can see %d tools: %v", len(e.Tools), e.Tools)
	}
})
```

## Permissions

`ApproveTool` runs before each tool call. Leave it nil and everything is
allowed.

```go
sess, err := pi.New(ctx, pi.Options{
	Tools:          []string{"Read"},
	PermissionMode: pi.PermissionModeDefault,
	ApproveTool: func(_ context.Context, req pi.ToolRequest) pi.Decision {
		path, _ := req.Input["file_path"].(string)
		if !strings.HasPrefix(path, root) {
			return pi.Deny("reads are confined to " + root)
		}
		return pi.Allow()
	},
})
```

Watch out for this one. If the machine's Claude Code config auto-approves, the
CLI never asks and your callback never runs. It fails open and says nothing
about it. Set `PermissionMode: pi.PermissionModeDefault` explicitly to take it
back. The zero value passes no flag on purpose and lets the machine config win,
so set it whenever the callback actually matters.

A `ToolRequest` tells you more than the name. `req.Mine` says it is one of
yours. `req.Name` is the name you registered, `greet` rather than
`mcp__pi__greet`. `req.Qualified` has the wire name. `req.BlockedPath` and
`req.DecisionReason` say why the CLI is asking, and `req.AgentID` says which
subagent asked.

## Hooks

Hooks fire at points in the CLI's own lifecycle. They can rewrite a tool's
arguments, add context, or stop the call.

```go
sess, err := pi.New(ctx, pi.Options{
	Hooks: map[pi.HookEvent][]pi.HookMatcher{
		pi.HookPreToolUse: {{
			Matcher: "Bash",
			Hooks: []pi.HookFunc{
				func(_ context.Context, in pi.HookInput) (pi.HookOutput, error) {
					if strings.Contains(fmt.Sprint(in.ToolInput["command"]), "rm -rf") {
						return pi.HookDeny("no recursive deletes"), nil
					}
					return pi.HookContinue(), nil
				},
			},
		}},
	},
})
```

The deny reason lands on the model as the tool result. Write it as an
instruction. "Use the existing helper instead" teaches it something. "Denied"
teaches it nothing.

Events: `HookPreToolUse`, `HookPostToolUse`, `HookPostToolUseFailure`,
`HookPermissionRequest`, `HookUserPromptSubmit`, `HookSessionStart`,
`HookSessionEnd`, `HookStop`, `HookSubagentStart`, `HookSubagentStop`,
`HookPreCompact`, `HookNotification`.

Hooks and `ApproveTool` overlap. Use `ApproveTool` for yes or no on a tool call.
Use a hook when you want to change the arguments, add context, or catch
something that is not a tool call at all.

## Structured output

Pin the answer to a shape and get it back typed. Useful when a program reads the
result instead of a person.

```go
type verdict struct {
	Answer     string `json:"answer" desc:"The capital city"`
	Confidence int    `json:"confidence" desc:"0 to 100"`
}

turn, err := pi.Run(ctx, "What is the capital of France?", pi.Options{
	OutputSchema: pi.SchemaFor[verdict](),
})
if err != nil {
	log.Fatal(err)
}

var got verdict
if err := json.Unmarshal(turn.StructuredOutput, &got); err != nil {
	log.Fatal(err)
}
```

If it cannot produce the shape, the turn comes back with `Subtype ==
"error_max_structured_output_retries"`.

## Streaming

```go
pi.Options{IncludePartialMessages: true}
```

turns on `DeltaEvent`, one per token as it arrives. `Thinking` tells you whether
it is reasoning or answer.

## Subagents

```go
pi.Options{Agents: map[string]pi.Agent{
	"reviewer": {Description: "Reviews code for bugs", Prompt: "You are a code reviewer."},
}}
```

## Events

`Subscribe` returns the function that removes your listener. Handle the events
you care about and ignore the rest.

```go
stop := sess.Subscribe(func(ev pi.Event) {
	switch e := ev.(type) {
	case pi.ReadyEvent:      // handshake: session id, model, tools offered
	case pi.TextEvent:       // assistant text
	case pi.ThinkingEvent:   // assistant reasoning
	case pi.ToolCallEvent:   // model wants to run a tool
	case pi.ToolResultEvent: // what the tool returned
	case pi.DeniedEvent:     // your approver refused one
	case pi.DeltaEvent:      // streamed token, needs IncludePartialMessages
	case pi.StatusEvent:     // e.g. "compacting"
	case pi.CompactEvent:    // conversation was compacted
	case pi.RateLimitEvent:  // window usage and reset time
	case pi.AuthEvent:       // authentication progress
	case pi.TurnEvent:       // turn finished, with accounting
	case pi.ErrorEvent:      // protocol or transport failure
	}
})
defer stop()
```

Listeners run on the read loop, so keep them quick. Do the slow thing on your
own goroutine.

## A harness where every tool is yours

`examples/harness` is a working coding agent. No built-in tools, no MCP servers
from the machine, no shell. Every tool is a Go function closing over state you
own.

```
harness                       # interactive, in the current directory
harness "fix the flaky test"  # one shot
echo "..." | harness          # piped, one prompt per line
harness -C ./other "task"     # somewhere else
```

Five tools: `list_files`, `read_file`, `search`, `write_file`, `edit_file`. They
all go through a `workspace` that resolves paths and refuses anything outside
itself. The sandbox is Go code, not a line in a prompt. Ask it for
`../../.ssh/id_rsa` and you get an error the model can read, not a file.

The workspace keeps its own record of what changed. That way what happened is
something you know, not something you reconstruct from a transcript.

Give it no task and it reads prompts from stdin on one session, so the second
turn still knows what the first one found.

```
$ cd myproject && harness
/Users/dan/myproject
14 files, 5 tools, no built-ins. Ctrl-D to exit.

> Total ignores Quantity. Fix it and add a table-driven test.
  · list_files
  · read_file cart.go
  · edit_file cart.go
  · write_file cart_test.go

cart.go line 14: total += item.Price -> total += item.Price * float64(item.Quantity)

[7 turns · $0.1287 · 12 in / 1996 out]

2 files changed
  edit   cart.go (+25 bytes)
  write  cart_test.go (+1482 bytes)
```

Ctrl-C stops the turn and gives you the prompt back. Ctrl-D leaves.

Two things fall out of owning the tools. `edit_file` refuses an ambiguous match
instead of picking an occurrence and hoping. And the agent cannot claim it ran
your tests, because nothing it has could run them.

## API

Starting and stopping:

| | |
|---|---|
| `pi.Run(ctx, prompt, opts) (Turn, error)` | one-shot |
| `pi.New(ctx, opts) (*Session, error)` | long-lived session |
| `sess.Close() error` | stop and reap the subprocess; safe to call twice |
| `sess.Wait() error` | block until the subprocess exits |

Talking to it:

| | |
|---|---|
| `sess.Prompt(ctx, text) (Turn, error)` | send, block until the turn ends |
| `sess.Subscribe(fn) (stop func())` | watch events |
| `sess.Interrupt() error` | stop the current turn |

Tools:

| | |
|---|---|
| `pi.DefineTool(name, desc, fn) Tool` | tool from a typed param struct |
| `pi.Text(...) / pi.Errorf(...)` | tool results |
| `pi.Allow() / pi.Deny(reason)` | permission decisions |
| `pi.HookContinue() / pi.HookDeny(reason)` | hook decisions |
| `pi.SchemaFor[T]()` | JSON Schema from a Go type |

Asking it things:

| | |
|---|---|
| `sess.ID() string` | CLI session id |
| `sess.OfferedTools() []string` | what the CLI actually gave the model |
| `sess.MCPStatus(ctx)` | external MCP server state |

Changing it mid-session:

| | |
|---|---|
| `sess.SetModel(model) error` | switch model |
| `sess.SetPermissionMode(mode) error` | switch permission behaviour |
| `sess.SetMaxThinkingTokens(n) error` | cap extended thinking; 0 clears |
| `sess.SetMCPServers(ctx, servers)` | replace external MCP servers |
| `sess.RewindFiles(ctx, id, dryRun)` | undo file edits, but read the caveat |

## What it does not do

No connection pool. One session is one subprocess and one prompt at a time. If
you want a fleet, that is your code, and it is not much code.

No retries. `RateLimitEvent` tells you where you stand, but nothing acts on it.

No transcript store. `Resume` takes a session id, and remembering which id goes
with which piece of work is your job.

## Examples

- `examples/harness`: a working coding agent, five custom tools, no built-ins, stdin REPL
- `examples/minimal`: one prompt, one answer
- `examples/tools-only`: every built-in off, one custom tool, checked end to end
- `examples/approve`: keeping file reads inside a directory
- `examples/streaming`: events across several turns
- `examples/verify`: hooks, streaming, structured output and subagents, run live

`examples/tools-only` is the same demo written by hand against the raw protocol
in 332 lines. Here it is 85, and 40 of those just print the verdict.

## Debugging

`PI_CLAUDE_DEBUG=1` logs every frame in both directions.

## Status

Tested against Claude Code 2.1.251. The protocol is not versioned and not
officially published, so assume a CLI upgrade can break it.

Run against the live CLI, not just unit tests: custom tools, turning tools off,
permissions, hooks including deny reasons reaching the model, streaming deltas,
structured output, subagents, MCP status. `go run ./examples/verify` re-checks
all of that and exits non-zero if something regressed. Run it after a CLI
upgrade.

One thing does not work. `RewindFiles` sends and receives correctly, but Claude
Code 2.1.251 replies `{"canRewind": false, "error": "File rewinding is not
enabled."}` even with `EnableFileCheckpointing` set, and there is no flag to
turn it on. Read `canRewind` in the reply rather than assuming.

Built on protocol notes from
[claude-code-stdio-protocol](https://github.com/TheLazyLemur/claude-agent-sdk-go),
which writes up the wire format in full.

## Licence

MIT
