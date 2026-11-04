package provider

import (
	"bytes"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"elichat/internal/auth"
)

// CliProxy relays to an OpenAI-compatible upstream (cliproxyapi) and streams the
// response bytes back untouched, flushing per chunk so SSE arrives live.
type CliProxy struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewCliProxy(baseURL, apiKey string) *CliProxy {
	return &CliProxy{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		// No overall timeout: long generations stream for minutes. Dial and
		// header timeouts still bound a dead upstream.
		Client: &http.Client{Transport: &http.Transport{
			ResponseHeaderTimeout: 120 * time.Second,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
}

var dialectPaths = map[Dialect]string{
	ChatCompletions: "/v1/chat/completions",
	Messages:        "/v1/messages",
	Responses:       "/v1/responses",
}

func (c *CliProxy) Serve(w http.ResponseWriter, r *http.Request, dialect Dialect, body []byte) {
	path, ok := dialectPaths[dialect]
	if !ok {
		path = dialectPaths[ChatCompletions]
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, c.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		auth.WriteError(w, 500, "bad upstream request")
		return
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
		slog.Warn("cliproxy upstream", "err", err)
		auth.WriteError(w, http.StatusBadGateway, "upstream unavailable")
		return
	}
	defer resp.Body.Close()

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
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			if err != io.EOF {
				slog.Debug("cliproxy stream ended", "err", err)
			}
			return
		}
	}
}
