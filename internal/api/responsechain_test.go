package api

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"elichat/internal/relay"
)

// recordThrough sends a captured upstream reply through the same rewriter and
// recorder the responses handler uses, in chunks of the given size.
func recordThrough(t *testing.T, path string, chunk int) (*responseRecorder, string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rec := &responseRecorder{}
	w := httptest.NewRecorder()
	rw := newModelRewriter(w, "kali-gamma", relay.OpenAI, true)
	rw.onLine = rec.observe
	for i := 0; i < len(data); i += chunk {
		end := min(i+chunk, len(data))
		if _, err := rw.Write(data[i:end]); err != nil {
			t.Fatal(err)
		}
	}
	rw.Close()
	return rec, w.Body.String()
}

func TestRecorderKeepsChainFromRealStream(t *testing.T) {
	for _, chunk := range []int{1, 7, 4096} {
		rec, _ := recordThrough(t, "testdata/responses_stream.sse", chunk)
		id, output, ok := rec.result()
		if !ok || id == "" || len(output) == 0 {
			t.Fatalf("chunk=%d: no chain recorded (id=%q ok=%v items=%d)", chunk, id, ok, len(output))
		}
	}
}

func TestRecorderKeepsChainFromRealPlainBody(t *testing.T) {
	rec, body := recordThrough(t, "testdata/responses_plain.json", 64)
	id, output, ok := rec.result()
	if !ok || id == "" || len(output) == 0 {
		t.Fatalf("no chain recorded from plain body (id=%q ok=%v items=%d)", id, ok, len(output))
	}
	if !json.Valid([]byte(body)) {
		t.Fatalf("rewritten body is not JSON: %s", body)
	}
}

func TestSetChainTTLClamps(t *testing.T) {
	defer SetChainTTL(chainTTLDefault)
	for in, want := range map[time.Duration]time.Duration{0: 30 * time.Minute, -time.Second: 30 * time.Minute, time.Hour: time.Hour, 48 * time.Hour: 12 * time.Hour} {
		if got := SetChainTTL(in); got != want || chainTTLSeconds() != int(want/time.Second) {
			t.Fatalf("SetChainTTL(%v)=%v want %v", in, got, want)
		}
	}
}

// Replies cut short by max_output_tokens end in response.incomplete.
func TestRecorderKeepsChainFromIncompleteStream(t *testing.T) {
	rec, _ := recordThrough(t, "testdata/responses_incomplete.sse", 4096)
	id, output, ok := rec.result()
	if !ok || id == "" || len(output) == 0 {
		t.Fatalf("no chain recorded from incomplete stream (id=%q ok=%v items=%d)", id, ok, len(output))
	}
}

func TestRecorderKeepsChainFromIncompletePlainBody(t *testing.T) {
	rec, _ := recordThrough(t, "testdata/responses_incomplete.json", 64)
	id, output, ok := rec.result()
	if !ok || id == "" || len(output) == 0 {
		t.Fatalf("no chain recorded from incomplete body (id=%q ok=%v items=%d)", id, ok, len(output))
	}
}
