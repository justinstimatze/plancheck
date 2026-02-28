// agentapi.go contains API types and the HTTP client for the Claude API.
package simulate

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// --- Claude API types for tool use ---

type agentTool struct {
	Name        string                 `json:"name"`
	Description string                 `json:"description"`
	InputSchema map[string]interface{} `json:"input_schema"`
}

type agentMessage struct {
	Role         string      `json:"role"`
	Content      interface{} `json:"content"` // string or []contentBlock
	ToolUseID    string      `json:"-"`
	IsToolResult bool        `json:"-"`
	IsRaw        bool        `json:"-"`
}

type agentAPIRequest struct {
	Model     string                   `json:"model"`
	MaxTokens int                      `json:"max_tokens"`
	System    string                   `json:"system,omitempty"`
	Messages  []map[string]interface{} `json:"messages"`
	Tools     []agentTool              `json:"tools,omitempty"`
}

type agentAPIResponse struct {
	Content    []contentBlock  `json:"content"`
	RawContent json.RawMessage `json:"-"`
	StopReason string          `json:"stop_reason"`
	Usage      apiUsage        `json:"usage"`
}

type contentBlock struct {
	Type  string                 `json:"type"`
	Text  string                 `json:"text,omitempty"`
	ID    string                 `json:"id,omitempty"`
	Name  string                 `json:"name,omitempty"`
	Input map[string]interface{} `json:"input,omitempty"`
}

func callAgentAPI(key, model, system string, messages []agentMessage, tools []agentTool) (*agentAPIResponse, error) {
	// Build messages array for API
	var apiMessages []map[string]interface{}
	for _, m := range messages {
		msg := map[string]interface{}{"role": m.Role}
		if m.IsToolResult {
			msg["content"] = []map[string]interface{}{
				{
					"type":        "tool_result",
					"tool_use_id": m.ToolUseID,
					"content":     m.Content,
				},
			}
		} else if m.IsRaw {
			msg["content"] = m.Content
		} else {
			msg["content"] = m.Content
		}
		apiMessages = append(apiMessages, msg)
	}

	reqBody := agentAPIRequest{
		Model:     model,
		MaxTokens: 8192,
		System:    system,
		Messages:  apiMessages,
		Tools:     tools,
	}

	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 120 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(body[:min(len(body), 200)]))
	}

	var result agentAPIResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("parse response: %w", err)
	}

	// Preserve raw content for passing back as assistant message
	var raw struct {
		Content json.RawMessage `json:"content"`
	}
	json.Unmarshal(body, &raw)
	result.RawContent = raw.Content

	return &result, nil
}
