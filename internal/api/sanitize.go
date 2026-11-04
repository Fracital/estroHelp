package api

import (
	"encoding/json"

	"elichat/internal/persona"
)

// Request fields the upstreams refuse or misapply. Mirrors abulxt's relay:
// top_p and n go unconditionally (the Kimi upstream only accepts top_p 0.95
// and the client chose them for a different model), /v1/responses rejects
// truncation and keeps no past responses, and a reasoning level the model
// does not offer is dropped rather than forwarded. "off"/"none" is forwarded
// only for models that can switch thinking off.

var responsesUnsupported = []string{"truncation", "previous_response_id"}

func sanitizeCommon(body map[string]json.RawMessage) {
	delete(body, "top_p")
	delete(body, "n")
}

// clampEffort keeps a requested reasoning level only when the model offers it.
func clampEffort(p *persona.Persona, requested string) (string, bool) {
	return p.ThinkingFor(requested)
}

func sanitizeChat(body map[string]json.RawMessage, p *persona.Persona, stream bool) {
	sanitizeCommon(body)
	if raw, ok := body["reasoning_effort"]; ok {
		var effort string
		if json.Unmarshal(raw, &effort) == nil {
			if level, ok := clampEffort(p, effort); ok {
				body["reasoning_effort"], _ = json.Marshal(level)
			} else {
				delete(body, "reasoning_effort")
			}
		} else {
			delete(body, "reasoning_effort")
		}
	}
	if stream {
		opts := map[string]json.RawMessage{}
		if raw, ok := body["stream_options"]; ok {
			json.Unmarshal(raw, &opts)
		}
		opts["include_usage"] = json.RawMessage("true")
		body["stream_options"], _ = json.Marshal(opts)
	}
}

func sanitizeResponses(body map[string]json.RawMessage, p *persona.Persona) {
	sanitizeCommon(body)
	for _, f := range responsesUnsupported {
		delete(body, f)
	}
	if raw, ok := body["reasoning"]; ok {
		var reasoning map[string]json.RawMessage
		if json.Unmarshal(raw, &reasoning) != nil {
			delete(body, "reasoning")
			return
		}
		if eff, ok := reasoning["effort"]; ok {
			var effort string
			if json.Unmarshal(eff, &effort) == nil {
				if level, ok := clampEffort(p, effort); ok {
					reasoning["effort"], _ = json.Marshal(level)
				} else {
					delete(reasoning, "effort")
				}
			} else {
				delete(reasoning, "effort")
			}
		}
		if len(reasoning) == 0 {
			delete(body, "reasoning")
		} else {
			body["reasoning"], _ = json.Marshal(reasoning)
		}
	}
}

// sanitizeMessages handles Anthropic's `thinking` switch: `{type:"disabled"}`
// is forwarded only when the model can switch thinking off; `enabled` passes.
func sanitizeMessages(body map[string]json.RawMessage, p *persona.Persona) {
	sanitizeCommon(body)
	if raw, ok := body["thinking"]; ok {
		var th struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(raw, &th) != nil {
			delete(body, "thinking")
			return
		}
		if th.Type == "disabled" {
			if _, ok := p.ThinkingFor("off"); !ok {
				delete(body, "thinking")
			}
		}
	}
}
