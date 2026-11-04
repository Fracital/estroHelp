package api

import (
	"encoding/json"
	"strings"
	"testing"
)

func rawList(items ...string) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(items))
	for _, s := range items {
		out = append(out, json.RawMessage(s))
	}
	return out
}

func TestRepairResponsesOrphansKeepsAnsweredCalls(t *testing.T) {
	items := rawList(
		`{"type":"function_call","call_id":"c1","name":"ls","arguments":"{}"}`,
		`{"type":"function_call_output","call_id":"c1","output":"a.txt"}`,
	)
	out, fixed := repairResponsesOrphans(items)
	if fixed != 0 || string(out[1]) != string(items[1]) {
		t.Fatalf("answered call rewritten: fixed=%d out=%s", fixed, out[1])
	}
}

func TestRepairResponsesOrphansFoldsUnansweredIntoUserMessage(t *testing.T) {
	items := rawList(
		`{"type":"function_call_output","call_id":"c9","output":[{"type":"input_text","text":"42 lines"}]}`,
		`{"type":"message","role":"user","content":"continue"}`,
	)
	out, fixed := repairResponsesOrphans(items)
	if fixed != 1 {
		t.Fatalf("fixed=%d", fixed)
	}
	var m struct {
		Type, Role string
		Content    []struct{ Type, Text string }
	}
	if err := json.Unmarshal(out[0], &m); err != nil || m.Type != "message" || m.Role != "user" {
		t.Fatalf("not a user message: %s", out[0])
	}
	if !strings.Contains(m.Content[0].Text, "c9") || !strings.Contains(m.Content[0].Text, "42 lines") {
		t.Fatalf("result text lost: %q", m.Content[0].Text)
	}
}

func TestRepairChatOrphans(t *testing.T) {
	msgs := rawList(
		`{"role":"assistant","content":null,"tool_calls":[{"id":"t1","type":"function","function":{"name":"ls","arguments":"{}"}}]}`,
		`{"role":"tool","tool_call_id":"t1","content":"ok"}`,
		`{"role":"tool","tool_call_id":"t2","content":"stale"}`,
	)
	out, fixed := repairChatOrphans(msgs)
	if fixed != 1 || string(out[1]) != string(msgs[1]) {
		t.Fatalf("fixed=%d out[1]=%s", fixed, out[1])
	}
	var m struct{ Role, Content string }
	if json.Unmarshal(out[2], &m) != nil || m.Role != "user" || !strings.Contains(m.Content, "stale") {
		t.Fatalf("orphan not folded: %s", out[2])
	}
}
