// Package api serves the OpenAI-compatible /v1 surface and the chat app's own
// /api routes (threads, models, usage).
package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"

	"elichat/internal/auth"
	"elichat/internal/billing"
	"elichat/internal/db"
	"elichat/internal/persona"
	"elichat/internal/provider"
	"elichat/internal/relay"
)

// bodyLimit matches abulxt: agent loops send whole repositories as context.
const bodyLimit = 32 << 20

type Chat struct {
	DB                   *db.DB
	Store                *auth.Store
	Providers            map[string]provider.Provider
	DailyRequestLimit    int
	RequireVerifiedEmail bool
}

func (c *Chat) Register(mux *http.ServeMux) {
	chainDB = c.DB
	mux.HandleFunc("POST /v1/chat/completions", auth.RequireUser(c.completions))
	mux.HandleFunc("POST /v1/completions", c.passthrough(provider.Completions))
	mux.HandleFunc("POST /v1/embeddings", c.passthrough(provider.Embeddings))
	mux.HandleFunc("GET /v1/models", c.listModelsOpenAI)
	mux.HandleFunc("GET /api/models", c.listModels)
	mux.HandleFunc("GET /api/usage", auth.RequireUser(c.usage))
	mux.HandleFunc("GET /v1/usage", auth.RequireUser(c.codexUsage))
}

type chatRequest struct {
	Model    string            `json:"model"`
	Messages []json.RawMessage `json:"messages"`
	Stream   bool              `json:"stream"`
}

// openAIType is the OpenAI-shape error type for a status; codes carry the detail.
func openAIType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 402:
		return "billing_error"
	case status == 403:
		return "permission_error"
	case status == 429:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

func openAIError(w http.ResponseWriter, status int, msg, code string) {
	relay.WriteError(w, relay.OpenAI, status, openAIType(status), msg, map[string]any{"code": code})
}

func anthropicError(w http.ResponseWriter, status int, msg, kind string) {
	relay.WriteError(w, relay.Anthropic, status, kind, msg, nil)
}

// readBody reads up to the body limit and answers 413 in the caller's dialect past it.
func readBody(w http.ResponseWriter, r *http.Request, shape relay.Shape) ([]byte, bool) {
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit+1))
	if err != nil {
		relay.WriteError(w, shape, 400, "invalid_request_error", "could not read body", nil)
		return nil, false
	}
	if len(raw) > bodyLimit {
		relay.WriteError(w, shape, 413, "invalid_request_error", "request body too large; the limit is 32 MB", map[string]any{"code": "body_too_large"})
		return nil, false
	}
	return raw, true
}

func (c *Chat) completions(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, ok := readBody(w, r, relay.OpenAI)
	if !ok {
		return
	}
	var req chatRequest
	var passthrough map[string]json.RawMessage
	if err := json.Unmarshal(raw, &req); err != nil || json.Unmarshal(raw, &passthrough) != nil {
		openAIError(w, 400, "invalid JSON body", "bad_request")
		return
	}
	if len(req.Messages) == 0 {
		openAIError(w, 400, "messages is required", "bad_request")
		return
	}
	p, prov, ok := c.gate(w, r, req.Model, func(s int, m, k string) { openAIError(w, s, m, k) })
	if !ok {
		return
	}
	if _, ok := c.admit(w, r, "openai", p.ID, func(s int, m, k string) { openAIError(w, s, m, k) }); !ok {
		return
	}

	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m)
	}
	sanitizeChat(passthrough, p, req.Stream)
	if repaired, n := repairChatOrphans(req.Messages); n > 0 {
		slog.Warn("chat: folded orphaned tool results into user turns", "user", u.Username, "count", n)
		req.Messages = repaired
	}
	applied, _ := json.Marshal(p.Apply(req.Messages))
	passthrough["messages"] = applied
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	slog.Info("chat", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream, "sandbox", u.Sandbox)
	rw := newModelRewriter(w, p.ID, relay.OpenAI, false)
	status := prov.Serve(rw, r, provider.ChatCompletions, body)
	rw.Close()
	c.finish(r.Context(), u, p.ID, status, rw, promptChars)
}

// finish records and meters a request once the upstream answered it. Refused
// requests (4xx, 5xx, transport failures) cost the caller nothing, as in abulxt.
func (c *Chat) finish(ctx context.Context, u *auth.User, model string, status int, rw *modelRewriter, promptChars int) {
	if status < 200 || status >= 300 {
		return
	}
	c.recordUsage(ctx, u, model, promptChars)
	c.meter(ctx, u, model, rw.usage, promptChars, rw.bytesOut)
}

func (c *Chat) recordUsage(ctx context.Context, u *auth.User, model string, promptChars int) {
	_, err := c.DB.Exec(context.WithoutCancel(ctx), `INSERT INTO ai_usage (user_id, day, model, requests, prompt_chars, sandbox) VALUES ($1, CURRENT_DATE, $2, 1, $3, $4)
		ON CONFLICT (user_id, day, model) DO UPDATE SET requests = ai_usage.requests + 1, prompt_chars = ai_usage.prompt_chars + EXCLUDED.prompt_chars`,
		u.ID, model, promptChars, u.Sandbox)
	if err != nil {
		slog.Warn("record usage", "err", err)
	}
}

func (c *Chat) usage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	var today int
	c.DB.QueryRow(r.Context(), `SELECT coalesce(sum(requests),0) FROM ai_usage WHERE user_id=$1 AND day=CURRENT_DATE`, u.ID).Scan(&today)
	limit := c.DailyRequestLimit
	if u.Admin || u.Sandbox {
		limit = 0
	}
	auth.WriteJSON(w, 200, map[string]any{"requestsToday": today, "dailyLimit": limit})
}

func (c *Chat) listModels(w http.ResponseWriter, r *http.Request) {
	type model struct {
		*persona.Persona
		Available    bool `json:"available"`
		RequiresPlan bool `json:"requiresPlan"`
	}
	out := []model{}
	for _, p := range persona.All() {
		_, ok := c.Providers[p.Provider]
		out = append(out, model{p, ok, !billing.IsFreeModel(p.ID)})
	}
	auth.WriteJSON(w, 200, map[string]any{"models": out})
}
