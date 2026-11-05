package zai

import (
	"net/http"
	"testing"
	"time"
)

func TestZaiRetryableCoversPreGenerationFailuresOnly(t *testing.T) {
	cases := []struct {
		name string
		out  zaiBrowserResult
		want bool
	}{
		{"waf 405", zaiBrowserResult{Status: http.StatusMethodNotAllowed, Error: "upstream HTTP 405: blocked by upstream WAF"}, true},
		{"relay blocked", zaiBrowserResult{RelayBlocked: true}, true},
		{"account busy", zaiBrowserResult{Status: http.StatusTooManyRequests, Error: "upstream: {...429...}"}, true},
		{"model capacity", zaiBrowserResult{Error: `upstream: {"code": "MODEL_CONCURRENCY_LIMIT", "detail": "Model is currently at capacity."}`}, true},
		{"send never fired", zaiBrowserResult{Error: "completion request never fired (captcha or send failed)"}, true},
		{"timed out mid-generation", zaiBrowserResult{Error: "timed out waiting for completion response"}, false},
		{"bad request", zaiBrowserResult{Status: 400, Error: "upstream: invalid"}, false},
	}
	for _, c := range cases {
		if got := zaiRetryable(&c.out); got != c.want {
			t.Errorf("%s: retryable=%v want %v", c.name, got, c.want)
		}
	}
}

func TestZaiRetryBackoffShapes(t *testing.T) {
	waf := &zaiBrowserResult{Status: http.StatusMethodNotAllowed, Error: "blocked by upstream WAF"}
	if d := zaiRetryBackoff(1, waf); d > time.Second {
		t.Errorf("first WAF retry should be immediate, got %s", d)
	}
	if d := zaiRetryBackoff(20, waf); d != 15*time.Second {
		t.Errorf("WAF backoff should cap at 15s, got %s", d)
	}
	cap := &zaiBrowserResult{Error: "MODEL_CONCURRENCY_LIMIT"}
	if d := zaiRetryBackoff(1, cap); d != 5*time.Second {
		t.Errorf("capacity backoff starts at 5s, got %s", d)
	}
	if d := zaiRetryBackoff(50, cap); d != 30*time.Second {
		t.Errorf("capacity backoff caps at 30s, got %s", d)
	}
}
