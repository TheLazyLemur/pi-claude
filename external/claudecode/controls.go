package claudecode

import (
	"context"

	"github.com/TheLazyLemur/pi-claude/core"
)

// controlReply is the CLI's answer to a control request we sent.
type controlReply struct {
	body map[string]any
	err  error
}

// control sends a control request without waiting for the answer.
func (c *Conversation) control(subtype string, fields map[string]any) error {
	c.mu.RLock()
	closed := c.closed
	c.mu.RUnlock()
	if closed {
		return core.ErrSessionClosed
	}

	fields["subtype"] = subtype
	return c.write(map[string]any{
		"type":       "control_request",
		"request_id": requestID(),
		"request":    fields,
	})
}

// request sends a control request and waits for the CLI's reply.
func (c *Conversation) request(ctx context.Context, subtype string, fields map[string]any) (map[string]any, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, core.ErrSessionClosed
	}
	id := requestID()
	reply := make(chan controlReply, 1)
	c.awaiting[id] = reply
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.awaiting, id)
		c.mu.Unlock()
	}()

	fields["subtype"] = subtype
	if err := c.write(map[string]any{
		"type":       "control_request",
		"request_id": id,
		"request":    fields,
	}); err != nil {
		return nil, err
	}

	select {
	case r := <-reply:
		return r.body, r.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.ctx.Done():
		return nil, core.ErrSessionClosed
	}
}

// SetModel switches model mid-conversation.
func (c *Conversation) SetModel(model string) error {
	return c.control("set_model", map[string]any{"model": model})
}

// SetPermissionMode changes permission behaviour mid-conversation.
func (c *Conversation) SetPermissionMode(mode core.PermissionMode) error {
	return c.control("set_permission_mode", map[string]any{"mode": string(mode)})
}

// SetMaxThinkingTokens caps extended thinking. Zero or less clears the cap.
func (c *Conversation) SetMaxThinkingTokens(tokens int) error {
	var value any
	if tokens > 0 {
		value = tokens
	}
	return c.control("set_max_thinking_tokens", map[string]any{"max_thinking_tokens": value})
}

// MCPStatus reports the connection state of external MCP servers.
func (c *Conversation) MCPStatus(ctx context.Context) (map[string]any, error) {
	return c.request(ctx, "mcp_status", map[string]any{})
}

// SetMCPServers replaces the external MCP servers for this conversation.
func (c *Conversation) SetMCPServers(ctx context.Context, servers map[string]any) (map[string]any, error) {
	return c.request(ctx, "mcp_set_servers", map[string]any{"servers": servers})
}

// RewindFiles undoes file edits back to a message id from the CLI's transcript.
//
// This needs file checkpointing enabled. Claude Code 2.1.251 answers
// {"canRewind": false, "error": "File rewinding is not enabled."} even with
// EnableFileCheckpointing set, so read canRewind rather than assuming.
func (c *Conversation) RewindFiles(ctx context.Context, userMessageID string, dryRun bool) (map[string]any, error) {
	return c.request(ctx, "rewind_files", map[string]any{
		"user_message_id": userMessageID,
		"dry_run":         dryRun,
	})
}
