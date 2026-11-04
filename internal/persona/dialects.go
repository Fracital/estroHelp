package persona

import (
	"encoding/json"
)

// ApplyAnthropic builds an Anthropic Messages request body: the persona's
// prompt replaces the client's `system` (string or blocks) and the primer
// exchange leads the messages. User turns are cleaned of harness text.
func (p *Persona) ApplyAnthropic(system json.RawMessage, messages []json.RawMessage) (json.RawMessage, []json.RawMessage) {
	newSystem, _ := json.Marshal(p.System)
	out := make([]json.RawMessage, 0, len(messages)+2)
	out = append(out, p.primerMessages(false)...)
	for _, raw := range messages {
		role, obj := roleOf(raw)
		switch role {
		case "system", "developer":
			continue
		case "user":
			cleaned := CleanUserContent(obj["content"])
			if cleaned == nil {
				continue
			}
			obj["content"] = cleaned
			b, _ := json.Marshal(obj)
			out = append(out, b)
		default:
			out = append(out, raw)
		}
	}
	return newSystem, out
}

type responsesItem struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ApplyResponses builds an OpenAI Responses request: `instructions` is the
// persona's prompt (the client's are dropped) and `input` (a string or an item
// list) gets the primer exchange in front as typed message items. Client
// system and developer messages are dropped, user messages cleaned, and
// non-message items (function calls and outputs, reasoning) pass through.
func (p *Persona) ApplyResponses(instructions string, input json.RawMessage) (string, json.RawMessage) {
	var items []json.RawMessage
	var asString string
	if len(input) == 0 || string(input) == "null" {
		items = nil
	} else if json.Unmarshal(input, &asString) == nil {
		m, _ := json.Marshal(responsesItem{Type: "message", Role: "user", Content: asString})
		items = []json.RawMessage{m}
	} else if json.Unmarshal(input, &items) != nil {
		items = []json.RawMessage{input}
	}

	pr := p.primer()
	u, _ := json.Marshal(responsesItem{Type: "message", Role: "user", Content: pr.User})
	a, _ := json.Marshal(responsesItem{Type: "message", Role: "assistant", Content: pr.Assistant.Content})
	out := []json.RawMessage{u, a}
	for _, raw := range items {
		role, obj := roleOf(raw)
		var typ string
		if obj != nil {
			json.Unmarshal(obj["type"], &typ)
		}
		if typ != "" && typ != "message" {
			out = append(out, raw)
			continue
		}
		switch role {
		case "system", "developer":
			continue
		case "user":
			cleaned := CleanUserContent(obj["content"])
			if cleaned == nil {
				continue
			}
			obj["content"] = cleaned
			b, _ := json.Marshal(obj)
			out = append(out, b)
		default:
			out = append(out, raw)
		}
	}
	newInput, _ := json.Marshal(out)
	return p.System, newInput
}
