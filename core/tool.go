package core

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/TheLazyLemur/pi-claude/internal/jsonschema"
)

// NoParams is the parameter type for a tool that takes no arguments.
type NoParams = jsonschema.NoParams

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
// derived from P, so the shape is declared once.
func DefineTool[P any](name, description string, exec func(ctx context.Context, params P) (ToolResult, error)) Tool {
	var zero P
	return Tool{
		Name:        name,
		Description: description,
		Schema:      jsonschema.Of(zero),
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

// registry holds the host's tools in the order they were given.
type registry struct {
	order  []string
	byName map[string]Tool
}

func newRegistry(tools []Tool) *registry {
	r := &registry{byName: make(map[string]Tool, len(tools))}
	for _, tool := range tools {
		if _, exists := r.byName[tool.Name]; exists {
			continue
		}
		r.order = append(r.order, tool.Name)
		r.byName[tool.Name] = tool
	}
	return r
}

func (r *registry) list() []Tool {
	out := make([]Tool, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.byName[name])
	}
	return out
}

func (r *registry) has(name string) bool {
	_, found := r.byName[name]
	return found
}

// call runs a tool. Failure comes back as a result the model can read, never as
// a Go error, because the model is the one who has to do something about it.
func (r *registry) call(ctx context.Context, name string, args json.RawMessage) ToolResult {
	tool, found := r.byName[name]
	if !found {
		return Errorf("unknown tool: %s", name)
	}

	result, err := tool.Execute(ctx, args)
	if err != nil {
		return Errorf("%s", err.Error())
	}
	return result
}
