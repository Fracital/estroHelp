// Package provider holds the upstreams that serve a completion once the
// persona has been applied: cliproxyapi for the Kimi tiers and the embedded
// z.ai client for Epsilon.
package provider

import (
	"bytes"
	"io"
	"net/http"

	"elichat/internal/zai"
)

// Dialect is one of the public API shapes Estro serves.
type Dialect string

const (
	ChatCompletions Dialect = "chat"      // POST /v1/chat/completions
	Messages        Dialect = "messages"  // POST /v1/messages (Anthropic)
	Responses       Dialect = "responses" // POST /v1/responses
)

// Provider writes a full response (SSE or JSON) in the given dialect for a
// request body that already names the upstream model.
type Provider interface {
	Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte)
}

type Zai struct{}

func (Zai) Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte) {
	switch dialect {
	case Messages:
		zai.ServeMessages(w, r, body)
	case Responses:
		zai.ServeResponses(w, r, body)
	default:
		zai.ServeChatCompletions(w, r, body)
	}
}

func withBody(r *http.Request, body []byte) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Body = io.NopCloser(bytes.NewReader(body))
	r2.ContentLength = int64(len(body))
	return r2
}
