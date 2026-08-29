package pi

import "testing"

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
