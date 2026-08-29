package claudecode

import (
	"encoding/json"
	"fmt"

	"github.com/TheLazyLemur/pi-claude/core"
	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// onControlRequest answers the requests the CLI sends back to us: permission
// checks and calls into the tools we registered.
func (c *Conversation) onControlRequest(frame proto.Frame) {
	var req struct {
		RequestID string `json:"request_id"`
		Request   struct {
			Subtype        string         `json:"subtype"`
			ToolName       string         `json:"tool_name"`
			ToolUseID      string         `json:"tool_use_id"`
			Input          map[string]any `json:"input"`
			AgentID        string         `json:"agent_id"`
			BlockedPath    string         `json:"blocked_path"`
			DecisionReason string         `json:"decision_reason"`
			Suggestions    []any          `json:"permission_suggestions"`
			ServerName     string         `json:"server_name"`
			Message        map[string]any `json:"message"`
			CallbackID     string         `json:"callback_id"`
		} `json:"request"`
	}
	if err := json.Unmarshal(frame.Raw, &req); err != nil {
		return
	}

	switch req.Request.Subtype {
	case "can_use_tool":
		c.answerPermission(req.RequestID, core.ToolRequest{
			Name:           c.tools.bare(req.Request.ToolName),
			Qualified:      req.Request.ToolName,
			Input:          req.Request.Input,
			ID:             req.Request.ToolUseID,
			Mine:           c.tools.owns(req.Request.ToolName),
			AgentID:        req.Request.AgentID,
			BlockedPath:    req.Request.BlockedPath,
			DecisionReason: req.Request.DecisionReason,
			Suggestions:    req.Request.Suggestions,
		})
	case "mcp_message":
		c.answerMCP(req.RequestID, req.Request.ServerName, req.Request.Message)
	case "hook_callback":
		c.answerHook(req.RequestID, req.Request.CallbackID, req.Request.Input)
	case "initialize":
		c.respond(req.RequestID, map[string]any{})
	}
}

func (c *Conversation) answerPermission(requestID string, req core.ToolRequest) {
	decision := c.rt.Approve(c.ctx, req)

	body := map[string]any{
		"behavior":  decision.Behavior,
		"toolUseID": req.ID,
	}

	if decision.Behavior == "deny" {
		if decision.Reason != "" {
			body["message"] = decision.Reason
		}
		if decision.Interrupt {
			body["interrupt"] = true
		}
		c.rt.Emit(core.DeniedEvent{Name: req.Name, Reason: decision.Reason})
		c.respond(requestID, body)
		return
	}

	// The CLI requires the original input echoed back on allow; an empty object
	// silently breaks the tool call.
	input := req.Input
	if input == nil {
		input = map[string]any{}
	}
	body["updatedInput"] = input
	c.respond(requestID, body)
}

func (c *Conversation) answerMCP(requestID, serverName string, msg map[string]any) {
	id := msg["id"]
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)

	if serverName != c.tools.server {
		c.respondError(requestID, "unknown MCP server: "+serverName)
		return
	}

	result, err := c.tools.dispatch(c.ctx, method, params)
	if err != nil {
		c.respond(requestID, map[string]any{
			"mcp_response": map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error":   map[string]any{"code": -32000, "message": err.Error()},
			},
		})
		return
	}

	c.respond(requestID, map[string]any{
		"mcp_response": map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		},
	})
}

func (c *Conversation) respond(requestID string, body map[string]any) {
	c.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   body,
		},
	})
}

func (c *Conversation) respondError(requestID, msg string) {
	c.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      msg,
		},
	})
}

// onControlResponse hands the CLI's reply to whichever request is waiting.
func (c *Conversation) onControlResponse(frame proto.Frame) {
	var msg struct {
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
			Error     string         `json:"error"`
		} `json:"response"`
	}
	if json.Unmarshal(frame.Raw, &msg) != nil {
		return
	}

	c.mu.Lock()
	reply, waiting := c.awaiting[msg.Response.RequestID]
	c.mu.Unlock()
	if !waiting {
		return
	}

	out := controlReply{body: msg.Response.Response}
	if msg.Response.Subtype == "error" {
		out.err = fmt.Errorf("pi: %s", msg.Response.Error)
	}

	select {
	case reply <- out:
	default:
	}
}
