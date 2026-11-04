// Package server assembles the HTTP mux: API routes behind session auth, the
// optional captcha-extension socket, and the built Astro site for everything else.
package server

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"elichat/internal/api"
	"elichat/internal/auth"
	"elichat/internal/billing"
	"elichat/internal/wisp"
)

type Deps struct {
	Store      *auth.Store
	Auth       *auth.Handlers
	Admin      *auth.Admin
	Chat       *api.Chat
	Billing    *billing.Handlers
	ExtHandler http.Handler
	StaticDir  string
	// SelfHosts are host:port pairs the sandbox relay may reach even though
	// they resolve to loopback/private addresses: Estro's own API.
	SelfHosts []string
}

func New(d Deps) http.Handler {
	mux := http.NewServeMux()
	d.Auth.Register(mux)
	d.Auth.RegisterAPIKeys(mux)
	d.Auth.RegisterPasskeys(mux)
	d.Auth.RegisterTOTP(mux)
	d.Admin.Register(mux)
	if d.Billing != nil {
		d.Billing.Register(mux)
	}
	d.Chat.Register(mux)
	d.Chat.RegisterThreads(mux)
	d.Chat.RegisterDialects(mux)
	if d.ExtHandler != nil {
		mux.Handle("/zai/ext", d.ExtHandler)
	}
	mux.HandleFunc("GET /api/health", func(w http.ResponseWriter, r *http.Request) {
		auth.WriteJSON(w, 200, map[string]any{"ok": true})
	})
	relay := wisp.New()
	relay.AllowHosts = d.SelfHosts
	mux.Handle("/wisp", relay)
	mux.Handle("/", newStatic(d.StaticDir))
	return logRequests(d.Store.Authenticate(limits(mux)))
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack lets WebSocket upgrades (the /wisp relay) take over the connection.
func (s *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := s.ResponseWriter.(http.Hijacker); ok {
		s.status = http.StatusSwitchingProtocols
		return h.Hijack()
	}
	return nil, nil, errors.New("response does not support hijacking")
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sw := &statusWriter{ResponseWriter: w, status: 200}
		start := time.Now()
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/api/health" {
			return
		}
		slog.Debug("http", "method", r.Method, "path", r.URL.Path, "status", sw.status, "ms", time.Since(start).Milliseconds())
	})
}
