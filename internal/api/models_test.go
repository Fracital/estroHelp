package api

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"elichat/internal/relay"
)

func TestListModels(t *testing.T) {
	c := &Chat{}
	w := httptest.NewRecorder()
	c.listModelsOpenAI(w, httptest.NewRequest("GET", "/v1/models", nil))
	var out struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("body: %v", err)
	}
	ids := map[string]bool{}
	for _, m := range out.Data {
		ids[m["id"].(string)] = true
	}
	for _, want := range []string{"kali-gamma", "kali-beta", "kali-epsilon"} {
		if !ids[want] {
			t.Errorf("catalog missing %s", want)
		}
	}
}

func TestRewriterStampsPersonaID(t *testing.T) {
	w := httptest.NewRecorder()
	rw := newModelRewriter(w, "kali-gamma", relay.OpenAI, false)
	rw.Write([]byte(`{"id":"x","model":"kimi-k3","choices":[]}`))
	rw.Close()
	if !strings.Contains(w.Body.String(), `"model":"kali-gamma"`) {
		t.Fatalf("stamp: %s", w.Body.String())
	}
}
