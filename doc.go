// Package pi drives the Claude Code CLI over its stdio protocol.
//
// It keeps one claude subprocess alive per session and hands you a small,
// typed API: prompts return a completed [Turn], events arrive through
// [Session.Subscribe], and your own Go functions become tools the model can
// call — with no MCP server, no port, and no JSON-RPC in your code.
//
// The shape is borrowed from the pi agent harness (https://pi.dev): one
// options struct, one factory, declarative tools, a subscribe function that
// returns its own unsubscribe.
//
// # Getting started
//
//	turn, err := pi.Run(ctx, "What is a goroutine?", pi.Options{NoTools: pi.NoToolsAll})
//	fmt.Println(turn.Text)
//
// # Tools
//
// Parameters are a Go struct. The JSON Schema is derived from it, so the shape
// is declared once:
//
//	type fillParams struct {
//	    Code string `json:"code" desc:"Replacement text for the region"`
//	}
//
//	fill := pi.DefineTool("submit_fill", "Submit the replacement code",
//	    func(ctx context.Context, p fillParams) (pi.ToolResult, error) {
//	        return pi.Text("applied %d bytes", len(p.Code)), nil
//	    })
//
// # Beyond prompting
//
// Hooks run at points in the CLI's lifecycle and can block or rewrite tool
// calls ([Options.Hooks]). [Options.OutputSchema] constrains the final answer
// to a JSON shape, returned in [Turn.StructuredOutput].
// [Options.IncludePartialMessages] streams [DeltaEvent] as tokens arrive.
// [Options.Agents] defines subagents the model can delegate to.
//
// # Locking down the tool surface
//
// A developer machine commonly has dozens of MCP tools configured globally. If
// you are embedding an agent in your own product, [NoToolsAll] removes the
// built-in tools and every machine-configured MCP server, leaving only the
// tools you register.
package pi
