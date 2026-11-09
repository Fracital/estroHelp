package api

import (
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/billing"
	"elichat/internal/persona"
)

// modelRecord is the OpenRouter-style record abulxt serves from /v1/models:
// the OpenAI list shape plus the limits and capabilities IDE clients read.
// id is what the record claims to be — the persona id for brand rows, the
// alias spelling for alias rows.
func modelRecord(p *persona.Persona, id string) map[string]any {
	params := []string{"max_tokens", "stream", "stop", "temperature", "top_p"}
	if len(p.Levels) > 0 {
		params = append(params, "reasoning_effort")
	}
	if p.ToolUse {
		params = append(params, "tools", "tool_choice")
	}
	inputs := []string{"text"}
	if p.Vision {
		inputs = append(inputs, "image")
	}
	mult := billing.ModelMultiplier(p.ID)
	levels := p.Levels
	if levels == nil {
		levels = []string{}
	}
	return map[string]any{
		"id": id, "object": "model", "created": 0, "owned_by": "estro",
		"name": p.Name, "description": p.Description, "short": p.Short,
		"context_length": p.ContextWindow, "max_completion_tokens": p.MaxOutput,
		"architecture":         map[string]any{"input_modalities": inputs, "output_modalities": []string{"text"}},
		"pricing":              map[string]any{"prompt": "0", "completion": "0", "input_cache_read": "0"},
		"top_provider":         map[string]any{"context_length": p.ContextWindow, "max_completion_tokens": p.MaxOutput},
		"supported_parameters": params,
		"reasoning":            p.Reasoning,
		"thinking_levels":      levels,
		"thinking_off":         p.ThinkingOff,
		"tool_use":             p.ToolUse,
		"vision":               p.Vision,
		"requires_plan":        !billing.IsFreeModel(p.ID),
		"multiplier":           mult,
		"billing":              map[string]any{"multiplier": mult, "units_per_1000_tokens": billing.UnitsFor(1000, 0, 0, mult)},
	}
}

// listModelsOpenAI answers GET /v1/models. A keyed caller without a plan sees
// only the free models, since those are all its key can use.
func (c *Chat) listModelsOpenAI(w http.ResponseWriter, r *http.Request) {
	freeOnly := false
	if u := auth.UserFrom(r.Context()); u != nil && !fromWebApp(r) {
		if ent, err := c.entitle(r.Context(), u); err == nil && ent.free {
			freeOnly = true
		}
	}
	data := []map[string]any{}
	for _, p := range persona.All() {
		if freeOnly && !billing.IsFreeModel(p.ID) {
			continue
		}
		data = append(data, modelRecord(p, p.ID))
	}
	auth.WriteJSON(w, 200, map[string]any{"object": "list", "data": data})
}
