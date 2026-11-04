package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/persona"
	"elichat/internal/provider"
)

// The Anthropic Messages and OpenAI Responses dialects share the chat
// completions gate (auth, ban, verification, daily cap) and metering; only the
// persona application differs per body shape.

func (c *Chat) RegisterDialects(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages", auth.RequireUser(c.messages))
	mux.HandleFunc("POST /v1/responses", auth.RequireUser(c.responses))
}

func anthropicError(w http.ResponseWriter, status int, msg, kind string) {
	auth.WriteJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": kind, "message": msg}})
}

// gate runs the shared admission checks and returns the persona and provider,
// or writes the error in the caller's dialect and returns ok=false.
func (c *Chat) gate(w http.ResponseWriter, r *http.Request, model string, fail func(status int, msg, code string)) (*persona.Persona, provider.Provider, bool) {
	u := auth.UserFrom(r.Context())
	p, ok := persona.Resolve(model)
	if !ok {
		fail(404, "unknown model "+model+"; use kali-gamma, kali-beta or kali-epsilon", "not_found_error")
		return nil, nil, false
	}
	prov, ok := c.Providers[p.Provider]
	if !ok {
		fail(503, p.Name+" is not available right now", "overloaded_error")
		return nil, nil, false
	}
	if ban, _ := c.Store.ActiveBan(r.Context(), u.ID); ban != nil {
		fail(403, "account banned: "+ban.Reason, "permission_error")
		return nil, nil, false
	}
	if c.RequireVerifiedEmail && !u.EmailVerified && !u.Admin {
		fail(403, "verify your email address to chat", "permission_error")
		return nil, nil, false
	}
	if !u.Admin && !u.Sandbox && c.DailyRequestLimit > 0 {
		var today int
		c.DB.QueryRow(r.Context(), `SELECT coalesce(sum(requests),0) FROM ai_usage WHERE user_id=$1 AND day=CURRENT_DATE`, u.ID).Scan(&today)
		if today >= c.DailyRequestLimit {
			fail(429, "daily request limit reached", "rate_limit_error")
			return nil, nil, false
		}
	}
	return p, prov, true
}

func (c *Chat) messages(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	if err != nil {
		anthropicError(w, 400, "could not read body", "invalid_request_error")
		return
	}
	var req struct {
		Model    string            `json:"model"`
		System   json.RawMessage   `json:"system"`
		Messages []json.RawMessage `json:"messages"`
		Stream   bool              `json:"stream"`
	}
	var passthrough map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil || json.Unmarshal(raw, &passthrough) != nil {
		anthropicError(w, 400, "invalid JSON body", "invalid_request_error")
		return
	}
	if len(req.Messages) == 0 {
		anthropicError(w, 400, "messages is required", "invalid_request_error")
		return
	}
	p, prov, ok := c.gate(w, r, req.Model, func(s int, m, k string) { anthropicError(w, s, m, k) })
	if !ok {
		return
	}
	if _, ok := c.admit(w, r, "anthropic", p.ID, func(s int, m, k string) { anthropicError(w, s, m, k) }); !ok {
		return
	}
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m)
	}
	system, msgs := p.ApplyAnthropic(req.System, req.Messages)
	passthrough["system"] = system
	passthrough["messages"], _ = json.Marshal(msgs)
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	c.recordUsage(r.Context(), u, p.ID, promptChars)
	slog.Info("chat", "dialect", "anthropic", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream)
	rw := newModelRewriter(w, p.ID)
	prov.Serve(rw, r, provider.Messages, body)
	rw.Close()
	c.meter(r.Context(), u, p.ID, rw.usage, promptChars, rw.bytesOut)
}

func (c *Chat) responses(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	if err != nil {
		openAIError(w, 400, "could not read body", "bad_request")
		return
	}
	var req struct {
		Model        string          `json:"model"`
		Instructions string          `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		Stream       bool            `json:"stream"`
	}
	var passthrough map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil || json.Unmarshal(raw, &passthrough) != nil {
		openAIError(w, 400, "invalid JSON body", "bad_request")
		return
	}
	if len(req.Input) == 0 || string(req.Input) == "null" {
		openAIError(w, 400, "input is required", "bad_request")
		return
	}
	p, prov, ok := c.gate(w, r, req.Model, func(s int, m, k string) { openAIError(w, s, m, k) })
	if !ok {
		return
	}
	if _, ok := c.admit(w, r, "openai", p.ID, func(s int, m, k string) { openAIError(w, s, m, k) }); !ok {
		return
	}
	instructions, input := p.ApplyResponses(req.Instructions, req.Input)
	passthrough["instructions"], _ = json.Marshal(instructions)
	passthrough["input"] = input
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	c.recordUsage(r.Context(), u, p.ID, len(req.Input))
	slog.Info("chat", "dialect", "responses", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream)
	rw := newModelRewriter(w, p.ID)
	prov.Serve(rw, r, provider.Responses, body)
	rw.Close()
	c.meter(r.Context(), u, p.ID, rw.usage, len(req.Input), rw.bytesOut)
}
