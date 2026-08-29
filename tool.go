package pi

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// ToolResult is what a tool hands back to the model.
type ToolResult struct {
	// Text is what the model sees.
	Text string

	// Details are carried alongside the text for your own consumers. The model
	// does not see them.
	Details map[string]any

	// IsError tells the model the call failed, so it can try something else.
	IsError bool
}

// Text builds a successful tool result. The arguments are formatted as by
// fmt.Sprintf.
func Text(format string, args ...any) ToolResult {
	if len(args) == 0 {
		return ToolResult{Text: format}
	}
	return ToolResult{Text: fmt.Sprintf(format, args...)}
}

// Errorf builds a failed tool result. Prefer this over returning an error when
// the model can usefully react to the failure; return an error when it cannot.
func Errorf(format string, args ...any) ToolResult {
	return ToolResult{Text: fmt.Sprintf(format, args...), IsError: true}
}

// Tool is a function the model can call. Build one with DefineTool rather than
// filling this in by hand.
type Tool struct {
	// Name is what the model calls, e.g. "get_time".
	Name string

	// Label is a human-readable name for your own UI. Optional.
	Label string

	// Description tells the model when to reach for this tool. It is the single
	// biggest influence on whether the tool gets used correctly.
	Description string

	// Schema is the JSON Schema for the tool's arguments.
	Schema map[string]any

	// Execute runs the tool. Arguments arrive as raw JSON matching Schema.
	Execute func(ctx context.Context, args json.RawMessage) (ToolResult, error)
}

// DefineTool builds a Tool from a typed parameter struct. The JSON Schema is
// derived from P, so the shape is declared once:
//
//	type fillParams struct {
//	    Code string `json:"code" desc:"Replacement text for the region"`
//	}
//
//	tool := pi.DefineTool("submit_fill", "Submit the code that replaces the region",
//	    func(ctx context.Context, p fillParams) (pi.ToolResult, error) {
//	        return pi.Text("applied %d bytes", len(p.Code)), nil
//	    })
//
// Use pi.NoParams for a tool that takes no arguments.
func DefineTool[P any](name, description string, exec func(ctx context.Context, params P) (ToolResult, error)) Tool {
	var zero P
	return Tool{
		Name:        name,
		Description: description,
		Schema:      schemaOf(zero),
		Execute: func(ctx context.Context, args json.RawMessage) (ToolResult, error) {
			var params P
			if len(args) > 0 {
				if err := json.Unmarshal(args, &params); err != nil {
					return Errorf("could not decode arguments: %v", err), nil
				}
			}
			return exec(ctx, params)
		},
	}
}

// toolset answers the MCP JSON-RPC methods the CLI sends over the control
// protocol, so callers never see JSON-RPC.
type toolset struct {
	server string
	order  []string
	byName map[string]Tool
}

func newToolset(server string, tools []Tool) *toolset {
	ts := &toolset{server: server, byName: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		if _, exists := ts.byName[tool.Name]; exists {
			continue
		}
		ts.order = append(ts.order, tool.Name)
		ts.byName[tool.Name] = tool
	}
	return ts
}

func (ts *toolset) empty() bool { return ts == nil || len(ts.order) == 0 }

// qualify returns the name the model sees for one of our tools.
func (ts *toolset) qualify(name string) string {
	return fmt.Sprintf("mcp__%s__%s", ts.server, name)
}

// bare strips our server prefix so callers see the name they registered.
// Built-in tools and tools from other servers are returned unchanged.
func (ts *toolset) bare(name string) string {
	prefix := fmt.Sprintf("mcp__%s__", ts.server)
	if !strings.HasPrefix(name, prefix) {
		return name
	}
	return strings.TrimPrefix(name, prefix)
}

// owns reports whether a tool name belongs to this toolset.
func (ts *toolset) owns(name string) bool {
	_, found := ts.byName[ts.bare(name)]
	return found
}

// dispatch handles one MCP JSON-RPC method.
func (ts *toolset) dispatch(ctx context.Context, method string, params map[string]any) (any, error) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": "2024-11-05",
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": ts.server, "version": "1.0.0"},
		}, nil

	case "notifications/initialized":
		return map[string]any{}, nil

	case "tools/list":
		defs := make([]map[string]any, 0, len(ts.order))
		for _, name := range ts.order {
			tool := ts.byName[name]
			def := map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"inputSchema": tool.Schema,
			}
			if tool.Label != "" {
				def["label"] = tool.Label
			}
			defs = append(defs, def)
		}
		return map[string]any{"tools": defs}, nil

	case "tools/call":
		return ts.call(ctx, params), nil

	default:
		return map[string]any{}, nil
	}
}

func (ts *toolset) call(ctx context.Context, params map[string]any) map[string]any {
	name, _ := params["name"].(string)
	tool, found := ts.byName[ts.bare(name)]
	if !found {
		return resultContent(Errorf("unknown tool: %s", name))
	}

	var args json.RawMessage
	if raw, ok := params["arguments"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return resultContent(Errorf("could not re-encode arguments: %v", err))
		}
		args = encoded
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		return resultContent(Errorf("%s", err.Error()))
	}
	return resultContent(result)
}

func resultContent(r ToolResult) map[string]any {
	out := map[string]any{
		"content": []map[string]any{{"type": "text", "text": r.Text}},
	}
	if r.IsError {
		out["isError"] = true
	}
	if len(r.Details) > 0 {
		out["details"] = r.Details
	}
	return out
}
