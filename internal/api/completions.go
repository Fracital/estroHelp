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
)

const bodyLimit = 24 << 20

type Chat struct {
	DB                   *db.DB
	Store                *auth.Store
	Providers            map[string]provider.Provider
	DailyRequestLimit    int
	RequireVerifiedEmail bool
}

func (c *Chat) Register(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/chat/completions", auth.RequireUser(c.completions))
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

func openAIError(w http.ResponseWriter, status int, msg, code string) {
	auth.WriteJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "invalid_request_error", "code": code}})
}

func (c *Chat) completions(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	raw, err := io.ReadAll(io.LimitReader(r.Body, bodyLimit))
	if err != nil {
		openAIError(w, 400, "could not read body", "bad_request")
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
	p, ok := persona.Resolve(req.Model)
	if !ok {
		openAIError(w, 404, "unknown model "+req.Model+"; use kali-gamma, kali-beta or kali-epsilon", "model_not_found")
		return
	}
	prov, ok := c.Providers[p.Provider]
	if !ok {
		openAIError(w, 503, p.Name+" is not available right now", "model_unavailable")
		return
	}

	if ban, _ := c.Store.ActiveBan(r.Context(), u.ID); ban != nil {
		openAIError(w, 403, "account banned: "+ban.Reason, "banned")
		return
	}
	if c.RequireVerifiedEmail && !u.EmailVerified && !u.Admin {
		openAIError(w, 403, "verify your email address to chat", "email_unverified")
		return
	}
	if !u.Admin && !u.Sandbox && c.DailyRequestLimit > 0 {
		var today int
		c.DB.QueryRow(r.Context(), `SELECT coalesce(sum(requests),0) FROM ai_usage WHERE user_id=$1 AND day=CURRENT_DATE`, u.ID).Scan(&today)
		if today >= c.DailyRequestLimit {
			openAIError(w, 429, "daily request limit reached", "rate_limit_exceeded")
			return
		}
	}
	if _, ok := c.admit(w, r, "openai", p.ID, func(s int, m, k string) { openAIError(w, s, m, k) }); !ok {
		return
	}

	promptChars := 0
	for _, m := range req.Messages {
		promptChars += len(m)
	}
	sanitizeChat(passthrough, p, req.Stream)
	applied, _ := json.Marshal(p.Apply(req.Messages))
	passthrough["messages"] = applied
	passthrough["model"], _ = json.Marshal(p.BaseModel)
	body, _ := json.Marshal(passthrough)

	c.recordUsage(r.Context(), u, p.ID, promptChars)
	slog.Info("chat", "user", u.Username, "model", p.ID, "upstream", p.BaseModel, "stream", req.Stream, "sandbox", u.Sandbox)
	rw := newModelRewriter(w, p.ID)
	prov.Serve(rw, r, provider.ChatCompletions, body)
	rw.Close()
	c.meter(r.Context(), u, p.ID, rw.usage, promptChars, rw.bytesOut)
}

func (c *Chat) recordUsage(ctx context.Context, u *auth.User, model string, promptChars int) {
	_, err := c.DB.Exec(ctx, `INSERT INTO ai_usage (user_id, day, model, requests, prompt_chars, sandbox) VALUES ($1, CURRENT_DATE, $2, 1, $3, $4)
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

func (c *Chat) listModelsOpenAI(w http.ResponseWriter, r *http.Request) {
	data := []map[string]any{}
	for _, p := range persona.All() {
		data = append(data, map[string]any{
			"id": p.ID, "object": "model", "owned_by": "estro", "name": p.Name, "description": p.Description, "short": p.Short,
			"requires_plan":  !billing.IsFreeModel(p.ID),
			"context_window": p.ContextWindow, "max_output": p.MaxOutput, "tool_use": p.ToolUse, "vision": p.Vision,
			"reasoning": p.Reasoning, "levels": p.Levels, "thinking_off": p.ThinkingOff,
		})
	}
	auth.WriteJSON(w, 200, map[string]any{"object": "list", "data": data})
}
