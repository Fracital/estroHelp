package api

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/persona"
	"elichat/internal/provider"
	"elichat/internal/relay"
)

// The Anthropic Messages and OpenAI Responses dialects share the chat
// completions gate (auth, ban, verification, daily cap) and metering; only the
// persona application differs per body shape.

func (c *Chat) RegisterDialects(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/messages", auth.RequireUser(c.messages))
	mux.HandleFunc("POST /v1/responses", auth.RequireUser(c.responses))
}

// gate runs the shared admission checks and returns the persona and provider,
// or writes the error in the caller's dialect and returns ok=false.
func (c *Chat) gate(w http.ResponseWriter, r *http.Request, model string, fail func(status int, msg, code string)) (*persona.Persona, provider.Provider, bool) {
	u := auth.UserFrom(r.Context())
	p, ok := persona.Resolve(model)
	if !ok {
		fail(404, "unknown model "+model+"; use kali-gamma, kali-beta or kali-epsilon", "model_not_found")
		return nil, nil, false
	}
	prov, ok := c.Providers[p.Provider]
	if !ok {
		fail(503, p.Name+" is not available right now", "model_unavailable")
		return nil, nil, false
	}
	if ban, _ := c.Store.ActiveBan(r.Context(), u.ID); ban != nil {
		fail(403, "account banned: "+ban.Reason, "banned")
		return nil, nil, false
	}
	if c.RequireVerifiedEmail && !u.EmailVerified && !u.Admin {
		fail(403, "verify your email address to chat", "email_unverified")
		return nil, nil, false
	}
	if !u.Admin && !u.Sandbox && c.DailyRequestLimit > 0 {
		var today int
		c.DB.QueryRow(r.Context(), `SELECT coalesce(sum(requests),0) FROM ai_usage WHERE user_id=$1 AND day=CURRENT_DATE`, u.ID).Scan(&today)
		if today >= c.DailyRequestLimit {
			fail(429, "daily request limit reached", "rate_limit_exceeded")
			return nil, nil, false
		}
	}
	return p, prov, true
}

// anthropicFail adapts gate's (status, message, code) to Anthropic's typed envelope.
func anthropicFail(w http.ResponseWriter) func(int, string, string) {
	return func(s int, m, _ string) { anthropicError(w, s, m, relay.AnthropicErrorType(s)) }
}

func (c *Chat) messages(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, ok := readBody(w, r, relay.Anthropic)
	if !ok {
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
	p, prov, ok := c.gate(w, r, req.Model, anthropicFail(w))
	if !ok {
		return
	}
	if _, ok := c.admit(w, r, "anthropic", p.ID, anthropicFail(w)); !ok {
		return
	}
	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m)
	}
	sanitizeMessages(passthrough, p)
	system, msgs := p.ApplyAnthropic(req.System, req.Messages)
	passthrough["system"] = system
	passthrough["messages"], _ = json.Marshal(msgs)
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	slog.Info("chat", "dialect", "anthropic", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream)
	rw := newModelRewriter(w, p.ID, relay.Anthropic, false)
	status := prov.Serve(rw, r, provider.Messages, body)
	rw.Close()
	c.finish(r.Context(), u, p.ID, status, rw, promptChars)
}

func (c *Chat) responses(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, ok := readBody(w, r, relay.OpenAI)
	if !ok {
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
	// A request that continues a previous response gets that response's items
	// in front of its own input; the upstream keeps no history of its own.
	clientInput := req.Input
	if raw, ok := passthrough["previous_response_id"]; ok {
		var prev string
		if json.Unmarshal(raw, &prev) == nil && prev != "" {
			if chained := chainInput(u.ID, prev, req.Input); chained != nil {
				clientInput, _ = json.Marshal(chained)
			} else {
				slog.Warn("responses: previous_response_id not kept; sending input without history", "user", u.Username, "kept", storedResponseCount())
			}
		}
	}
	if repaired, n := repairResponsesOrphans(inputItems(clientInput)); n > 0 {
		slog.Warn("responses: folded orphaned tool results into user turns", "user", u.Username, "count", n)
		clientInput, _ = json.Marshal(repaired)
	}
	sanitizeResponses(passthrough, p)
	instructions, input := p.ApplyResponses(req.Instructions, clientInput)
	passthrough["instructions"], _ = json.Marshal(instructions)
	passthrough["input"] = input
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	slog.Info("chat", "dialect", "responses", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream)
	rec := &responseRecorder{}
	rw := newModelRewriter(w, p.ID, relay.OpenAI, true)
	rw.onLine = rec.observe
	status := prov.Serve(rw, r, provider.Responses, body)
	rw.Close()
	if id, output, ok := rec.result(); ok {
		rememberResponse(u.ID, id, inputItems(clientInput), output)
	}
	c.finish(r.Context(), u, p.ID, status, rw, len(req.Input))
}
