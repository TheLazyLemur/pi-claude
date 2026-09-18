package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/TheLazyLemur/pi-claude/core"
)

// block is one content block. The Messages API uses the same shape in both
// directions, so one struct covers assistant output and the tool results sent
// back up.
type block struct {
	Type string `json:"type"`

	// text
	Text string `json:"text,omitempty"`

	// thinking
	Thinking string `json:"thinking,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string `json:"tool_use_id,omitempty"`
	Content   string `json:"content,omitempty"`
	IsError   bool   `json:"is_error,omitempty"`
}

type message struct {
	Role    string  `json:"role"`
	Content []block `json:"content"`
}

func userText(text string) message {
	return message{Role: "user", Content: []block{{Type: "text", Text: text}}}
}

func toolResult(id, text string, isError bool) block {
	return block{Type: "tool_result", ToolUseID: id, Content: text, IsError: isError}
}

// tool is a function declaration on the wire. The schema is the host's own, so
// a tool is described once, in Go.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type request struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system,omitempty"`
	Messages  []message `json:"messages"`
	Tools     []tool    `json:"tools,omitempty"`
}

type response struct {
	ID         string     `json:"id"`
	Model      string     `json:"model"`
	Content    []block    `json:"content"`
	StopReason string     `json:"stop_reason"`
	Usage      core.Usage `json:"usage"`
}

// text is every text block in the response, concatenated.
func (r response) text() string {
	var out strings.Builder
	for _, b := range r.Content {
		if b.Type == "text" {
			out.WriteString(b.Text)
		}
	}
	return out.String()
}

// toolCalls are the tool_use blocks, in the order the model asked for them.
func (r response) toolCalls() []block {
	var out []block
	for _, b := range r.Content {
		if b.Type == "tool_use" {
			out = append(out, b)
		}
	}
	return out
}

// assistantTurn is the response as a history entry.
//
// Thinking blocks are left out: the API only accepts them back with the
// signature it issued, and an endpoint that emits unsigned ones would make the
// next request unsendable.
func (r response) assistantTurn() (message, bool) {
	msg := message{Role: "assistant"}
	for _, b := range r.Content {
		if b.Type == "text" || b.Type == "tool_use" {
			msg.Content = append(msg.Content, b)
		}
	}
	return msg, len(msg.Content) > 0
}

// client speaks POST /v1/messages and nothing else.
type client struct{ cfg Config }

func (c *client) send(ctx context.Context, body request) (response, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return response{}, fmt.Errorf("pi: anthropic: marshal request: %w", err)
	}

	url := strings.TrimSuffix(c.cfg.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return response{}, fmt.Errorf("pi: anthropic: build request: %w", err)
	}

	req.Header.Set("content-type", "application/json")
	req.Header.Set("anthropic-version", apiVersion)
	if c.cfg.AuthToken != "" {
		req.Header.Set("authorization", "Bearer "+c.cfg.AuthToken)
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("x-api-key", c.cfg.APIKey)
	}

	res, err := c.cfg.httpClient().Do(req)
	if err != nil {
		return response{}, fmt.Errorf("pi: anthropic: %w", err)
	}
	defer res.Body.Close()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, fmt.Errorf("pi: anthropic: read response: %w", err)
	}

	if res.StatusCode < 200 || res.StatusCode > 299 {
		return response{}, apiError(res.StatusCode, payload)
	}

	var decoded response
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return response{}, fmt.Errorf("pi: anthropic: parse response: %w", err)
	}
	return decoded, nil
}

// apiError turns the endpoint's own error body into a Go error, falling back to
// the raw body when it is not shaped like one.
func apiError(status int, payload []byte) error {
	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(payload, &body) == nil && body.Error.Message != "" {
		return fmt.Errorf("pi: anthropic: %s: %s", body.Error.Type, body.Error.Message)
	}
	return fmt.Errorf("pi: anthropic: http %d: %s", status, truncate(string(payload), 200))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
