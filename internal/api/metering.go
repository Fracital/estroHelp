package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"time"

	"elichat/internal/auth"
	"elichat/internal/billing"
	"elichat/internal/persona"
)

// entitlement is what a request may spend: a plan's windows, the free daily
// pool, or nothing (admins are treated as the top plan with no limit).
type entitlement struct {
	plan      billing.PlanID
	admin     bool
	free      bool
	snapshot  billing.Snapshot
	freeUsed  float64
	freeReset time.Time
}

func (e entitlement) planID() billing.PlanID {
	if e.plan == "" {
		return billing.Max
	}
	return e.plan
}

// fromWebApp is true for the site's own session-cookie requests. API clients
// (bearer tokens and keys) need a plan; the site gets the free daily pool.
func fromWebApp(r *http.Request) bool {
	return r.Header.Get("Authorization") == "" && r.Header.Get("X-Api-Key") == ""
}

func (c *Chat) entitle(ctx context.Context, u *auth.User) (entitlement, error) {
	if u.Admin {
		snap, err := billing.ReadSnapshot(ctx, c.DB, u.ID)
		return entitlement{plan: billing.Max, admin: true, snapshot: snap}, err
	}
	row, err := billing.ReadBillingRow(ctx, c.DB, u.ID)
	if err != nil {
		return entitlement{}, err
	}
	now := time.Now()
	if plan := row.ActivePlan(now); plan != "" {
		snap, err := billing.ReadSnapshot(ctx, c.DB, u.ID)
		return entitlement{plan: plan, snapshot: snap}, err
	}
	used, err := billing.FreeUsedToday(ctx, c.DB, u.ID)
	return entitlement{free: true, freeUsed: used, freeReset: billing.NextUTCMidnight(now)}, err
}

// limitBody is abulxt's usage_limit_reached detail, in the OpenAI or Anthropic envelope.
func limitBody(shape string, l billing.UsageLimit, now time.Time, windowMinutes int) []byte {
	detail := map[string]any{
		"message": l.Message(), "type": "usage_limit_reached",
		"resets_at": now.Unix() + int64(l.ResetsIn), "limit_window_minutes": windowMinutes, "window": l.Window,
		"used": int(math.Floor(l.Used)), "limit": l.Limit, "resets_in": l.ResetsIn, "model_multiplier": l.Multiplier,
	}
	var body any
	if shape == "anthropic" {
		body = map[string]any{"type": "error", "error": detail}
	} else {
		body = map[string]any{"error": detail}
	}
	b, _ := json.Marshal(body)
	return b
}

func pricingURL() string { return billing.PublicURL("https://estro.help") + "/pricing" }

// admit applies the plan preflight. On refusal it writes the response and returns false.
func (c *Chat) admit(w http.ResponseWriter, r *http.Request, shape string, model string, fail func(status int, msg, code string)) (entitlement, bool) {
	u := auth.UserFrom(r.Context())
	ent, err := c.entitle(r.Context(), u)
	if err != nil {
		slog.Error("entitlement", "err", err)
		fail(500, "billing lookup failed", "api_error")
		return ent, false
	}
	mult := billing.ModelMultiplier(model)
	h := w.Header()
	if ent.admin {
		billing.SetCodexHeaders(h, ent.planID(), ent.snapshot)
		return ent, true
	}
	if ent.free {
		if !billing.IsFreeModel(model) {
			name := model
			if p, ok := persona.Resolve(model); ok {
				name = p.Name
			}
			fail(402, name+" requires an active Estro plan.", "billing_error")
			return ent, false
		}
		if !fromWebApp(r) {
			fail(402, "An active Estro plan is required to use the API.", "billing_error")
			return ent, false
		}
		if ent.freeUsed >= billing.FreeUnitsPerDay {
			resetsIn := int(math.Ceil(time.Until(ent.freeReset).Seconds()))
			l := billing.UsageLimit{Window: "daily", Used: ent.freeUsed, Limit: billing.FreeUnitsPerDay, ResetsIn: resetsIn, Multiplier: mult}
			h.Set("Retry-After", strconv.Itoa(resetsIn))
			h.Set("x-codex-rate-limit-reached-type", "rate_limit_reached")
			h.Set("x-codex-promo-message", "Choose an Estro plan at "+pricingURL())
			h.Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(429)
			w.Write(limitBody(shape, l, time.Now(), 24*60))
			return ent, false
		}
		return ent, true
	}
	billing.SetCodexHeaders(h, ent.plan, ent.snapshot)
	if l, ok := billing.Preflight(ent.plan, mult, ent.snapshot); !ok {
		minutes := int(billing.WindowLength[billing.FiveHour].Minutes())
		if l.Window == "weekly" {
			minutes = int(billing.WindowLength[billing.Weekly].Minutes())
		}
		h.Set("Retry-After", strconv.Itoa(l.ResetsIn))
		billing.SetCodexLimitHeaders(h, ent.plan, pricingURL())
		h.Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(429)
		w.Write(limitBody(shape, l, ent.snapshot.Now, minutes))
		return ent, false
	}
	return ent, true
}

