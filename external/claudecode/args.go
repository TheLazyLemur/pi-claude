package claudecode

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/TheLazyLemur/pi-claude/core"
)

// buildArgs turns Options into CLI flags.
func buildArgs(opts core.Options) []string {
	var args []string

	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(opts.MaxTurns))
	}
	if opts.MaxBudgetUSD > 0 {
		args = append(args, "--max-budget-usd", strconv.FormatFloat(opts.MaxBudgetUSD, 'f', 2, 64))
	}
	if opts.SystemPrompt != "" {
		args = append(args, "--system-prompt", opts.SystemPrompt)
	}
	if opts.AppendSystemPrompt != "" {
		args = append(args, "--append-system-prompt", opts.AppendSystemPrompt)
	}
	// Sent whenever explicitly set, default included: the machine config may be
	// set to auto-approve, and only an explicit flag brings ApproveTool back.
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", string(opts.PermissionMode))
	}
	if opts.Resume != "" {
		args = append(args, "--resume", opts.Resume)
	}
	if opts.Continue {
		args = append(args, "--continue")
	}
	if opts.ResumeSessionAt != "" {
		args = append(args, "--resume-session-at", opts.ResumeSessionAt)
	}
	if opts.ForkSession {
		args = append(args, "--fork-session")
	}
	if opts.NoSessionPersistence {
		args = append(args, "--no-session-persistence")
	}
	if opts.IncludePartialMessages {
		args = append(args, "--include-partial-messages")
	}
	if opts.FallbackModel != "" {
		args = append(args, "--fallback-model", opts.FallbackModel)
	}
	if len(opts.AdditionalDirectories) > 0 {
		args = append(args, "--add-dir")
		args = append(args, opts.AdditionalDirectories...)
	}
	if len(opts.Betas) > 0 {
		args = append(args, "--betas")
		args = append(args, opts.Betas...)
	}
	if len(opts.SettingSources) > 0 {
		args = append(args, "--setting-sources", strings.Join(opts.SettingSources, ","))
	}
	if len(opts.MCPConfig) > 0 {
		args = append(args, "--mcp-config")
		args = append(args, opts.MCPConfig...)
	}
	if len(opts.OutputSchema) > 0 {
		if encoded, err := json.Marshal(opts.OutputSchema); err == nil {
			args = append(args, "--json-schema", string(encoded))
		}
	}
	if len(opts.Agents) > 0 {
		if encoded, err := json.Marshal(opts.Agents); err == nil {
			args = append(args, "--agents", string(encoded))
		}
	}

	// NoTools wins over an allowlist: an allowlist is meaningless once the
	// built-in set is dropped.
	strictMCP := opts.StrictMCPConfig
	switch {
	case opts.NoTools == core.NoToolsAll:
		args = append(args, "--tools", "")
		strictMCP = true
	case opts.NoTools == core.NoToolsBuiltin:
		args = append(args, "--tools", "")
	case len(opts.Tools) > 0:
		args = append(args, "--tools", strings.Join(opts.Tools, ","))
	}

	if strictMCP {
		args = append(args, "--strict-mcp-config")
	}

	return args
}
