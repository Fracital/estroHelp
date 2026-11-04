package relay

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestContextOverflow(t *testing.T) {
	cases := []struct {
		text      string
		ok        bool
		requested int
		limit     int
	}{
		{"prompt is too long: 1085472 tokens > 1048576 maximum", true, 1085472, 1048576},
		{"Your request exceeded model token limit: 1048576 (requested: 1085472)", true, 1085472, 1048576},
		{"This model's maximum context length is 131072 tokens. However, you requested 140000 tokens", true, 140000, 131072},
		{"The input (140000 tokens) is longer than the model's context length (131072 tokens)", true, 140000, 131072},
		{"context window exceeded", true, 0, 0},
		{"max_tokens is too large", false, 0, 0},
		{"", false, 0, 0},
	}
	for _, c := range cases {
		o, ok := ContextOverflow(c.text)
		if ok != c.ok || o.Requested != c.requested || o.Limit != c.limit {
			t.Errorf("%q: got %+v %v", c.text, o, ok)
		}
	}
}

func TestOverflowReplies(t *testing.T) {
	o := Overflow{Requested: 10, Limit: 5, Numbered: true}
	w := httptest.NewRecorder()
	WriteOverflow(w, Anthropic, o, false)
	if w.Code != 400 || !strings.Contains(w.Body.String(), `"prompt is too long: 10 tokens > 5 maximum"`) || !strings.Contains(w.Body.String(), `"type":"error"`) {
		t.Fatalf("anthropic: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteOverflow(w, OpenAI, o, true)
	if w.Code != 200 || !strings.HasPrefix(w.Body.String(), "event: response.failed\ndata: ") || !strings.Contains(w.Body.String(), "context_length_exceeded") {
		t.Fatalf("responses stream: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteOverflow(w, OpenAI, o, false)
	var body map[string]map[string]any
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 400 || body["error"]["code"] != "context_length_exceeded" {
		t.Fatalf("openai: %d %s", w.Code, w.Body.String())
	}
}

func TestUpstreamFailureMapping(t *testing.T) {
	w := httptest.NewRecorder()
	WriteUpstreamFailure(w, OpenAI, false, 429, []byte(`{"error":{"message":"busy"}}`), http.Header{"Retry-After": {"7"}})
	if w.Code != 429 || w.Header().Get("Retry-After") != "7" || !strings.Contains(w.Body.String(), "server_busy") {
		t.Fatalf("429: %d %s %s", w.Code, w.Header().Get("Retry-After"), w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteUpstreamFailure(w, Anthropic, false, 400, []byte(`{"error":{"message":"kimi-k3 rejected the schema"}}`), nil)
	if w.Code != 400 || strings.Contains(w.Body.String(), "kimi") || !strings.Contains(w.Body.String(), `"type":"invalid_request_error"`) {
		t.Fatalf("anthropic 400: %s", w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteUpstreamFailure(w, OpenAI, false, 401, []byte(`{"error":"bad key"}`), nil)
	if w.Code != 502 || strings.Contains(w.Body.String(), "bad key") {
		t.Fatalf("upstream 401 must become 502 without the message: %d %s", w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	WriteUpstreamFailure(w, Anthropic, false, 400, []byte(`{"error":{"message":"prompt is too long: 9 tokens > 3 maximum"}}`), nil)
	if !strings.Contains(w.Body.String(), "9 tokens > 3 maximum") {
		t.Fatalf("overflow via failure: %s", w.Body.String())
	}
}

func TestRetryAfterSeconds(t *testing.T) {
	if RetryAfterSeconds(http.Header{}) != 60 || RetryAfterSeconds(http.Header{"Retry-After": {"99999"}}) != 3600 || RetryAfterSeconds(http.Header{"Retry-After": {"0"}}) != 1 {
		t.Fatal("clamp")
	}
}

func TestRewriteErrorLine(t *testing.T) {
	line := []byte(`data: {"type":"error","error":{"type":"invalid_request_error","message":"Your request exceeded model token limit: 10 (requested: 20)"}}`)
	out, ok := RewriteErrorLine(Anthropic, false, line)
	if !ok || !strings.HasPrefix(string(out), "event: error\ndata: ") || !strings.Contains(string(out), "20 tokens > 10 maximum") {
		t.Fatalf("anthropic rewrite: %v %s", ok, out)
	}
	out, ok = RewriteErrorLine(OpenAI, true, line)
	if !ok || !strings.Contains(string(out), "response.failed") {
		t.Fatalf("responses rewrite: %v %s", ok, out)
	}
	out, ok = RewriteErrorLine(OpenAI, false, []byte(`data: {"error":{"message":"glm-5.3 is down","type":"api_error"}}`))
	if !ok || strings.Contains(string(out), "glm") {
		t.Fatalf("scrub: %v %s", ok, out)
	}
	if _, ok := RewriteErrorLine(OpenAI, false, []byte(`data: {"choices":[{"delta":{"content":"hi"}}]}`)); ok {
		t.Fatal("content line must pass")
	}
}
