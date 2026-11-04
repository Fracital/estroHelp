package zai

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZaiDumpWritesUpstreamDetail(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("ERROR_DUMP_DIR", dir)
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Client-Request-Id", "req-123")
	body := []byte(`{"model":"kali-epsilon","messages":[{"role":"user","content":"hi"}]}`)
	zaiDump(r, body, 502, &zaiUpstreamError{Status: 429, Code: "1302", Detail: "too many requests"})

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	if len(files) != 1 {
		t.Fatalf("expected one dump, got %v", files)
	}
	raw, _ := os.ReadFile(files[0])
	var d map[string]any
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	if d["provider"] != "zai" || d["dialect"] != "chat/completions" || d["upstream_status"] != float64(429) {
		t.Fatalf("header fields wrong: %v", d)
	}
	if !strings.Contains(d["upstream_body"].(string), "too many requests") {
		t.Fatalf("upstream detail missing: %v", d["upstream_body"])
	}
	if d["client_headers"].(map[string]any)["X-Client-Request-Id"] != "req-123" {
		t.Fatalf("client request id missing: %v", d["client_headers"])
	}
	if req, _ := d["request"].(map[string]any); req["model"] != "kali-epsilon" {
		t.Fatalf("request body not kept: %v", d["request"])
	}
}
