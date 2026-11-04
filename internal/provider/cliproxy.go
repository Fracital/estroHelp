package provider

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"elichat/internal/relay"
)

// CliProxy relays to an OpenAI-compatible upstream (cliproxyapi) and streams
// the response bytes back, flushing per chunk so SSE arrives live. Upstream
// failures never reach the client raw: they are mapped into the dialect's own
// error envelope with vendor wording scrubbed.
type CliProxy struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

// Long generations with a large prompt can sit for many minutes before the
// first byte; abulxt waits 20 minutes for headers.
const headerTimeout = 1200 * time.Second

// errorTextMax caps how much of an upstream error body is read for mapping.
const errorTextMax = 64 << 10

func NewCliProxy(baseURL, apiKey string) *CliProxy {
	return &CliProxy{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// No overall timeout: long generations stream for minutes. Dial and
		// header timeouts still bound a dead upstream.
		Client: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: headerTimeout,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
}

var dialectPaths = map[Dialect]string{
	ChatCompletions: "/v1/chat/completions",
	Messages:        "/v1/messages",
	Responses:       "/v1/responses",
	Completions:     "/v1/completions",
	Embeddings:      "/v1/embeddings",
}

func wantsStream(body []byte) bool {
	var v struct {
		Stream bool `json:"stream"`
	}
	return json.Unmarshal(body, &v) == nil && v.Stream
}

func (c *CliProxy) Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte) int {
	shape := dialect.Shape()
	responsesStream := dialect == Responses && wantsStream(body)
	path, ok := dialectPaths[dialect]
	if !ok {
		path = dialectPaths[ChatCompletions]
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		relay.WriteTransportFailure(w, shape)
		return 0
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Accept", "text/event-stream, application/json")
	if dialect == Messages {
		req.Header.Set("x-api-key", c.APIKey)
		version := r.Header.Get("anthropic-version")
		if version == "" {
			version = "2023-06-01"
		}
		req.Header.Set("anthropic-version", version)
		if beta := r.Header.Get("anthropic-beta"); beta != "" {
			req.Header.Set("anthropic-beta", beta)
		}
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		if r.Context().Err() != nil {
			return 0
		}
		slog.Warn("cliproxy upstream", "err", err)
		relay.WriteTransportFailure(w, shape)
		return 0
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		text, _ := io.ReadAll(io.LimitReader(resp.Body, errorTextMax))
		slog.Warn("cliproxy upstream error", "status", resp.StatusCode, "dialect", dialect, "body", truncate(string(text), 400))
		relay.WriteUpstreamFailure(w, shape, responsesStream, resp.StatusCode, text, resp.Header)
		return resp.StatusCode
	}

	ct := resp.Header.Get("Content-Type")
	if ct == "" {
		ct = "application/json"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)

	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 16*1024)
	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return resp.StatusCode
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Debug("cliproxy stream ended", "err", err)
			}
			return resp.StatusCode
		}
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
