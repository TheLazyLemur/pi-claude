package core

import (
	"context"
	"errors"
	"testing"
)

type greetParams struct {
	Name string `json:"name" desc:"Who to greet"`
}

func greetTool() Tool {
	return DefineTool("greet", "Greet someone by name",
		func(_ context.Context, p greetParams) (ToolResult, error) {
			return Text("hello %s", p.Name), nil
		})
}

func TestDefineTool_DerivesSchemaFromParams(t *testing.T) {
	// given
	// ... a tool defined from a typed params struct
	tool := greetTool()

	// when
	// ... its schema is inspected
	props := tool.Schema["properties"].(map[string]any)

	// then
	// ... the field, its type and its description came from the struct
	if tool.Name != "greet" || tool.Description != "Greet someone by name" {
		t.Fatalf("name/description = %q / %q", tool.Name, tool.Description)
	}
	name := props["name"].(map[string]any)
	if name["type"] != "string" || name["description"] != "Who to greet" {
		t.Fatalf("name property = %v", name)
	}
}

func TestRegistry_CallRunsTheTool(t *testing.T) {
	// given
	// ... a registry holding one tool
	r := newRegistry([]Tool{greetTool()})

	// when
	// ... it is called with arguments
	result := r.call(context.Background(), "greet", []byte(`{"name":"dan"}`))

	// then
	// ... the tool ran and its text came back
	if result.Text != "hello dan" {
		t.Fatalf("text = %q", result.Text)
	}
}

func TestRegistry_UnknownToolIsAnErrorResult(t *testing.T) {
	// given
	// ... a registry that does not hold the tool being asked for
	r := newRegistry([]Tool{greetTool()})

	// when
	// ... it is called
	result := r.call(context.Background(), "nope", nil)

	// then
	// ... the model is told, rather than the session failing
	if !result.IsError {
		t.Fatalf("result = %+v, want an error result", result)
	}
}

func TestRegistry_ExecuteErrorBecomesErrorResult(t *testing.T) {
	// given
	// ... a tool whose execute fails
	boom := DefineTool("boom", "always fails",
		func(_ context.Context, _ NoParams) (ToolResult, error) {
			return ToolResult{}, errors.New("disk on fire")
		})
	r := newRegistry([]Tool{boom})

	// when
	// ... it is called
	result := r.call(context.Background(), "boom", nil)

	// then
	// ... the failure reaches the model as something it can react to
	if !result.IsError || result.Text != "disk on fire" {
		t.Fatalf("result = %+v", result)
	}
}
