// Package provider holds the upstreams that serve a completion once the
// persona has been applied: cliproxyapi for the Kimi tiers and the embedded
// z.ai client for Epsilon.
package provider

import (
	"bytes"
	"io"
	"net/http"

	"elichat/internal/relay"
	"elichat/internal/zai"
)

// Dialect is one of the public API shapes Estro serves.
type Dialect string

const (
	ChatCompletions Dialect = "chat"        // POST /v1/chat/completions
	Messages        Dialect = "messages"    // POST /v1/messages (Anthropic)
	Responses       Dialect = "responses"   // POST /v1/responses
	Completions     Dialect = "completions" // POST /v1/completions (legacy)
	Embeddings      Dialect = "embeddings"  // POST /v1/embeddings
)

// Shape is the error dialect a Dialect's clients expect.
func (d Dialect) Shape() relay.Shape {
	if d == Messages {
		return relay.Anthropic
	}
	return relay.OpenAI
}

// Provider writes a full response (SSE or JSON) in the given dialect for a
// request body that already names the upstream model. It returns the status
// it answered with, so callers meter only completed requests; 0 means no
// upstream response arrived.
type Provider interface {
	Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte) int
}

type Zai struct{}

func (Zai) Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte) int {
	sw := &statusWriter{ResponseWriter: w}
	switch dialect {
	case Messages:
		zai.ServeMessages(sw, r, body)
	case Responses:
		zai.ServeResponses(sw, r, body)
	case Completions, Embeddings:
		relay.WriteError(w, relay.OpenAI, 400, "invalid_request_error", "this model does not serve /v1/"+string(dialect), map[string]any{"code": "unsupported_endpoint"})
		return 400
	default:
		zai.ServeChatCompletions(sw, r, body)
	}
	return sw.status()
}

// statusWriter records the status an upstream handler answered with.
type statusWriter struct {
	http.ResponseWriter
	code int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.code == 0 {
		s.code = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(p []byte) (int, error) {
	if s.code == 0 {
		s.code = 200
	}
	return s.ResponseWriter.Write(p)
}

func (s *statusWriter) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusWriter) status() int { return s.code }

func withBody(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	return r2
}
