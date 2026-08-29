package pi

import (
	"encoding/json"
	"strings"
	"testing"
)

func argValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a != flag {
			continue
		}
		if i+1 < len(args) {
			return args[i+1], true
		}
		return "", true
	}
	return "", false
}

func TestBuildArgs_ExplicitDefaultPermissionModeIsSent(t *testing.T) {
	// given
	// ... a caller explicitly asking for the default permission mode, because
	// ... the machine's own config may be set to auto-approve
	opts := Options{PermissionMode: PermissionModeDefault}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... the flag is passed, so approval callbacks are actually consulted
	value, present := argValue(args, "--permission-mode")
	if !present || value != "default" {
		t.Fatalf("--permission-mode = %q (present %v), want default", value, present)
	}
}

func TestBuildArgs_UnsetPermissionModeIsLeftAlone(t *testing.T) {
	// given
	// ... options that say nothing about permissions
	opts := Options{}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... no flag is added, so the machine's configuration wins
	if _, present := argValue(args, "--permission-mode"); present {
		t.Fatal("--permission-mode should not be set from the zero value")
	}
}

func TestBuildArgs_NoToolsAll(t *testing.T) {
	// given
	// ... a session that must offer no tools but its own
	opts := Options{NoTools: NoToolsAll}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... built-ins are dropped and machine-configured MCP servers are ignored
	value, present := argValue(args, "--tools")
	if !present || value != "" {
		t.Fatalf("--tools = %q (present %v), want empty", value, present)
	}
	found := false
	for _, a := range args {
		if a == "--strict-mcp-config" {
			found = true
		}
	}
	if !found {
		t.Fatal("--strict-mcp-config missing, so machine MCP servers stay in reach")
	}
}

func TestBuildArgs_NoToolsBuiltinKeepsMCPServers(t *testing.T) {
	// given
	// ... a session dropping built-ins but keeping configured MCP servers
	opts := Options{NoTools: NoToolsBuiltin}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... only the built-in set is removed
	for _, a := range args {
		if a == "--strict-mcp-config" {
			t.Fatal("--strict-mcp-config should not be set for NoToolsBuiltin")
		}
	}
}

func TestBuildArgs_ToolsAllowlistIsCommaSeparated(t *testing.T) {
	// given
	// ... a session restricted to two built-in tools
	opts := Options{Tools: []string{"Read", "Grep"}}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... the allowlist is passed as a single value
	value, _ := argValue(args, "--tools")
	if value != "Read,Grep" {
		t.Fatalf("--tools = %q", value)
	}
}

func TestBuildArgs_NoToolsBeatsAllowlist(t *testing.T) {
	// given
	// ... an allowlist alongside an instruction to drop every tool
	opts := Options{Tools: []string{"Read"}, NoTools: NoToolsAll}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... dropping wins and --tools appears exactly once
	value, _ := argValue(args, "--tools")
	if value != "" {
		t.Fatalf("--tools = %q, want empty", value)
	}
	count := 0
	for _, a := range args {
		if a == "--tools" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("--tools appeared %d times", count)
	}
}

func TestBuildArgs_SystemPrompts(t *testing.T) {
	// given
	// ... a replaced system prompt and an appended one
	opts := Options{SystemPrompt: "you fill gaps", AppendSystemPrompt: "never change signatures"}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... both reach the CLI
	if v, _ := argValue(args, "--system-prompt"); v != "you fill gaps" {
		t.Fatalf("--system-prompt = %q", v)
	}
	if v, _ := argValue(args, "--append-system-prompt"); v != "never change signatures" {
		t.Fatalf("--append-system-prompt = %q", v)
	}
}

func TestBuildArgs_ZeroValueAddsNothing(t *testing.T) {
	// given
	// ... the zero Options
	opts := Options{}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... nothing is imposed on the CLI
	if len(args) != 0 {
		t.Fatalf("args = %v, want empty", args)
	}
}

