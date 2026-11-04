package zai

// The z.ai extension transport mints Aliyun captcha tokens in the operator's own
// browser. Aliyun's captcha SDK refuses to boot under Patchright's automation
// fingerprint (initAliyunCaptcha never attaches, even headed under xvfb), so the
// reliable way to get a fresh captcha_verify_param is a real browser already
// trusted by chat.z.ai. The goldenticket z.ai extension (ext/zai-extension)
// connects here over a websocket; each captcha request is forwarded to it, the
// content script on chat.z.ai drives the Aliyun widget (which auto-passes on a
// clean fingerprint), and the resulting base64 token comes back over the wire.
//
// This mirrors the ChatGPT extension transport (chatgpt_ext.go) but is far
// simpler: no conversation state, no streaming, one request -> one token.

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// zaiExtTokenEnv holds the shared secret the z.ai extension presents on hello.
const zaiExtTokenEnv = "ZAI_EXT_TOKEN"

// zaiExtSolveTimeout bounds one captcha mint in the browser.
const zaiExtSolveTimeout = 60 * time.Second

var errZaiExtNotConnected = errors.New("the z.ai browser extension is not connected")

// zaiExtEvent is one message in either direction. Server -> extension: "solve".
// Extension -> server: "token" (success) or "error", each carrying the request id.
type zaiExtEvent struct {
	Type  string `json:"type"`
	ID    string `json:"id"`
	Token string `json:"token,omitempty"` // base64 captcha_verify_param
	Error string `json:"error,omitempty"` // human-readable failure
}

// zaiExtRequest is one in-flight captcha solve.
type zaiExtRequest struct {
	events chan zaiExtEvent
	gone   chan struct{}
}

// zaiExtHub holds the one connected z.ai extension and its in-flight solves.
type zaiExtHub struct {
	mu       sync.Mutex
	conn     *websocket.Conn
	requests map[string]*zaiExtRequest
}

func newZaiExtHub() *zaiExtHub {
	return &zaiExtHub{requests: map[string]*zaiExtRequest{}}
}

var zaiExt = newZaiExtHub()

func (h *zaiExtHub) isConnected() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.conn != nil
}

func (h *zaiExtHub) attach(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != nil {
		h.conn.Close()
	}
	h.conn = conn
	conn.WriteJSON(zaiExtEvent{Type: "ready"})
}

func (h *zaiExtHub) detach(conn *websocket.Conn) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn != conn {
		return
	}
	h.conn = nil
	for id, req := range h.requests {
		close(req.gone)
		delete(h.requests, id)
	}
}

// solve asks the extension for a fresh captcha token and waits for it.
func (h *zaiExtHub) solve(ctx context.Context) (string, error) {
	id := generateUUID()
	h.mu.Lock()
	if h.conn == nil {
		h.mu.Unlock()
		return "", errZaiExtNotConnected
	}
	req := &zaiExtRequest{events: make(chan zaiExtEvent, 4), gone: make(chan struct{})}
	h.requests[id] = req
	err := h.conn.WriteJSON(zaiExtEvent{Type: "solve", ID: id})
	h.mu.Unlock()
	if err != nil {
		h.finish(id)
		return "", err
	}
	defer h.finish(id)

	timer := time.NewTimer(zaiExtSolveTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-req.gone:
			return "", errZaiExtNotConnected
		case <-timer.C:
			return "", errors.New("z.ai extension captcha solve timed out")
		case ev := <-req.events:
			switch ev.Type {
			case "token":
				if ev.Token == "" {
					return "", errors.New("z.ai extension returned an empty token")
				}
				return ev.Token, nil
			case "error":
				return "", errors.New("z.ai extension: " + ev.Error)
			}
		}
	}
}

func (h *zaiExtHub) finish(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.requests, id)
}

// readLoop routes the extension's events to their in-flight solves until the
// connection ends.
func (h *zaiExtHub) readLoop(conn *websocket.Conn) {
	defer func() {
		conn.Close()
		h.detach(conn)
	}()
	for {
		var ev zaiExtEvent
		if err := conn.ReadJSON(&ev); err != nil {
			return
		}
		if ev.Type == "ping" {
			h.mu.Lock()
			conn.WriteJSON(zaiExtEvent{Type: "pong"})
			h.mu.Unlock()
			continue
		}
		h.mu.Lock()
		req := h.requests[ev.ID]
		h.mu.Unlock()
		if req == nil {
			logf("event=zai_ext_unrouted type=%s id=%s", ev.Type, ev.ID)
			continue
		}
		select {
		case req.events <- ev:
		case <-req.gone:
		}
	}
}

// serveSocket accepts the z.ai extension's websocket. The first message must be
// a hello carrying the shared token; the origin must be a Chrome extension.
func (h *zaiExtHub) serveSocket(w http.ResponseWriter, r *http.Request) {
	want := strings.TrimSpace(os.Getenv(zaiExtTokenEnv))
	if want == "" {
		writeAPIError(w, http.StatusServiceUnavailable, APIErrorDetail{
			Message: "the z.ai extension transport is disabled. Set ZAI_EXT_TOKEN.",
			Type:    "api_error",
			Code:    "zai_extension_disabled",
		})
		return
	}
	upgrader := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool {
		return strings.HasPrefix(r.Header.Get("Origin"), "chrome-extension://")
	}}
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var hello struct {
		Type    string `json:"type"`
		Token   string `json:"token"`
		Version string `json:"version"`
	}
	if err := conn.ReadJSON(&hello); err != nil || hello.Type != "hello" {
		logf("event=zai_ext_rejected reason=no_hello")
		closeSocket(conn, 4400, "expected a hello message first")
		return
	}
	if subtle.ConstantTimeCompare([]byte(hello.Token), []byte(want)) != 1 {
		logf("event=zai_ext_rejected reason=bad_token")
		closeSocket(conn, 4401, "invalid extension token")
		return
	}
	conn.SetReadDeadline(time.Time{})
	logf("event=zai_ext_connected version=%s", hello.Version)
	h.attach(conn)
	h.readLoop(conn)
}

// zaiExtEnabled reports whether the extension transport is configured.
func zaiExtEnabled() bool {
	return strings.TrimSpace(os.Getenv(zaiExtTokenEnv)) != ""
}
