package server

import (
	"net/http"
	"regexp"
	"strings"
)

// Clients configured with a base URL that already ends in /v1 send /v1/v1/...;
// abulxt collapses the repeat rather than 404ing.
var doubledV1 = regexp.MustCompile(`^(?:/v1)+/`)

// Browser clients (Open WebUI, LibreChat, playgrounds) call /v1 cross-origin
// with a key, so the API answers preflights and exposes the usage headers.
const (
	corsMethods = "GET, POST, OPTIONS"
	corsHeaders = "Authorization, Content-Type, X-API-Key, HTTP-Referer, X-Title, anthropic-version, anthropic-beta"
	corsExpose  = "Retry-After, x-codex-primary-used-percent, x-codex-primary-window-minutes, x-codex-primary-reset-at, " +
		"x-codex-secondary-used-percent, x-codex-secondary-window-minutes, x-codex-secondary-reset-at, " +
		"x-codex-rate-limit-reached-type, x-codex-promo-message"
)

// v1Front normalises the /v1 path and applies CORS before auth and limits run,
// so even refusals carry the headers and preflights never need a key.
func v1Front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/v1/") {
			next.ServeHTTP(w, r)
			return
		}
		if p := doubledV1.ReplaceAllString(r.URL.Path, "/v1/"); p != r.URL.Path {
			r.URL.Path = p
			r.URL.RawPath = ""
		}
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", "*")
		h.Set("Access-Control-Allow-Methods", corsMethods)
		h.Set("Access-Control-Allow-Headers", corsHeaders)
		h.Set("Access-Control-Expose-Headers", corsExpose)
		h.Set("Access-Control-Max-Age", "86400")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
