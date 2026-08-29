package pi

import (
	"context"
	"fmt"
)

// HookEvent is a point in the CLI's lifecycle a hook can run at.
type HookEvent string

const (
	HookPreToolUse         HookEvent = "PreToolUse"
	HookPostToolUse        HookEvent = "PostToolUse"
	HookPostToolUseFailure HookEvent = "PostToolUseFailure"
	HookPermissionRequest  HookEvent = "PermissionRequest"
	HookUserPromptSubmit   HookEvent = "UserPromptSubmit"
	HookSessionStart       HookEvent = "SessionStart"
	HookSessionEnd         HookEvent = "SessionEnd"
	HookStop               HookEvent = "Stop"
	HookSubagentStart      HookEvent = "SubagentStart"
	HookSubagentStop       HookEvent = "SubagentStop"
	HookPreCompact         HookEvent = "PreCompact"
	HookNotification       HookEvent = "Notification"
)

// HookInput is what the CLI passes to a hook.
type HookInput struct {
	Event          HookEvent
	ToolName       string
	ToolInput      map[string]any
	ToolResponse   any
	ToolUseID      string
	SessionID      string
	CWD            string
	TranscriptPath string
	PermissionMode string
	Prompt         string
	Error          string
}

// HookOutput is a hook's answer.
type HookOutput struct {
	// Continue false stops the session.
	Continue bool

	// PermissionDecision is "allow", "deny" or "ask" for tool-use hooks.
	PermissionDecision string

	// PermissionDecisionReason explains a deny to the model.
	PermissionDecisionReason string

	// UpdatedInput rewrites the tool's arguments before it runs.
	UpdatedInput map[string]any

	// AdditionalContext is injected into the conversation.
	AdditionalContext string

	// SystemMessage is shown to the user.
	SystemMessage string

	// StopReason explains a Continue of false.
	StopReason string

	// SuppressOutput hides the hook's output from the transcript.
	SuppressOutput bool
}

// HookContinue lets the session carry on unchanged.
func HookContinue() HookOutput { return HookOutput{Continue: true} }

// HookDeny blocks the tool call the hook was fired for.
func HookDeny(reason string) HookOutput {
	return HookOutput{
		Continue:                 true,
		PermissionDecision:       "deny",
		PermissionDecisionReason: reason,
	}
}

// HookFunc runs at a HookEvent.
type HookFunc func(ctx context.Context, in HookInput) (HookOutput, error)

// HookMatcher scopes a set of hooks to matching tools.
type HookMatcher struct {
	// Matcher is a regular expression over tool names. Empty matches all.
	Matcher string

	// Hooks run in order.
	Hooks []HookFunc

	// Timeout in seconds. Zero uses the CLI default.
	Timeout int
}

// hookCallbackID is the stable id a hook is registered under, so the CLI can
// name it when firing.
func hookCallbackID(event HookEvent, matcher, hook int) string {
	return fmt.Sprintf("%s_%d_%d", event, matcher, hook)
}

// buildHooks turns the configured hooks into the initialize payload and the
// callback table used to dispatch them.
func buildHooks(hooks map[HookEvent][]HookMatcher) (map[string]any, map[string]HookFunc) {
	if len(hooks) == 0 {
		return nil, nil
	}

	config := make(map[string]any, len(hooks))
	callbacks := make(map[string]HookFunc)

	for event, matchers := range hooks {
		entries := make([]map[string]any, 0, len(matchers))
		for i, matcher := range matchers {
			ids := make([]string, 0, len(matcher.Hooks))
			for j, fn := range matcher.Hooks {
				id := hookCallbackID(event, i, j)
				ids = append(ids, id)
				callbacks[id] = fn
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

	return config, callbacks
}

// answerHook runs one hook callback and replies to the CLI.
func (s *Session) answerHook(requestID, callbackID string, raw map[string]any) {
	fn, found := s.hooks[callbackID]
	if !found {
		s.respondError(requestID, "unknown hook callback: "+callbackID)
		return
	}

	in := HookInput{
		Event:          HookEvent(stringField(raw, "hook_event_name")),
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

	out, err := fn(s.ctx, in)
	if err != nil {
		s.respondError(requestID, err.Error())
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

	s.respond(requestID, body)
}

func stringField(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func mapField(m map[string]any, key string) map[string]any {
	v, _ := m[key].(map[string]any)
	return v
}