func hasArg(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

// argValues returns every value following flag up to the next flag.
func argValues(args []string, flag string) []string {
	for i, a := range args {
		if a != flag {
			continue
		}
		var out []string
		for _, v := range args[i+1:] {
			if strings.HasPrefix(v, "--") {
				break
			}
			out = append(out, v)
		}
		return out
	}
	return nil
}

func TestBuildArgs_StreamingAndDirectories(t *testing.T) {
	// given
	// ... a session that streams deltas and may read two extra directories
	opts := Options{
		IncludePartialMessages: true,
		AdditionalDirectories:  []string{"/tmp/a", "/tmp/b"},
	}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... partial messages are enabled and both directories are passed
	if !hasArg(args, "--include-partial-messages") {
		t.Fatal("--include-partial-messages missing")
	}
	dirs := argValues(args, "--add-dir")
	if len(dirs) != 2 || dirs[0] != "/tmp/a" || dirs[1] != "/tmp/b" {
		t.Fatalf("--add-dir = %v", dirs)
	}
}

func TestBuildArgs_ResumeAndPersistence(t *testing.T) {
	// given
	// ... a forked resume of an earlier session that must not be saved
	opts := Options{
		Resume:               "sess-1",
		ForkSession:          true,
		ResumeSessionAt:      "msg-9",
		NoSessionPersistence: true,
	}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... every resume control reaches the CLI
	if v, _ := argValue(args, "--resume"); v != "sess-1" {
		t.Fatalf("--resume = %q", v)
	}
	if v, _ := argValue(args, "--resume-session-at"); v != "msg-9" {
		t.Fatalf("--resume-session-at = %q", v)
	}
	if !hasArg(args, "--fork-session") || !hasArg(args, "--no-session-persistence") {
		t.Fatalf("args = %v", args)
	}
}

func TestBuildArgs_ModelFallbackBetasAndSettings(t *testing.T) {
	// given
	// ... a session with a fallback model, betas, and restricted setting sources
	opts := Options{
		FallbackModel:  "sonnet",
		Betas:          []string{"context-1m-2025-08-07"},
		SettingSources: []string{"user", "project"},
	}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... each is passed in the form the CLI expects
	if v, _ := argValue(args, "--fallback-model"); v != "sonnet" {
		t.Fatalf("--fallback-model = %q", v)
	}
	if v := argValues(args, "--betas"); len(v) != 1 || v[0] != "context-1m-2025-08-07" {
		t.Fatalf("--betas = %v", v)
	}
	if v, _ := argValue(args, "--setting-sources"); v != "user,project" {
		t.Fatalf("--setting-sources = %q", v)
	}
}

func TestBuildArgs_StructuredOutputSchema(t *testing.T) {
	// given
	// ... a session that must answer in a fixed shape
	type answer struct {
		Verdict string `json:"verdict" desc:"pass or fail"`
	}
	opts := Options{OutputSchema: SchemaFor[answer]()}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... the schema is passed as JSON on the command line
	raw, present := argValue(args, "--json-schema")
	if !present {
		t.Fatal("--json-schema missing")
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if decoded["type"] != "object" {
		t.Fatalf("schema = %v", decoded)
	}
}

func TestBuildArgs_Agents(t *testing.T) {
	// given
	// ... a custom subagent definition
	opts := Options{Agents: map[string]Agent{
		"reviewer": {Description: "Reviews code", Prompt: "You are a reviewer", Model: "sonnet"},
	}}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... it is passed as a JSON object keyed by agent name
	raw, present := argValue(args, "--agents")
	if !present {
		t.Fatal("--agents missing")
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatalf("agents is not JSON: %v", err)
	}
	if decoded["reviewer"]["description"] != "Reviews code" {
		t.Fatalf("agents = %v", decoded)
	}
}

func TestBuildArgs_MCPConfigAndStrict(t *testing.T) {
	// given
	// ... an explicit MCP config that should be the only one used
	opts := Options{MCPConfig: []string{"/tmp/mcp.json"}, StrictMCPConfig: true}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... the config is loaded and machine configuration is ignored
	if v := argValues(args, "--mcp-config"); len(v) != 1 || v[0] != "/tmp/mcp.json" {
		t.Fatalf("--mcp-config = %v", v)
	}
	if !hasArg(args, "--strict-mcp-config") {
		t.Fatal("--strict-mcp-config missing")
	}
}

func TestBuildArgs_StrictMCPConfigNotDuplicated(t *testing.T) {
	// given
	// ... strict config requested directly and also implied by NoToolsAll
	opts := Options{NoTools: NoToolsAll, StrictMCPConfig: true}

	// when
	// ... the CLI args are built
	args := buildArgs(opts)

	// then
	// ... the flag appears once
	count := 0
	for _, a := range args {
		if a == "--strict-mcp-config" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("--strict-mcp-config appeared %d times", count)
	}
}
