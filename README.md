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
| `sess.Interrupt() / SetModel() / SetPermissionMode()` | mid-session control |
| `sess.Close() error` | stop the subprocess; safe to call twice |
| `pi.DefineTool(name, desc, fn) Tool` | tool from a typed param struct |
| `pi.Text(...) / pi.Errorf(...)` | tool results |
| `pi.Allow() / pi.Deny(reason)` | permission decisions |

## Examples

- `examples/minimal` — one prompt, one answer
- `examples/tools-only` — every built-in off, one custom tool, verified end to end
- `examples/approve` — confining file reads to a directory
- `examples/streaming` — events across several turns

`examples/tools-only` is the same demo written by hand against the raw protocol
in **332 lines**. Here it is **85**, and 40 of those print the verdict.

## Debugging

`PI_CLAUDE_DEBUG=1` logs every frame in both directions.

## Status

Tested against Claude Code 2.1.251. The protocol is not versioned or officially
published, so treat CLI upgrades as potentially breaking.

Not yet covered: hooks, streaming partial messages, structured JSON-schema
output, file checkpointing and rewind, subagent definitions.

Built on protocol notes from
[claude-code-stdio-protocol](https://github.com/TheLazyLemur/claude-agent-sdk-go),
which documents the wire format in full.

## Licence

MIT
