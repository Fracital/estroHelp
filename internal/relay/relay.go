// Package relay holds what the /v1 dialects share: error envelopes, the
// context-window overflow replies clients recover from, and the scrubbing of
// upstream wording so no base model or vendor is ever named to a client.
// Ported from abulxt's abstract-relay (routes.ts, context-limit.ts, upstream.ts).
package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Shape is the client's error dialect.
type Shape string

const (
	OpenAI    Shape = "openai"
	Anthropic Shape = "anthropic"
)

// AnthropicErrorType maps a status to Anthropic's error type names.
func AnthropicErrorType(status int) string {
	switch {
	case status == 401:
		return "authentication_error"
	case status == 403:
		return "permission_error"
	case status == 404:
		return "not_found_error"
	case status == 402:
		return "billing_error"
	case status == 429:
		return "rate_limit_error"
	case status >= 500:
		return "api_error"
	}
	return "invalid_request_error"
}

// marshal encodes without HTML escaping so ">" in overflow messages survives verbatim.
func marshal(v any) []byte {
	var buf strings.Builder
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
	return []byte(strings.TrimSuffix(buf.String(), "\n"))
}

// Envelope renders {error:{...}} for OpenAI shapes and {type:'error',error:{...}} for Anthropic.
func Envelope(shape Shape, typ, message string, extra map[string]any) []byte {
	detail := map[string]any{"message": message, "type": typ}
	for k, v := range extra {
		detail[k] = v
	}
	var body any
	if shape == Anthropic {
		body = map[string]any{"type": "error", "error": detail}
	} else {
		body = map[string]any{"error": detail}
	}
	b := marshal(body)
	return b
}

// WriteError sends an error envelope. Headers already set on w are kept.
func WriteError(w http.ResponseWriter, shape Shape, status int, typ, message string, extra map[string]any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	w.Write(Envelope(shape, typ, message, extra))
}

// ---- Context-window overflow ----

// Overflow is the token counts an upstream overflow message gave, when it gave them.
type Overflow struct {
	Requested int
	Limit     int
	Numbered  bool
}

type numbered struct {
	re               *regexp.Regexp
	requested, limit int
}

var numberedPatterns = []numbered{
	// Anthropic: "prompt is too long: 1085472 tokens > 1048576 maximum"
	{regexp.MustCompile(`(?i)prompt is too long:\s*(\d+)\s*tokens?\s*>\s*(\d+)`), 1, 2},
	// Moonshot / Kimi: "Your request exceeded model token limit: 1048576 (requested: 1085472)"
	{regexp.MustCompile(`(?i)exceeded model token limit:\s*(\d+)\s*\(requested:\s*(\d+)\)`), 2, 1},
	// OpenAI / vLLM: "maximum context length is 131072 tokens. However, you requested 140000 tokens"
	{regexp.MustCompile(`(?is)maximum context length is\s*(\d+)\s*tokens?.*?(?:resulted in|requested)\s*(\d+)\s*tokens?`), 2, 1},
	// sglang: "The input (140000 tokens) is longer than the model's context length (131072 tokens)"
	{regexp.MustCompile(`(?i)input \((\d+) tokens?\) is longer than the model's context length \((\d+) tokens?\)`), 1, 2},
}

// Wording that names the input or context window. Output caps are a different error and must not match.
var unnumbered = regexp.MustCompile(`(?i)context_length_exceeded|context length|context window|prompt is too long|input is too long|exceeded model token limit`)

// ContextOverflow reports the overflow an upstream error text describes, or false.
func ContextOverflow(text string) (Overflow, bool) {
	if text == "" {
		return Overflow{}, false
	}
	for _, p := range numberedPatterns {
		if m := p.re.FindStringSubmatch(text); m != nil {
			req, _ := strconv.Atoi(m[p.requested])
			lim, _ := strconv.Atoi(m[p.limit])
			return Overflow{Requested: req, Limit: lim, Numbered: true}, true
		}
	}
	if unnumbered.MatchString(text) {
		return Overflow{}, true
	}
	return Overflow{}, false
}

