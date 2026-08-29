package claudecode

import (
	"context"
	"errors"
	"testing"

	"github.com/TheLazyLemur/pi-claude/core"
)

type greetParams struct {
	Name string `json:"name" desc:"Who to greet"`
}

func greetTool() core.Tool {
	return core.DefineTool("greet", "Greet someone by name",
		func(_ context.Context, p greetParams) (core.ToolResult, error) {
			return core.Text("hello %s", p.Name), nil
		})
}

func TestToolset_ListReturnsMCPShape(t *testing.T) {
	// given
	// ... a toolset holding one tool
	ts := newToolset("pi", runtimeWith(greetTool()))

	// when
	// ... the CLI asks for the tool list
	result, err := ts.dispatch(context.Background(), "tools/list", nil)

	// then
	// ... the MCP tools/list shape comes back with our tool in it
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	list := result.(map[string]any)["tools"].([]map[string]any)
	if len(list) != 1 || list[0]["name"] != "greet" {
		t.Fatalf("tools = %v", list)
	}
	if _, ok := list[0]["inputSchema"]; !ok {
		t.Fatal("tool definition is missing inputSchema")
	}
}

func TestToolset_CallDecodesParams(t *testing.T) {
	// given
	// ... a toolset and a call carrying arguments
	ts := newToolset("pi", runtimeWith(greetTool()))
	params := map[string]any{"name": "greet", "arguments": map[string]any{"name": "dan"}}

	// when
	// ... the tool is called
	result, err := ts.dispatch(context.Background(), "tools/call", params)

	// then
	// ... the params were decoded into the struct and the text returned
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	content := result.(map[string]any)["content"].([]map[string]any)
	if content[0]["text"] != "hello dan" {
		t.Fatalf("text = %v", content[0]["text"])
	}
}

func TestToolset_CallUnknownToolIsAnErrorResult(t *testing.T) {
	// given
	// ... a call naming a tool that was never registered
	ts := newToolset("pi", runtimeWith(greetTool()))
	params := map[string]any{"name": "nope", "arguments": map[string]any{}}

	// when
	// ... it is dispatched
	result, err := ts.dispatch(context.Background(), "tools/call", params)

	// then
	// ... the model is told, rather than the session dying
	if err != nil {
		t.Fatalf("dispatch should not fail hard: %v", err)
	}
	if result.(map[string]any)["isError"] != true {
		t.Fatalf("result = %v, want isError", result)
	}
}

func TestToolset_ExecuteErrorBecomesErrorResult(t *testing.T) {
	// given
	// ... a tool whose execute fails
	boom := core.DefineTool("boom", "always fails",
		func(_ context.Context, _ core.NoParams) (core.ToolResult, error) {
			return core.ToolResult{}, errors.New("disk on fire")
		})
	ts := newToolset("pi", runtimeWith(boom))

	// when
	// ... it is called
	result, _ := ts.dispatch(context.Background(), "tools/call",
		map[string]any{"name": "boom", "arguments": map[string]any{}})

	// then
	// ... the failure reaches the model as an error result it can react to
	m := result.(map[string]any)
	if m["isError"] != true {
		t.Fatalf("result = %v, want isError", m)
	}
	content := m["content"].([]map[string]any)
	if content[0]["text"] != "disk on fire" {
		t.Fatalf("text = %v", content[0]["text"])
	}
}

func TestToolset_InitializeAdvertisesTools(t *testing.T) {
	// given
	// ... a toolset being initialised by the CLI
	ts := newToolset("pi", runtimeWith(greetTool()))

	// when
	// ... the MCP initialize handshake runs
	result, err := ts.dispatch(context.Background(), "initialize", nil)

	// then
	// ... it declares the tools capability under our server name
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	m := result.(map[string]any)
	if _, ok := m["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatalf("capabilities = %v", m["capabilities"])
	}
	if m["serverInfo"].(map[string]any)["name"] != "pi" {
		t.Fatalf("serverInfo = %v", m["serverInfo"])
	}
}

func TestToolset_QualifiedAndBareNames(t *testing.T) {
	// given
	// ... a toolset on the server name the session uses
	ts := newToolset("pi", runtimeWith(greetTool()))

	// when
	// ... names are converted in both directions
	qualified := ts.qualify("greet")
	bare := ts.bare("mcp__pi__greet")
	builtin := ts.bare("Read")
	foreign := ts.bare("mcp__other__thing")

	// then
	// ... our tools round-trip and other tools are left untouched
	if qualified != "mcp__pi__greet" {
		t.Fatalf("qualify = %q", qualified)
	}
	if bare != "greet" {
		t.Fatalf("bare = %q", bare)
	}
	if builtin != "Read" || foreign != "mcp__other__thing" {
		t.Fatalf("other names were rewritten: %q %q", builtin, foreign)
	}
}