// tokenUsage keeps the largest value seen per field, since streams report usage in pieces.
type tokenUsage struct {
	input, cached, output int
	seen                  bool
}

func count(v any) (int, bool) {
	f, ok := v.(float64)
	if !ok || f < 0 || math.IsNaN(f) {
		return 0, false
	}
	return int(f), true
}

func usageBlock(v any) (tokenUsage, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return tokenUsage{}, false
	}
	out, outOK := count(m["completion_tokens"])
	if !outOK {
		out, outOK = count(m["output_tokens"])
	}
	cacheRead, readOK := count(m["cache_read_input_tokens"])
	cacheWrite, writeOK := count(m["cache_creation_input_tokens"])
	if readOK || writeOK {
		in, _ := count(m["input_tokens"])
		return tokenUsage{input: in + cacheRead + cacheWrite, cached: cacheRead, output: out, seen: true}, true
	}
	in, inOK := count(m["prompt_tokens"])
	if !inOK {
		in, inOK = count(m["input_tokens"])
	}
	if !inOK && !outOK {
		return tokenUsage{}, false
	}
	cached := 0
	for _, key := range []string{"prompt_tokens_details", "input_tokens_details"} {
		if d, ok := m[key].(map[string]any); ok {
			if c, ok := count(d["cached_tokens"]); ok {
				cached = c
				break
			}
		}
	}
	if cached == 0 {
		cached, _ = count(m["cached_tokens"])
	}
	return tokenUsage{input: in, cached: cached, output: out, seen: true}, true
}

func (t *tokenUsage) observe(line []byte) {
	if !bytes.Contains(line, []byte(`"usage"`)) {
		return
	}
	line = bytes.TrimSpace(line)
	line = bytes.TrimPrefix(line, []byte("data:"))
	line = bytes.TrimSpace(line)
	var v map[string]any
	if json.Unmarshal(line, &v) != nil {
		return
	}
	candidates := []any{v["usage"]}
	if r, ok := v["response"].(map[string]any); ok {
		candidates = append(candidates, r["usage"])
	}
	if msg, ok := v["message"].(map[string]any); ok {
		candidates = append(candidates, msg["usage"])
	}
	for _, c := range candidates {
		if u, ok := usageBlock(c); ok {
			t.seen = true
			t.input = max(t.input, u.input)
			t.cached = max(t.cached, u.cached)
			t.output = max(t.output, u.output)
			return
		}
	}
}

// meter records the finished request against the user's windows.
func (c *Chat) meter(ctx context.Context, u *auth.User, model string, usage tokenUsage, promptChars, outputBytes int) {
	in, cached, out := usage.input, usage.cached, usage.output
	if !usage.seen {
		in, cached, out = promptChars/4, 0, outputBytes/4
	}
	mult := billing.ModelMultiplier(model)
	rec := billing.UsageRecord{UserID: u.ID, Model: model, Multiplier: mult, Units: billing.UnitsFor(in, cached, out, mult), InputTokens: in, CachedTokens: cached, OutputTokens: out}
	if err := billing.RecordUsage(context.WithoutCancel(ctx), c.DB, rec); err != nil {
		slog.Warn("record usage", "err", err)
	}
}

// codexUsage answers GET /v1/usage in the Codex /wham/usage shape.
func (c *Chat) codexUsage(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	ent, err := c.entitle(r.Context(), u)
	if err != nil || ent.free {
		auth.WriteJSON(w, 200, map[string]any{"plan_type": nil, "rate_limit": nil, "credits": nil})
		return
	}
	plan := ent.planID()
	primary := billing.WindowStatus(plan, billing.FiveHour, ent.snapshot.Rows[billing.FiveHour], ent.snapshot.Now)
	secondary := billing.WindowStatus(plan, billing.Weekly, ent.snapshot.Rows[billing.Weekly], ent.snapshot.Now)
	reached := !ent.admin && (primary.Full || secondary.Full)
	var reachedType any
	if reached {
		reachedType = "rate_limit_reached"
	}
	auth.WriteJSON(w, 200, map[string]any{
		"plan_type":               plan,
		"rate_limit":              map[string]any{"allowed": !reached, "limit_reached": reached, "primary_window": billing.CodexUsageWindow(primary), "secondary_window": billing.CodexUsageWindow(secondary)},
		"credits":                 nil,
		"rate_limit_reached_type": reachedType,
	})
}
