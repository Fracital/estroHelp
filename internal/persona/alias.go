package persona

// Aliases maps upstream base model names back to their public persona so API
// clients can address "kimi-k3.1-pro" or "kimi-k3" directly, like the OpenRouter
// and Moonshot conventions, instead of learning the Kali brand names.
var Aliases = map[string]string{
	"kimi-k3.1-pro": "kali-gamma",
	"kimi-k3":       "kali-gamma",
	"kimi-k2.8":     "kali-beta",
}

// registerAliases must run after personas is populated, so it is called from
// persona.go's init rather than relying on file-order init sequencing.
func registerAliases() {
	for alias, id := range Aliases {
		if target, ok := personas[id]; ok {
			personas[alias] = target
		}
	}
}
