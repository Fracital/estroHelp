package api

import (
	"net/http/httptest"
	"strings"
	"testing"

	"elichat/internal/relay"
)

func TestRewriterStampsModelAndRewritesMidStreamOverflow(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newModelRewriter(w, "kali-gamma", relay.Anthropic, false)
	rw.Write([]byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"model\":\"kimi-k3\",\"usage\":{\"input_tokens\":5,\"output_tokens\":0}}}\n\n"))
	rw.Write([]byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"invalid_request_error\",\"message\":\"prompt is too long: 9 tokens > 3 maximum\"}}\n\n"))
	rw.Close()
	out := w.Body.String()
	if strings.Contains(out, "kimi") || !strings.Contains(out, `"model":"kali-gamma"`) {
		t.Fatalf("model stamp: %s", out)
	}
	if strings.Count(out, "event: error") != 1 || !strings.Contains(out, "9 tokens > 3 maximum") {
		t.Fatalf("overflow event: %s", out)
	}
	if rw.usage.input != 5 {
		t.Fatalf("usage %+v", rw.usage)
	}
}

func TestRewriterPlainBody(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newModelRewriter(w, "kali-beta", relay.OpenAI, false)
	rw.Write([]byte(`{"id":"x","model":"kimi-k3","choices":[],"usage":{"prompt_tokens":3,"completion_tokens":4}}`))
	rw.Close()
	if !strings.Contains(w.Body.String(), `"model":"kali-beta"`) || rw.usage.output != 4 {
		t.Fatalf("%s %+v", w.Body.String(), rw.usage)
	}
}
