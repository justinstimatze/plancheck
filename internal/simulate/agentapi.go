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

// ephemeralCache is the cache_control marker value for the 5-minute cache tier.
// Anthropic caches everything in the request up to (and including) any content
// element carrying this marker; later requests with the same prefix pay ~10%
// input cost on the cached portion. Mark it on the last static element (the
// last tool) and the last message-content element being sent each turn.
var ephemeralCache = &cacheControl{Type: "ephemeral"}

type cacheControl struct {
	Type string `json:"type"`
}

type agentTool struct {
	Name         string                 `json:"name"`
	Description  string                 `json:"description"`
	InputSchema  map[string]interface{} `json:"input_schema"`
	CacheControl *cacheControl          `json:"cache_control,omitempty"`
}

type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
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
	System    []systemBlock            `json:"system,omitempty"`
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

// markLastMessageCacheable attaches ephemeral cache_control to the final
// content element of the final message in msgs. Anthropic caches everything
// in the request up to and including this element. String content is upgraded
// to a text block; a tool_result list gets the marker on its last entry.
// Raw assistant content is left alone (assistant messages are always followed
// by a user message in this loop, so this case does not arise on the wire).
func markLastMessageCacheable(msgs []map[string]interface{}) {
	if len(msgs) == 0 {
		return
	}
	last := msgs[len(msgs)-1]
	switch c := last["content"].(type) {
	case string:
		last["content"] = []map[string]interface{}{
			{"type": "text", "text": c, "cache_control": map[string]string{"type": "ephemeral"}},
		}
	case []map[string]interface{}:
		if len(c) > 0 {
			c[len(c)-1]["cache_control"] = map[string]string{"type": "ephemeral"}
		}
	}
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

	// Mark the tools+system prefix cacheable so turns 2..N pay ~10% input cost
	// on it. Marker goes on the last tool; system precedes tools in the request
	// so a single marker at the tools tail caches both.
	if len(tools) > 0 {
		tools[len(tools)-1].CacheControl = ephemeralCache
	}

	// Mark the growing message history cacheable through the last block being
	// sent this turn. Everything before the marker (all prior turns, all tool
	// results, plus system+tools) reuses cached tokens on the next call.
	markLastMessageCacheable(apiMessages)

	var systemBlocks []systemBlock
	if system != "" {
		systemBlocks = []systemBlock{{Type: "text", Text: system}}
	}

	reqBody := agentAPIRequest{
		Model:     model,
		MaxTokens: 8192,
		System:    systemBlocks,
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
