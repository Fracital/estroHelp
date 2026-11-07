package persona

import "strings"

// Aliases maps upstream base model names back to their public persona, so API
// clients can address "kimi-k3.1-pro" or "kimi-k3" directly — the OpenRouter
// and Moonshot convention — instead of learning the Kali brand names.
var Aliases = map[string]string{
	"kimi-k3.1-pro": "kali-gamma",
	"kimi-k3":       "kali-gamma",
	"kimi-k2.8":     "kali-beta",
}

// AliasRow is an Aliases entry resolved to its persona, with Display carrying
// the caller-facing catalog id. Rows are computed at lookup time rather than
// registered as map entries, so they never leak into All()'s brand listing.
type AliasRow struct {
	*Persona
	Display string
}

func AliasRows() []AliasRow {
	ids := []string{"kimi-k3.1-pro", "kimi-k3"}
	out := make([]AliasRow, 0, len(ids))
	for _, id := range ids {
		if target, ok := personas[Aliases[id]]; ok {
			out = append(out, AliasRow{target, id})
		}
	}
	return out
}

func resolveAlias(m string) (*Persona, bool) {
	id, ok := Aliases[m]
	if !ok {
		return nil, false
	}
	p, ok := personas[id]
	return p, ok
}

// RequestedID echoes the caller's model spelling for the response envelope, so
// a client that asked for an alias sees that alias answered back. Falls back to
// the canonical id for brand names and garbage.
func (p *Persona) RequestedID(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	m = strings.ReplaceAll(m, " ", "-")
	if m == p.ID {
		return m
	}
	if target, ok := resolveAlias(m); ok && target == p {
		return m
	}
	return p.ID
}