// AnthropicOverflowMessage is Anthropic's exact wording, which Claude Code matches.
func AnthropicOverflowMessage(o Overflow) string {
	if o.Numbered {
		return fmt.Sprintf("prompt is too long: %d tokens > %d maximum", o.Requested, o.Limit)
	}
	return "prompt is too long"
}

// OpenAIOverflowMessage is OpenAI's wording for the same error.
func OpenAIOverflowMessage(o Overflow) string {
	if o.Numbered {
		return fmt.Sprintf("This model's maximum context length is %d tokens. However, your messages resulted in %d tokens. Please reduce the length of the messages.", o.Limit, o.Requested)
	}
	return "Your input exceeds the context window of this model. Please adjust your input and try again."
}

func randomID(prefix string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

// AnthropicOverflowEvent is one SSE event (no trailing blank line) for an open Anthropic stream.
func AnthropicOverflowEvent(o Overflow) string {
	ev := marshal(map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": AnthropicOverflowMessage(o)}})
	return "event: error\ndata: " + string(ev)
}

// ResponsesOverflowEvent is one SSE event for a Responses stream: response.failed with context_length_exceeded.
func ResponsesOverflowEvent(o Overflow) string {
	ev := marshal(map[string]any{
		"type": "response.failed", "sequence_number": 0,
		"response": map[string]any{
			"id": randomID("resp_"), "object": "response", "status": "failed",
			"error": map[string]any{"code": "context_length_exceeded", "message": OpenAIOverflowMessage(o)},
			"usage": nil,
		},
	})
	return "event: response.failed\ndata: " + string(ev)
}

// WriteOverflow answers a context overflow in the wording each client recovers from.
func WriteOverflow(w http.ResponseWriter, shape Shape, o Overflow, responsesStream bool) {
	if shape == Anthropic {
		WriteError(w, Anthropic, 400, "invalid_request_error", AnthropicOverflowMessage(o), nil)
		return
	}
	if responsesStream {
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(200)
		w.Write([]byte(ResponsesOverflowEvent(o) + "\n\n"))
		return
	}
	WriteError(w, OpenAI, 400, "invalid_request_error", OpenAIOverflowMessage(o), map[string]any{"param": "messages", "code": "context_length_exceeded"})
}

// ---- Upstream wording ----

var vendorWords = regexp.MustCompile(`(?i)\b(?:kimi[\w.\-]*|moonshot(?:ai)?|glm[\w.\-]*|z\.ai|zai|zhipu(?:ai)?|x-preview-l)\b`)

// Scrub replaces base-model and vendor names in text with a neutral phrase.
func Scrub(text string) string {
	return vendorWords.ReplaceAllString(text, "the model")
}

// upstreamMessage pulls a human message out of an upstream error body of any dialect.
func upstreamMessage(body []byte) string {
	var v map[string]any
	if json.Unmarshal(body, &v) == nil {
		if e, ok := v["error"].(map[string]any); ok {
			if m, ok := e["message"].(string); ok && m != "" {
				return m
			}
		}
		if e, ok := v["error"].(string); ok && e != "" {
			return e
		}
		if m, ok := v["message"].(string); ok && m != "" {
			return m
		}
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 300 {
		s = s[:300]
	}
	return s
}

const busyRetryAfter = 60

// RetryAfterSeconds reads an upstream Retry-After (delta-seconds or an HTTP date), clamped to 1s–1h, default 60.
func RetryAfterSeconds(h http.Header) int {
	raw := strings.TrimSpace(h.Get("Retry-After"))
	if raw == "" {
		return busyRetryAfter
	}
	var secs int
	if n, err := strconv.Atoi(raw); err == nil {
		secs = n
	} else if t, err := http.ParseTime(raw); err == nil {
		secs = int(time.Until(t).Seconds()) + 1
	} else {
		return busyRetryAfter
	}
	return min(3600, max(1, secs))
}

// WriteUpstreamFailure maps an upstream error (status and body) into the
// client's dialect. Nothing from the upstream body reaches the client verbatim
// except a scrubbed message for 4xx responses the client can act on.
func WriteUpstreamFailure(w http.ResponseWriter, shape Shape, responsesStream bool, status int, body []byte, h http.Header) {
	text := upstreamMessage(body)
	if o, ok := ContextOverflow(text); ok {
		WriteOverflow(w, shape, o, responsesStream)
		return
	}
	if status == 429 {
		wait := RetryAfterSeconds(h)
		w.Header().Set("Retry-After", strconv.Itoa(wait))
		msg := fmt.Sprintf("Estro is at capacity right now. Retry in about %s.", formatWait(wait))
		if shape == Anthropic {
			WriteError(w, Anthropic, 429, "rate_limit_error", msg, nil)
		} else {
			WriteError(w, OpenAI, 429, "rate_limit_error", msg, map[string]any{"code": "server_busy"})
		}
		return
	}
	// Upstream auth failures are the relay's problem, not the client's key.
	if status == 401 || status == 403 {
		status = 502
	}
	detail := "upstream request failed"
	if status >= 400 && status < 500 && text != "" {
		detail = Scrub(text)
	} else if status >= 500 {
		detail = fmt.Sprintf("upstream request failed (status %d)", status)
	}
	if shape == Anthropic {
		WriteError(w, Anthropic, status, AnthropicErrorType(status), detail, nil)
		return
	}
	typ := "invalid_request_error"
	code := any("upstream_error")
	if status >= 500 {
		typ = "upstream_error"
		code = status
	}
	WriteError(w, OpenAI, status, typ, detail, map[string]any{"code": code, "upstream_status": status})
}

// WriteTransportFailure answers when no upstream response arrived at all.
func WriteTransportFailure(w http.ResponseWriter, shape Shape) {
	if shape == Anthropic {
		WriteError(w, Anthropic, 502, "api_error", "upstream request failed", nil)
		return
	}
	WriteError(w, OpenAI, 502, "upstream_error", "upstream request failed", map[string]any{"code": 502, "upstream_status": nil})
}

func formatWait(secs int) string {
	switch {
	case secs < 60:
		return fmt.Sprintf("%ds", secs)
	case secs < 3600:
		return fmt.Sprintf("%dm", (secs+59)/60)
	}
	return "1h"
}

// RewriteErrorLine turns an upstream SSE `data:` error event into the overflow
// event the dialect's clients recover from, or scrubs its message. It returns
// the replacement bytes for the whole line (without the trailing newline) and
// whether anything changed.
func RewriteErrorLine(shape Shape, responses bool, line []byte) ([]byte, bool) {
	t := strings.TrimSpace(string(line))
	if !strings.HasPrefix(t, "data:") {
		return nil, false
	}
	payload := strings.TrimSpace(strings.TrimPrefix(t, "data:"))
	if payload == "" || payload[0] != '{' || !strings.Contains(payload, `"error"`) {
		return nil, false
	}
	var ev map[string]any
	if json.Unmarshal([]byte(payload), &ev) != nil {
		return nil, false
	}
	errv, hasErr := ev["error"]
	typ, _ := ev["type"].(string)
	isError := typ == "error" || typ == "response.failed" || (hasErr && errv != nil && typ == "")
	if !isError {
		return nil, false
	}
	raw := marshal(errv)
	if o, ok := ContextOverflow(string(raw)); ok {
		switch {
		case shape == Anthropic:
			return []byte(AnthropicOverflowEvent(o)), true
		case responses:
			return []byte(ResponsesOverflowEvent(o)), true
		}
		msg := marshal(map[string]any{"error": map[string]any{"message": OpenAIOverflowMessage(o), "type": "invalid_request_error", "code": "context_length_exceeded"}})
		return []byte("data: " + string(msg)), true
	}
	if em, ok := errv.(map[string]any); ok {
		if m, ok := em["message"].(string); ok {
			scrubbed := Scrub(m)
			if scrubbed != m {
				em["message"] = scrubbed
				out := marshal(ev)
				return []byte("data: " + string(out)), true
			}
		}
	}
	return nil, false
}
