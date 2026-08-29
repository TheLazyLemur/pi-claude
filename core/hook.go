package core

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

// HookCallbackID is the stable id a hook is registered under, so a backend can
// name it when firing. The shape is the backend's business; the mapping is not.
func HookCallbackID(event HookEvent, matcher, hook int) string {
	return fmt.Sprintf("%s_%d_%d", event, matcher, hook)
}

// hookCallbacks flattens the host's hooks into the table RunHook looks in.
func hookCallbacks(hooks map[HookEvent][]HookMatcher) map[string]HookFunc {
	out := map[string]HookFunc{}
	for event, matchers := range hooks {
		for i, matcher := range matchers {
			for j, fn := range matcher.Hooks {
				out[HookCallbackID(event, i, j)] = fn
			}
		}
	}
	return out
}
