package simulate

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestMarkLastMessageCacheable_StringContent verifies string content is
// upgraded to a text block with cache_control, matching Anthropic's contract.
func TestMarkLastMessageCacheable_StringContent(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "user", "content": "hello world"},
	}
	markLastMessageCacheable(msgs)

	blocks, ok := msgs[0]["content"].([]map[string]interface{})
	if !ok {
		t.Fatalf("string content should be upgraded to []map[string]interface{}, got %T", msgs[0]["content"])
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	if blocks[0]["type"] != "text" || blocks[0]["text"] != "hello world" {
		t.Errorf("block shape wrong: %+v", blocks[0])
	}
	cc, ok := blocks[0]["cache_control"].(map[string]string)
	if !ok || cc["type"] != "ephemeral" {
		t.Errorf("cache_control missing or wrong: %+v", blocks[0]["cache_control"])
	}
}

// TestMarkLastMessageCacheable_ToolResultList verifies a tool_result list gets
// cache_control on its last entry, without disturbing earlier entries.
func TestMarkLastMessageCacheable_ToolResultList(t *testing.T) {
	msgs := []map[string]interface{}{
		{"role": "user", "content": []map[string]interface{}{
			{"type": "tool_result", "tool_use_id": "a", "content": "first"},
			{"type": "tool_result", "tool_use_id": "b", "content": "second"},
		}},
	}
	markLastMessageCacheable(msgs)

	blocks := msgs[0]["content"].([]map[string]interface{})
	if _, hasCache := blocks[0]["cache_control"]; hasCache {
		t.Errorf("first entry should not have cache_control")
	}
	cc, ok := blocks[1]["cache_control"].(map[string]string)
	if !ok || cc["type"] != "ephemeral" {
		t.Errorf("cache_control missing on last entry: %+v", blocks[1])
	}
}

// TestMarkLastMessageCacheable_RawContent leaves raw JSON content untouched
// (assistant messages are never the last message being sent in the loop).
func TestMarkLastMessageCacheable_RawContent(t *testing.T) {
	raw := json.RawMessage(`[{"type":"text","text":"assistant reply"}]`)
	msgs := []map[string]interface{}{
		{"role": "assistant", "content": raw},
	}
	markLastMessageCacheable(msgs)
	if _, isRaw := msgs[0]["content"].(json.RawMessage); !isRaw {
		t.Errorf("raw content should be left as json.RawMessage, got %T", msgs[0]["content"])
	}
}

// TestMarkLastMessageCacheable_EmptyMessages is a no-op safety check.
func TestMarkLastMessageCacheable_EmptyMessages(t *testing.T) {
	markLastMessageCacheable(nil)
	markLastMessageCacheable([]map[string]interface{}{})
}

// TestRequestMarshaling_SystemAndToolsCacheMarkers verifies the on-wire JSON
// carries cache_control on the last tool and on the system block. This is the
// prefix that caches from turn 2 onward.
func TestRequestMarshaling_SystemAndToolsCacheMarkers(t *testing.T) {
	tools := []agentTool{
		{Name: "read", Description: "read a file", InputSchema: map[string]interface{}{"type": "object"}},
		{Name: "list", Description: "list a directory", InputSchema: map[string]interface{}{"type": "object"}},
	}
	tools[len(tools)-1].CacheControl = ephemeralCache

	req := agentAPIRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: 8192,
		System:    []systemBlock{{Type: "text", Text: "You are a senior Go engineer.", CacheControl: ephemeralCache}},
		Messages:  []map[string]interface{}{{"role": "user", "content": "hi"}},
		Tools:     tools,
	}
	out, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)

	// System block carries cache_control
	if !strings.Contains(s, `"system":[{"type":"text","text":"You are a senior Go engineer.","cache_control":{"type":"ephemeral"}}]`) {
		t.Errorf("system block missing cache_control marker in JSON: %s", s)
	}
	// Last tool carries cache_control
	if !strings.Contains(s, `"name":"list"`) || !strings.Contains(s, `"cache_control":{"type":"ephemeral"}`) {
		t.Errorf("last tool missing cache_control marker: %s", s)
	}
	// First tool does NOT carry cache_control
	firstToolStart := strings.Index(s, `"name":"read"`)
	firstToolEnd := strings.Index(s, `"name":"list"`)
	if firstToolStart == -1 || firstToolEnd == -1 || firstToolStart >= firstToolEnd {
		t.Fatalf("tool ordering broken in JSON: %s", s)
	}
	if strings.Contains(s[firstToolStart:firstToolEnd], "cache_control") {
		t.Errorf("first tool should NOT carry cache_control: %s", s[firstToolStart:firstToolEnd])
	}
}
