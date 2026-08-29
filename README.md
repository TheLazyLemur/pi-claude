# pi-claude

Drive the Claude Code CLI from Go. One subprocess per session, a typed API, and
your own Go functions as tools — no MCP server, no port, no JSON-RPC in your code.

The API shape is borrowed from the [pi agent harness](https://pi.dev): one options
struct, one factory, declarative tools, and a `Subscribe` that returns its own
unsubscribe.

```bash
go get github.com/TheLazyLemur/pi-claude
```

Requires the `claude` CLI on `PATH`.

## Quick start

```go
turn, err := pi.Run(ctx, "In one sentence, what is a goroutine?", pi.Options{
    NoTools:  pi.NoToolsAll,
    MaxTurns: 1,
})
fmt.Println(turn.Text)
fmt.Printf("%d tokens in, %d out, $%.4f\n",
    turn.Usage.InputTokens, turn.Usage.OutputTokens, turn.CostUSD)
```

## Your own tools

Parameters are a Go struct. The JSON Schema is derived from it, so you declare
the shape once:

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

`desc` becomes the field description the model reads. Fields are required unless
they are pointers or carry `omitempty`. `json:"-"` is skipped. Use `pi.NoParams`
for a tool that takes no arguments.

There is no MCP server here. The tool is registered over the control protocol at
handshake time, and the CLI calls back down the same pipe.

## Locking the tool surface down

This matters more than it looks. A developer machine commonly has a lot of MCP
tools configured globally — on the machine this was built on, a default session
offered **134 tools**: 30 built-ins plus 104 from Gmail, Calendar, Drive, M365
and others. If you are embedding an agent in your own product, that is the blast
radius you inherit.

```go
pi.Options{NoTools: pi.NoToolsAll}     // built-ins AND machine MCP servers gone
pi.Options{NoTools: pi.NoToolsBuiltin} // built-ins gone, machine MCP servers kept
pi.Options{Tools: []string{"Read", "Grep"}} // just these built-ins
```

Assert on it rather than trusting it:

```go
sess.Subscribe(func(ev pi.Event) {
    if e, ok := ev.(pi.ReadyEvent); ok {
        log.Printf("model can see %d tools: %v", len(e.Tools), e.Tools)
    }
})
```

## Permissions

`ApproveTool` is consulted before each tool call. Nil allows everything.

```go
sess, _ := pi.New(ctx, pi.Options{
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

**The gotcha:** if the machine's own Claude Code config sets an auto-approving
permission mode, the CLI never asks and your callback never runs. Passing
`PermissionMode: pi.PermissionModeDefault` explicitly overrides that. The zero
value deliberately passes no flag, leaving the machine's configuration in charge —
so set it explicitly whenever the approval callback is load-bearing.

Your tools are reported by the bare name you registered (`greet`, not
`mcp__pi__greet`); `req.Mine` distinguishes them from built-ins, and
`req.Qualified` has the wire name if you need it.

## Hooks

Hooks run at points in the CLI's own lifecycle, and can rewrite a tool's
arguments, inject context, or block the call outright.

```go
sess, _ := pi.New(ctx, pi.Options{
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

The deny reason reaches the model as the tool result, so write it as an
instruction — "use the existing helper instead" teaches it more than "denied".

Events: `HookPreToolUse`, `HookPostToolUse`, `HookPostToolUseFailure`,
`HookPermissionRequest`, `HookUserPromptSubmit`, `HookSessionStart`,
`HookSessionEnd`, `HookStop`, `HookSubagentStart`, `HookSubagentStop`,
`HookPreCompact`, `HookNotification`.

## Structured output

Constrain the final answer to a shape, and get it back typed:

```go
type verdict struct {
    Answer     string `json:"answer" desc:"The capital city"`
    Confidence int    `json:"confidence" desc:"0 to 100"`
}

turn, _ := pi.Run(ctx, "What is the capital of France?", pi.Options{
    OutputSchema: pi.SchemaFor[verdict](),
})

var got verdict
json.Unmarshal(turn.StructuredOutput, &got) // {Paris 100}
```

A turn that fails validation comes back with `Subtype ==
"error_max_structured_output_retries"`.

## Streaming

```go
pi.Options{IncludePartialMessages: true}
```

turns on `DeltaEvent`, which carries tokens as they arrive; `Thinking`
distinguishes reasoning from answer text.

## Subagents

```go
pi.Options{Agents: map[string]pi.Agent{
    "reviewer": {Description: "Reviews code for bugs", Prompt: "You are a code reviewer."},
}}
```

## Events

```go
stop := sess.Subscribe(func(ev pi.Event) {
    switch e := ev.(type) {
    case pi.ReadyEvent:      // handshake: session id, model, tools offered
    case pi.TextEvent:       // assistant text
    case pi.ThinkingEvent:   // assistant reasoning
    case pi.ToolCallEvent:   // model wants to run a tool
    case pi.ToolResultEvent: // what the tool returned
    case pi.DeniedEvent:     // your approver refused one
    case pi.DeltaEvent:      // streamed token (IncludePartialMessages)
    case pi.StatusEvent:     // e.g. "compacting"
    case pi.CompactEvent:    // conversation was compacted
    case pi.RateLimitEvent:  // window utilisation and reset time
    case pi.AuthEvent:       // authentication progress
    case pi.TurnEvent:       // turn finished, with accounting
    case pi.ErrorEvent:      // protocol or transport failure
    }
})
defer stop()
```

## Multi-turn

The subprocess stays alive, so context and cost carry across prompts.

```go
sess, _ := pi.New(ctx, pi.Options{Tools: []string{"Read", "Grep"}})
defer sess.Close()

sess.Prompt(ctx, "What Go files are in this directory?")
sess.Prompt(ctx, "Which of those is the largest?") // still has the first in context
```

`Resume` and `Continue` pick up an earlier session. `Interrupt`, `SetModel` and
`SetPermissionMode` work mid-session.

## API

| | |
|---|---|
| `pi.Run(ctx, prompt, opts) (Turn, error)` | one-shot |
| `pi.New(ctx, opts) (*Session, error)` | long-lived session |
| `sess.Prompt(ctx, text) (Turn, error)` | send, block until the turn ends |
| `sess.Subscribe(fn) (stop func())` | watch events |
| `sess.OfferedTools() []string` | what the CLI actually gave the model |
| `sess.ID() string` | CLI session id |
| `sess.Interrupt() / SetModel() / SetPermissionMode() / SetMaxThinkingTokens()` | mid-session control |
| `sess.MCPStatus(ctx)` | external MCP server state |
| `sess.SetMCPServers(ctx, servers)` | replace external MCP servers |
| `sess.RewindFiles(ctx, id, dryRun)` | undo file edits (see caveat) |
| `pi.SchemaFor[T]()` | JSON Schema from a Go type |
| `sess.Close() error` | stop the subprocess; safe to call twice |
| `pi.DefineTool(name, desc, fn) Tool` | tool from a typed param struct |
| `pi.Text(...) / pi.Errorf(...)` | tool results |
| `pi.Allow() / pi.Deny(reason)` | permission decisions |

## A harness where every tool is yours

`examples/harness` is a working coding agent, and the shape most embedded agents
want: no built-in tools, no MCP servers from the machine, no shell. The tool
surface is entirely Go functions closing over state the host owns.

```
harness                       # interactive, in the current directory
harness "fix the flaky test"  # one shot
echo "..." | harness          # piped, one prompt per line
harness -C ./other "task"     # somewhere else
```

Five tools — `list_files`, `read_file`, `search`, `write_file`, `edit_file` —
over a `workspace` that resolves every path and refuses anything outside itself.
The sandbox is Go code, not a line in the prompt, so a model asking for
`../../.ssh/id_rsa` gets an error result it can read rather than a file. The
workspace keeps its own audit trail, so what changed is a fact the host holds
rather than something reconstructed from the transcript.

With no task it reads prompts from stdin on the same session, so the second turn
still knows what the first one found.

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

Ctrl-C stops the turn in progress and returns the prompt; Ctrl-D leaves.

Two behaviours you get only because the tools are yours: `edit_file` refuses an
ambiguous match instead of guessing which occurrence you meant, and the agent
cannot claim it ran the tests, because there is no tool that could.

## Examples

- `examples/harness` — a working coding agent: five custom tools, no built-ins, stdin REPL
- `examples/minimal` — one prompt, one answer
- `examples/tools-only` — every built-in off, one custom tool, verified end to end
- `examples/approve` — confining file reads to a directory
- `examples/streaming` — events across several turns
- `examples/verify` — hooks, streaming, structured output and subagents, checked live

`examples/tools-only` is the same demo written by hand against the raw protocol
in **332 lines**. Here it is **85**, and 40 of those print the verdict.

## Debugging

`PI_CLAUDE_DEBUG=1` logs every frame in both directions.

## Status

Tested against Claude Code 2.1.251. The protocol is not versioned or officially
published, so treat CLI upgrades as potentially breaking.

Verified against the live CLI: custom tools, tool-surface control, permissions,
hooks (including deny reasons reaching the model), streaming deltas, structured
output, subagents, and MCP status. Run `go run ./examples/verify` to re-check
after a CLI upgrade — it exits non-zero on regression.

One known limitation: `RewindFiles` plumbs through correctly but Claude Code
2.1.251 answers `{"canRewind": false, "error": "File rewinding is not
enabled."}` even with `EnableFileCheckpointing` set. The CLI exposes no flag for
it, so check `canRewind` in the reply rather than assuming.

Built on protocol notes from
[claude-code-stdio-protocol](https://github.com/TheLazyLemur/claude-agent-sdk-go),
which documents the wire format in full.

## Licence

MIT
