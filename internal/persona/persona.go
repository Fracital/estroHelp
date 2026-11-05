// Package persona maps the public Kali model names to an upstream base model plus
// the system prompt and priming exchange that give each tier its character.
package persona

import (
	"embed"
	"encoding/json"
	"fmt"
	"strings"
)

//go:embed *.json
var files embed.FS

type Primer struct {
	User      string `json:"user"`
	Assistant struct {
		Content   string `json:"content"`
		Reasoning string `json:"reasoning"`
	} `json:"assistant"`
}

type Persona struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	BaseModel   string `json:"base_model"`
	Provider    string `json:"provider"`
	Description string `json:"description"`
	Short       string `json:"short"`
	// Capabilities and limits, served to clients and the integration snippets.
	ContextWindow int      `json:"context_window"`
	MaxOutput     int      `json:"max_output"`
	ToolUse       bool     `json:"tool_use"`
	Vision        bool     `json:"vision"`
	Reasoning     bool     `json:"reasoning"`
	Levels        []string `json:"levels"`
	ThinkingOff   bool     `json:"thinking_off"`
	System        string   `json:"-"`
	Primer        *Primer  `json:"-"`
	Primers       []Primer `json:"primers"`
}

// Order is the public display order.
var Order = []string{"kali-gamma", "kali-beta", "kali-epsilon"}

var personas = map[string]*Persona{}

func init() {
	for _, id := range Order {
		raw, err := files.ReadFile(id + ".json")
		if err != nil {
			panic(fmt.Sprintf("persona %s: %v", id, err))
		}
		var p Persona
		if err := json.Unmarshal(raw, &p); err != nil {
			panic(fmt.Sprintf("persona %s: %v", id, err))
		}
		var sys struct {
			System  string   `json:"system"`
			Primer  *Primer  `json:"primer"`
			Primers []Primer `json:"primers"`
		}
		json.Unmarshal(raw, &sys)
		p.ID, p.System, p.Primer, p.Primers = id, sys.System, sys.Primer, sys.Primers
		if p.BaseModel == "" || p.System == "" || p.Provider == "" {
			panic("persona " + id + ": base_model, provider and system are required")
		}
		personas[id] = &p
	}
}

func All() []*Persona {
	out := make([]*Persona, 0, len(Order))
	for _, id := range Order {
		out = append(out, personas[id])
	}
	return out
}

// Resolve accepts the persona id or its display name, case-insensitively.
func Resolve(model string) (*Persona, bool) {
	m := strings.ToLower(strings.TrimSpace(model))
	if p, ok := personas[m]; ok {
		return p, true
	}
	m = strings.ReplaceAll(m, " ", "-")
	p, ok := personas[m]
	return p, ok
}

// ThinkingFor decides what reasoning level to forward for a client's request.
// "off" and "none" are forwarded only when the model can switch thinking off;
// any other level is kept only when the model lists it. ok=false means the
// field is dropped so the upstream picks its default.
func (p *Persona) ThinkingFor(requested string) (string, bool) {
	r := strings.ToLower(strings.TrimSpace(requested))
	if r == "off" || r == "none" {
		if p.ThinkingOff {
			return "none", true
		}
		return "", false
	}
	for _, l := range p.Levels {
		if l == r {
			return l, true
		}
	}
	return "", false
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// primer is the persona's priming exchange, with the system prompt as its
// request when the persona names none, so every request carries one.
func (p *Persona) primer() Primer {
	if p.Primer != nil && p.Primer.Assistant.Content != "" {
		return *p.Primer
	}
	var pr Primer
	pr.User = p.System
	pr.Assistant.Content = "Understood. I'll follow these instructions."
	return pr
}

// primers returns the persona's priming exchanges: the multi-turn "primers"
// list when present (weaker base models need to SEE the persona behave across
// several exchanges, not one), else the single primer, else a system-prompt
// echo so every request carries one.
func (p *Persona) primers() []Primer {
	if len(p.Primers) > 0 {
		return p.Primers
	}
	return []Primer{p.primer()}
}

// primerMessages renders every primer exchange as chat messages; assistant
// turns carry reasoning_content when the primer has reasoning.
func (p *Persona) primerMessages(withReasoning bool) []json.RawMessage {
	var out []json.RawMessage
	for _, pr := range p.primers() {
		u, _ := json.Marshal(message{Role: "user", Content: pr.User})
		a := map[string]any{"role": "assistant", "content": pr.Assistant.Content}
		if withReasoning && pr.Assistant.Reasoning != "" {
			a["reasoning_content"] = pr.Assistant.Reasoning
		}
		ab, _ := json.Marshal(a)
		out = append(out, u, ab)
	}
	return out
}

// roleOf reads a message's role without decoding the rest.
func roleOf(raw json.RawMessage) (string, map[string]json.RawMessage) {
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return "", nil
	}
	var role string
	json.Unmarshal(obj["role"], &role)
	return role, obj
}

// Apply builds the chat message list: the persona's prompt is the only system
// message, followed by its primer. Client system and developer turns are
// dropped (the persona replaces them), user turns are cleaned of harness text
// and dropped when nothing remains, assistant and tool turns pass through.
func (p *Persona) Apply(messages []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(messages)+3)
	sys, _ := json.Marshal(message{Role: "system", Content: p.System})
	out = append(out, sys)
	out = append(out, p.primerMessages(true)...)
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
	return out
}
