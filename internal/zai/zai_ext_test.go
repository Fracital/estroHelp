package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// zaiExtClient dials the hub's socket as a fake extension and runs a scripted
// read loop, calling answer for each solve request.
func zaiExtClient(t *testing.T, url, token string, answer func(id string) (string, string)) {
	t.Helper()
	dialer := websocket.Dialer{}
	hdr := map[string][]string{"Origin": {"chrome-extension://fake"}}
	conn, _, err := dialer.Dial(url, hdr)
	if err != nil {
		t.Errorf("extension dial: %v", err)
		return
	}
	defer conn.Close()
	if err := conn.WriteJSON(map[string]string{"type": "hello", "token": token, "version": "test"}); err != nil {
		t.Errorf("hello: %v", err)
		return
	}
	for {
		var ev zaiExtEvent
		if err := conn.ReadJSON(&ev); err != nil {
			return
		}
		if ev.Type == "solve" {
			tok, errStr := answer(ev.ID)
			if errStr != "" {
				conn.WriteJSON(zaiExtEvent{Type: "error", ID: ev.ID, Error: errStr})
			} else {
				conn.WriteJSON(zaiExtEvent{Type: "token", ID: ev.ID, Token: tok})
			}
		}
	}
}

func TestZaiExtHubSolveRoundTrip(t *testing.T) {
	t.Setenv(zaiExtTokenEnv, "sekret")
	hub := newZaiExtHub()
	srv := httptest.NewServer(http.HandlerFunc(hub.serveSocket))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	go zaiExtClient(t, wsURL, "sekret", func(id string) (string, string) {
		return "cHVzaGVkLXRva2Vu", "" // base64 "pushed-token"
	})

	// Wait for the extension to connect.
	deadline := time.Now().Add(3 * time.Second)
	for !hub.isConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if !hub.isConnected() {
		t.Fatal("extension did not connect")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tok, err := hub.solve(ctx)
	if err != nil {
		t.Fatalf("solve: %v", err)
	}
	if tok != "cHVzaGVkLXRva2Vu" {
		t.Errorf("token = %q", tok)
	}
}

func TestZaiExtHubNotConnected(t *testing.T) {
	hub := newZaiExtHub()
	_, err := hub.solve(context.Background())
	if err != errZaiExtNotConnected {
		t.Errorf("expected not-connected, got %v", err)
	}
}

func TestZaiExtRejectsBadToken(t *testing.T) {
	t.Setenv(zaiExtTokenEnv, "sekret")
	hub := newZaiExtHub()
	srv := httptest.NewServer(http.HandlerFunc(hub.serveSocket))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	dialer := websocket.Dialer{}
	conn, _, err := dialer.Dial(wsURL, map[string][]string{"Origin": {"chrome-extension://fake"}})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()
	conn.WriteJSON(map[string]string{"type": "hello", "token": "wrong"})
	// Server should close with 4401.
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected the server to close a bad-token connection")
	}
	if hub.isConnected() {
		t.Error("hub should not be connected after a bad-token hello")
	}
}

func TestZaiSolveCaptchaPrefersExtension(t *testing.T) {
	t.Setenv(zaiExtTokenEnv, "sekret")
	t.Setenv("ZAI_CAPTCHA_PARAM", "pasted-token")
	hub := newZaiExtHub()
	srv := httptest.NewServer(http.HandlerFunc(hub.serveSocket))
	defer srv.Close()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	// Point the global hub at a connected fake extension.
	old := zaiExt
	zaiExt = hub
	t.Cleanup(func() { zaiExt = old })

	go zaiExtClient(t, wsURL, "sekret", func(id string) (string, string) { return "ext-token", "" })
	deadline := time.Now().Add(3 * time.Second)
	for !hub.isConnected() && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := zaiSolveCaptcha(ctx)
	if err != nil {
		t.Fatalf("zaiSolveCaptcha: %v", err)
	}
	if got != "ext-token" {
		t.Errorf("expected the extension token to win over the pasted one, got %q", got)
	}
}

func TestZaiSolveCaptchaFallsBackToPasted(t *testing.T) {
	// No extension connected, but a pasted token is set.
	t.Setenv(zaiExtTokenEnv, "sekret")
	t.Setenv("ZAI_CAPTCHA_PARAM", "pasted-token")
	old := zaiExt
	zaiExt = newZaiExtHub() // nothing connected
	t.Cleanup(func() { zaiExt = old })

	got, err := zaiSolveCaptcha(context.Background())
	if err != nil {
		t.Fatalf("zaiSolveCaptcha: %v", err)
	}
	if got != "pasted-token" {
		t.Errorf("expected pasted token fallback, got %q", got)
	}
}

// The solve request the server sends must be valid JSON the extension reads.
func TestZaiExtEventShape(t *testing.T) {
	b, _ := json.Marshal(zaiExtEvent{Type: "solve", ID: "abc"})
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if m["type"] != "solve" || m["id"] != "abc" {
		t.Errorf("event shape: %v", m)
	}
}
