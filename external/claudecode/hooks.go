package claudecode

import (
	"github.com/TheLazyLemur/pi-claude/core"
)

// buildHooks turns the host's hooks into the initialize payload. The callbacks
// themselves stay with the host; this only names them.
func buildHooks(hooks map[core.HookEvent][]core.HookMatcher) map[string]any {
	if len(hooks) == 0 {
		return nil
	}

	config := make(map[string]any, len(hooks))
	for event, matchers := range hooks {
		entries := make([]map[string]any, 0, len(matchers))
		for i, matcher := range matchers {
			ids := make([]string, 0, len(matcher.Hooks))
			for j := range matcher.Hooks {
				ids = append(ids, core.HookCallbackID(event, i, j))
			}

			entry := map[string]any{"hookCallbackIds": ids}
			if matcher.Matcher != "" {
				entry["matcher"] = matcher.Matcher
			}
			if matcher.Timeout > 0 {
				entry["timeout"] = matcher.Timeout
			}
			entries = append(entries, entry)
		}
		config[string(event)] = entries
	}
	return config
}

// answerHook runs one hook through the host and replies to the CLI.
func (c *Conversation) answerHook(requestID, callbackID string, raw map[string]any) {
	in := core.HookInput{
		Event:          core.HookEvent(stringField(raw, "hook_event_name")),
		ToolName:       stringField(raw, "tool_name"),
		ToolInput:      mapField(raw, "tool_input"),
		ToolResponse:   raw["tool_response"],
		ToolUseID:      stringField(raw, "tool_use_id"),
		SessionID:      stringField(raw, "session_id"),
		CWD:            stringField(raw, "cwd"),
		TranscriptPath: stringField(raw, "transcript_path"),
		PermissionMode: stringField(raw, "permission_mode"),
		Prompt:         stringField(raw, "prompt"),
		Error:          stringField(raw, "error"),
	}

	out, err := c.rt.RunHook(c.ctx, callbackID, in)
	if err != nil {
		c.respondError(requestID, err.Error())
		return
	}

	body := map[string]any{"continue": out.Continue}
	if out.StopReason != "" {
		body["stopReason"] = out.StopReason
	}
	if out.SystemMessage != "" {
		body["systemMessage"] = out.SystemMessage
	}
	if out.SuppressOutput {
		body["suppressOutput"] = true
	}

	specific := map[string]any{"hookEventName": string(in.Event)}
	if out.PermissionDecision != "" {
		specific["permissionDecision"] = out.PermissionDecision
	}
	if out.PermissionDecisionReason != "" {
		specific["permissionDecisionReason"] = out.PermissionDecisionReason
	}
	if out.UpdatedInput != nil {
		specific["updatedInput"] = out.UpdatedInput
	}
	if out.AdditionalContext != "" {
		specific["additionalContext"] = out.AdditionalContext
	}
	if len(specific) > 1 {
		body["hookSpecificOutput"] = specific
	}

	c.respond(requestID, body)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func mapField(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}
