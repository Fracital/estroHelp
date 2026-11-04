package api

import (
	"encoding/json"
	"strings"
)

// Tool results that answer a call the upstream will not see in the same
// request are rejected outright ("tool_call_id is not found"). That happens
// when a client continues from a previous_response_id the relay no longer
// holds, or sends a trimmed history. Rather than fail the turn, the orphaned
// result is folded into a user message carrying the same text, so the model
// still sees what the tool returned.

// repairResponsesOrphans rewrites function_call_output items whose call_id
// matches no function_call in items. It returns the repaired list and how
// many items were rewritten.
func repairResponsesOrphans(items []json.RawMessage) ([]json.RawMessage, int) {
	calls := map[string]bool{}
	for _, raw := range items {
		var it struct {
			Type   string `json:"type"`
			CallID string `json:"call_id"`
		}
		if json.Unmarshal(raw, &it) == nil && it.Type == "function_call" && it.CallID != "" {
			calls[it.CallID] = true
		}
	}
	fixed := 0
	out := make([]json.RawMessage, 0, len(items))
	for _, raw := range items {
		var it struct {
			Type   string          `json:"type"`
			CallID string          `json:"call_id"`
			Output json.RawMessage `json:"output"`
		}
		if json.Unmarshal(raw, &it) != nil || it.Type != "function_call_output" || calls[it.CallID] {
			out = append(out, raw)
			continue
		}
		text := "Result of an earlier tool call (" + it.CallID + "):\n" + outputText(it.Output)
		msg, _ := json.Marshal(map[string]any{
			"type": "message", "role": "user",
			"content": []map[string]string{{"type": "input_text", "text": text}},
		})
		out = append(out, msg)
		fixed++
	}
	return out, fixed
}

// repairChatOrphans does the same for chat completions: a `tool` message whose
// tool_call_id answers no earlier assistant tool_calls entry becomes a user
// message.
func repairChatOrphans(messages []json.RawMessage) ([]json.RawMessage, int) {
	calls := map[string]bool{}
	fixed := 0
	out := make([]json.RawMessage, 0, len(messages))
	for _, raw := range messages {
		var m struct {
			Role       string          `json:"role"`
			ToolCallID string          `json:"tool_call_id"`
			Content    json.RawMessage `json:"content"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		}
		if json.Unmarshal(raw, &m) != nil {
			out = append(out, raw)
			continue
		}
		if m.Role == "assistant" {
			for _, tc := range m.ToolCalls {
				if tc.ID != "" {
					calls[tc.ID] = true
				}
			}
		}
		if m.Role != "tool" || calls[m.ToolCallID] {
			out = append(out, raw)
			continue
		}
		text := "Result of an earlier tool call (" + m.ToolCallID + "):\n" + outputText(m.Content)
		msg, _ := json.Marshal(map[string]string{"role": "user", "content": text})
		out = append(out, msg)
		fixed++
	}
	return out, fixed
}

// outputText renders a tool output (a string, or content parts) as plain text.
func outputText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil && len(parts) > 0 {
		var b strings.Builder
		for _, p := range parts {
			if p.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n")
				}
				b.WriteString(p.Text)
			}
		}
		if b.Len() > 0 {
			return b.String()
		}
	}
	return string(raw)
}
