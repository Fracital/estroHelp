package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestV1Front(t *testing.T) {
	var got string
	h := v1Front(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { got = r.URL.Path }))

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/v1/v1/v1/models", nil))
	if got != "/v1/models" || w.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("collapse: path %q origin %q", got, w.Header().Get("Access-Control-Allow-Origin"))
	}

	w = httptest.NewRecorder()
	got = ""
	h.ServeHTTP(w, httptest.NewRequest("OPTIONS", "/v1/chat/completions", nil))
	if w.Code != 204 || got != "" || w.Header().Get("Access-Control-Allow-Headers") == "" {
		t.Fatalf("preflight: %d %q", w.Code, got)
	}

	w = httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/api/health", nil))
	if got != "/api/health" || w.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("non-v1 paths must be untouched")
	}
}
