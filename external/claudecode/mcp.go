// Package claudecode drives the Claude Code CLI over its stdio protocol.
//
// It is one implementation of core.Backend. Everything specific to Claude Code
// lives here: the flags, the control protocol, and the MCP shape custom tools
// are presented in. The core knows none of it.
package claudecode

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/TheLazyLemur/pi-claude/core"
)

// toolset answers the MCP JSON-RPC methods the CLI sends over the control
// protocol, so callers never see JSON-RPC.
type toolset struct {
	server string
	rt     core.Runtime
}

func newToolset(server string, rt core.Runtime) *toolset {
	return &toolset{server: server, rt: rt}
}

func (ts *toolset) empty() bool { return ts == nil || len(ts.rt.Tools()) == 0 }

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

// owns reports whether a tool name belongs to the host.
func (ts *toolset) owns(name string) bool {
	return ts.rt.Owns(ts.bare(name))
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
		tools := ts.rt.Tools()
		defs := make([]map[string]any, 0, len(tools))
		for _, tool := range tools {
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

	var args json.RawMessage
	if raw, ok := params["arguments"]; ok {
		encoded, err := json.Marshal(raw)
		if err != nil {
			return resultContent(core.Errorf("could not re-encode arguments: %v", err))
		}
		args = encoded
	}

	return resultContent(ts.rt.CallTool(ctx, ts.bare(name), args))
}

func resultContent(r core.ToolResult) map[string]any {
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
