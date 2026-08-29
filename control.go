package pi

import (
	"encoding/json"
	"fmt"

	"github.com/TheLazyLemur/pi-claude/internal/proto"
)

// onControlRequest answers the requests the CLI sends back to us: permission
// checks and calls into the tools we registered.
func (s *Session) onControlRequest(frame proto.Frame) {
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
		s.answerPermission(req.RequestID, ToolRequest{
			Name:           s.tools.bare(req.Request.ToolName),
			Qualified:      req.Request.ToolName,
			Input:          req.Request.Input,
			ID:             req.Request.ToolUseID,
			Mine:           s.tools.owns(req.Request.ToolName),
			AgentID:        req.Request.AgentID,
			BlockedPath:    req.Request.BlockedPath,
			DecisionReason: req.Request.DecisionReason,
			Suggestions:    req.Request.Suggestions,
		})
	case "mcp_message":
		s.answerMCP(req.RequestID, req.Request.ServerName, req.Request.Message)
	case "hook_callback":
		s.answerHook(req.RequestID, req.Request.CallbackID, req.Request.Input)
	case "initialize":
		s.respond(req.RequestID, map[string]any{})
	}
}

func (s *Session) answerPermission(requestID string, req ToolRequest) {
	decision := Allow()
	if s.opts.ApproveTool != nil {
		decision = s.opts.ApproveTool(s.ctx, req)
	}

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
		s.emit(DeniedEvent{Name: req.Name, Reason: decision.Reason})
		s.respond(requestID, body)
		return
	}

	// The CLI requires the original input echoed back on allow; an empty object
	// silently breaks the tool call.
	input := req.Input
	if input == nil {
		input = map[string]any{}
	}
	body["updatedInput"] = input
	s.respond(requestID, body)
}

func (s *Session) answerMCP(requestID, serverName string, msg map[string]any) {
	id := msg["id"]
	method, _ := msg["method"].(string)
	params, _ := msg["params"].(map[string]any)

	if serverName != s.tools.server {
		s.respondError(requestID, "unknown MCP server: "+serverName)
		return
	}

	result, err := s.tools.dispatch(s.ctx, method, params)
	if err != nil {
		s.respond(requestID, map[string]any{
			"mcp_response": map[string]any{
				"jsonrpc": "2.0",
				"id":      id,
				"error":   map[string]any{"code": -32000, "message": err.Error()},
			},
		})
		return
	}

	s.respond(requestID, map[string]any{
		"mcp_response": map[string]any{
			"jsonrpc": "2.0",
			"id":      id,
			"result":  result,
		},
	})
}

func (s *Session) respond(requestID string, body map[string]any) {
	s.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   body,
		},
	})
}

func (s *Session) respondError(requestID, msg string) {
	s.write(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      msg,
		},
	})
}

// onControlResponse hands the CLI's reply to whichever request is waiting.
func (s *Session) onControlResponse(frame proto.Frame) {
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

	s.mu.Lock()
	reply, waiting := s.awaiting[msg.Response.RequestID]
	s.mu.Unlock()
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
