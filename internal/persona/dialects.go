package persona

import (
	"encoding/json"
	"strings"
)

// ApplyAnthropic prepends the persona to an Anthropic Messages request. `system`
// may be absent, a string, or an array of content blocks; the persona text goes
// first in either form. The primer exchange leads the messages array.
func (p *Persona) ApplyAnthropic(system json.RawMessage, messages []json.RawMessage) (json.RawMessage, []json.RawMessage) {
	var newSystem json.RawMessage
	var asString string
	if len(system) == 0 || string(system) == "null" {
		newSystem, _ = json.Marshal(p.System)
	} else if json.Unmarshal(system, &asString) == nil {
		newSystem, _ = json.Marshal(p.System + "\n\n" + asString)
	} else {
		var blocks []json.RawMessage
		if json.Unmarshal(system, &blocks) != nil {
			blocks = nil
		}
		first, _ := json.Marshal(map[string]string{"type": "text", "text": p.System})
		newSystem, _ = json.Marshal(append([]json.RawMessage{first}, blocks...))
	}

	out := make([]json.RawMessage, 0, len(messages)+2)
	if p.Primer != nil && p.Primer.Assistant.Content != "" {
		u, _ := json.Marshal(message{Role: "user", Content: p.Primer.User})
		a, _ := json.Marshal(message{Role: "assistant", Content: p.Primer.Assistant.Content})
		out = append(out, u, a)
	}
	return newSystem, append(out, messages...)
}

// ApplyResponses prepends the persona to an OpenAI Responses request:
// `instructions` carries the system prompt and `input` (a string or an item
// list) gets the primer exchange in front.
func (p *Persona) ApplyResponses(instructions string, input json.RawMessage) (string, json.RawMessage) {
	newInstructions := p.System
	if s := strings.TrimSpace(instructions); s != "" {
		newInstructions += "\n\n" + s
	}

	var items []json.RawMessage
	var asString string
	if len(input) == 0 || string(input) == "null" {
		items = nil
	} else if json.Unmarshal(input, &asString) == nil {
		m, _ := json.Marshal(message{Role: "user", Content: asString})
		items = []json.RawMessage{m}
	} else if json.Unmarshal(input, &items) != nil {
		items = []json.RawMessage{input}
	}

	out := make([]json.RawMessage, 0, len(items)+2)
	if p.Primer != nil && p.Primer.Assistant.Content != "" {
		u, _ := json.Marshal(message{Role: "user", Content: p.Primer.User})
		a, _ := json.Marshal(message{Role: "assistant", Content: p.Primer.Assistant.Content})
		out = append(out, u, a)
	}
	newInput, _ := json.Marshal(append(out, items...))
	return newInstructions, newInput
}
