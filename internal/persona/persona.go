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
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	BaseModel   string  `json:"base_model"`
	Provider    string  `json:"provider"`
	Description string  `json:"description"`
	Short       string  `json:"short"`
	System      string  `json:"-"`
	Primer      *Primer `json:"-"`
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
			System string  `json:"system"`
			Primer *Primer `json:"primer"`
		}
		json.Unmarshal(raw, &sys)
		p.ID, p.System, p.Primer = id, sys.System, sys.Primer
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

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Apply prepends the persona's system prompt and priming exchange to the
// client's messages. Client-supplied system messages are kept after ours.
func (p *Persona) Apply(messages []json.RawMessage) []json.RawMessage {
	out := make([]json.RawMessage, 0, len(messages)+3)
	add := func(role, content string) {
		b, _ := json.Marshal(message{Role: role, Content: content})
		out = append(out, b)
	}
	add("system", p.System)
	if p.Primer != nil && p.Primer.Assistant.Content != "" {
		add("user", p.Primer.User)
		add("assistant", p.Primer.Assistant.Content)
	}
	return append(out, messages...)
}
