package server

import (
	"net/http"
	"strconv"
	"strings"

	"elichat/internal/auth"
	"elichat/internal/ratelimit"
)

// Three tiers. Auth routes are keyed by IP and kept tight against guessing.
// Model routes are keyed by account and sized for agent loops, which fire many
// sequential requests per turn; the daily cap is the real budget. Everything
// else under /api (threads, account, admin) gets the roomiest per-account tier
// because one tool round persists several messages.
var (
	authLimit = ratelimit.FromEnv("RATE_AUTH", 10, 10)
	llmLimit  = ratelimit.FromEnv("RATE_LLM", 120, 120)
	appLimit  = ratelimit.FromEnv("RATE_APP", 300, 300)
)

const (
	plainBody     = `{"error":"too many requests, slow down"}`
	openAIBody    = `{"error":{"message":"rate limit exceeded, retry shortly","type":"rate_limit_error","code":"rate_limit_exceeded"}}`
	anthropicBody = `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit exceeded, retry shortly"}}`
)

func accountKey(r *http.Request) string {
	if u := auth.UserFrom(r.Context()); u != nil {
		return "u:" + strconv.Itoa(u.ID)
	}
	return "ip:" + auth.RequestIP(r)
}

func limits(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		var lim *ratelimit.Limiter
		var key, body string
		switch {
		// Minting a passkey challenge happens on every sign-in page load
		// (conditional UI) and cannot be brute-forced, so it takes the app tier.
		case path == "/api/auth/passkey/begin":
			lim, key, body = appLimit, "ip:"+auth.RequestIP(r), plainBody
		case strings.HasPrefix(path, "/api/auth/") && r.Method != http.MethodGet:
			lim, key, body = authLimit, "ip:"+auth.RequestIP(r), plainBody
		case strings.HasPrefix(path, "/v1/messages") && r.Method != http.MethodGet:
			lim, key, body = llmLimit, accountKey(r), anthropicBody
		case strings.HasPrefix(path, "/v1/") && r.Method != http.MethodGet:
			lim, key, body = llmLimit, accountKey(r), openAIBody
		case strings.HasPrefix(path, "/api/"):
			lim, key, body = appLimit, accountKey(r), plainBody
		}
		if lim != nil {
			if ok, wait := lim.Allow(key); !ok {
				ratelimit.Reject(w, wait, body)
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}
