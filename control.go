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
			Subtype    string         `json:"subtype"`
			ToolName   string         `json:"tool_name"`
			ToolUseID  string         `json:"tool_use_id"`
			Input      map[string]any `json:"input"`
			ServerName string         `json:"server_name"`
			Message    map[string]any `json:"message"`
			CallbackID string         `json:"callback_id"`
		} `json:"request"`
	}
	if err := json.Unmarshal(frame.Raw, &req); err != nil {
		return
	}

	switch req.Request.Subtype {
	case "can_use_tool":
		s.answerPermission(req.RequestID, req.Request.ToolName, req.Request.ToolUseID, req.Request.Input)
	case "mcp_message":
		s.answerMCP(req.RequestID, req.Request.ServerName, req.Request.Message)
	case "hook_callback":
		s.answerHook(req.RequestID, req.Request.CallbackID, req.Request.Input)
	case "initialize":
		s.respond(req.RequestID, map[string]any{})
	}
}

func (s *Session) answerPermission(requestID, toolName, toolUseID string, input map[string]any) {
	decision := Allow()
	if s.opts.ApproveTool != nil {
		decision = s.opts.ApproveTool(s.ctx, ToolRequest{
			Name:      s.tools.bare(toolName),
			Qualified: toolName,
			Input:     input,
			ID:        toolUseID,
			Mine:      s.tools.owns(toolName),
		})
	}

	body := map[string]any{
		"behavior":  decision.Behavior,
		"toolUseID": toolUseID,
	}

	if decision.Behavior == "deny" {
		if decision.Reason != "" {
			body["message"] = decision.Reason
		}
		if decision.Interrupt {
			body["interrupt"] = true
		}
		s.emit(DeniedEvent{Name: s.tools.bare(toolName), Reason: decision.Reason})
		s.respond(requestID, body)
		return
	}

	// The CLI requires the original input echoed back on allow; an empty object
	// silently breaks the tool call.
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
