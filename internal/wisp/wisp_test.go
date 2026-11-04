package wisp

import (
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func frame(typ byte, id uint32, payload []byte) []byte {
	b := make([]byte, 5+len(payload))
	b[0] = typ
	binary.LittleEndian.PutUint32(b[1:], id)
	copy(b[5:], payload)
	return b
}

func connectPayload(kind byte, host string, port uint16) []byte {
	p := make([]byte, 3+len(host))
	p[0] = kind
	binary.LittleEndian.PutUint16(p[1:], port)
	copy(p[3:], host)
	return p
}

func echoServer(t *testing.T) (string, uint16) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { io.Copy(c, c); c.Close() }()
		}
	}()
	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), uint16(addr.Port)
}

func dial(t *testing.T, h *Handler) *websocket.Conn {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func read(t *testing.T, ws *websocket.Conn) (byte, uint32, []byte) {
	t.Helper()
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if len(msg) < 5 {
		t.Fatalf("short frame %x", msg)
	}
	return msg[0], binary.LittleEndian.Uint32(msg[1:5]), msg[5:]
}

func expectInitialContinue(t *testing.T, ws *websocket.Conn) {
	typ, id, p := read(t, ws)
	if typ != pktContinue || id != 0 || binary.LittleEndian.Uint32(p) != bufferSize {
		t.Fatalf("expected initial CONTINUE, got type=%d id=%d payload=%x", typ, id, p)
	}
}

func TestEchoRoundTrip(t *testing.T) {
	host, port := echoServer(t)
	h := New()
	h.AllowPrivate = true
	ws := dial(t, h)
	expectInitialContinue(t, ws)

	if err := ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 7, connectPayload(streamTCP, host, port))); err != nil {
		t.Fatal(err)
	}
	if err := ws.WriteMessage(websocket.BinaryMessage, frame(pktData, 7, []byte("hello wisp"))); err != nil {
		t.Fatal(err)
	}
	var got []byte
	gotContinue := false
	for len(got) < len("hello wisp") || !gotContinue {
		typ, id, p := read(t, ws)
		if id != 7 {
			t.Fatalf("unexpected stream %d", id)
		}
		switch typ {
		case pktData:
			got = append(got, p...)
		case pktContinue:
			gotContinue = true
		case pktClose:
			t.Fatalf("stream closed early, reason %x", p)
		}
	}
	if string(got) != "hello wisp" {
		t.Fatalf("echo mismatch: %q", got)
	}

	// Client close tears down the destination; nothing further arrives for it.
	ws.WriteMessage(websocket.BinaryMessage, frame(pktClose, 7, []byte{ReasonVoluntary}))
	ws.WriteMessage(websocket.BinaryMessage, frame(pktData, 7, []byte("after close")))
	ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, msg, err := ws.ReadMessage(); err == nil {
		t.Fatalf("unexpected frame after close: %x", msg)
	}
}

func TestRemoteEOFClosesStream(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	defer ln.Close()
	go func() {
		c, _ := ln.Accept()
		c.Write([]byte("bye"))
		c.Close()
	}()
	addr := ln.Addr().(*net.TCPAddr)
	h := New()
	h.AllowPrivate = true
	ws := dial(t, h)
	expectInitialContinue(t, ws)
	ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 1, connectPayload(streamTCP, "127.0.0.1", uint16(addr.Port))))
	sawData, sawClose := false, false
	for !sawClose {
		typ, _, p := read(t, ws)
		switch typ {
		case pktData:
			sawData = string(p) == "bye"
		case pktClose:
			sawClose = true
			if p[0] != ReasonVoluntary {
				t.Fatalf("close reason %x", p[0])
			}
		}
	}
	if !sawData {
		t.Fatal("did not receive remote data before close")
	}
}

func TestPrivateHostBlocked(t *testing.T) {
	host, port := echoServer(t)
	ws := dial(t, New())
	expectInitialContinue(t, ws)
	ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 3, connectPayload(streamTCP, host, port)))
	typ, id, p := read(t, ws)
	if typ != pktClose || id != 3 || p[0] != ReasonHostBlocked {
		t.Fatalf("expected HostBlocked close, got type=%d id=%d payload=%x", typ, id, p)
	}
	for _, h := range []string{"localhost", "127.0.0.1", "10.1.2.3", "172.16.8.8", "192.168.1.1", "169.254.1.1", "::1", "fd00::1"} {
		if !hostBlocked(h) {
			t.Errorf("%s should be blocked", h)
		}
	}
	if hostBlocked("93.184.216.34") {
		t.Error("public address should not be blocked")
	}
}

func TestConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	h := New()
	h.AllowPrivate = true
	ws := dial(t, h)
	expectInitialContinue(t, ws)
	ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 9, connectPayload(streamTCP, "127.0.0.1", uint16(port))))
	typ, _, p := read(t, ws)
	if typ != pktClose || p[0] != ReasonConnRefused {
		t.Fatalf("expected ConnRefused, got type=%d payload=%x", typ, p)
	}
}

func TestGarbageIgnoredAndStreamCap(t *testing.T) {
	host, port := echoServer(t)
	h := New()
	h.AllowPrivate = true
	h.MaxStreams = 2
	ws := dial(t, h)
	expectInitialContinue(t, ws)
	ws.WriteMessage(websocket.BinaryMessage, []byte{0x02})            // too short
	ws.WriteMessage(websocket.TextMessage, []byte("not a packet"))    // wrong type
	ws.WriteMessage(websocket.BinaryMessage, frame(0x7f, 1, nil))     // unknown packet
	ws.WriteMessage(websocket.BinaryMessage, frame(pktData, 42, nil)) // unknown stream
	for id := uint32(1); id <= 3; id++ {
		ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, id, connectPayload(streamTCP, host, port)))
	}
	typ, id, p := read(t, ws)
	if typ != pktClose || id != 3 || p[0] != ReasonConnThrottled {
		t.Fatalf("expected third stream throttled, got type=%d id=%d payload=%x", typ, id, p)
	}
}

func TestOriginCheck(t *testing.T) {
	srv := httptest.NewServer(New())
	defer srv.Close()
	hdr := http.Header{"Origin": {"https://evil.example"}}
	if _, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), hdr); err == nil {
		t.Fatal("cross-origin upgrade should be refused")
	}
	hdr = http.Header{"Origin": {srv.URL}}
	ws, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(srv.URL, "http"), hdr)
	if err != nil {
		t.Fatalf("same-origin upgrade refused: %v", err)
	}
	ws.Close()
}

func TestAllowHostsExemptsLoopback(t *testing.T) {
	host, port := echoServer(t)
	h := New()
	h.AllowHosts = []string{net.JoinHostPort(host, strconv.Itoa(int(port)))}
	ws := dial(t, h)
	expectInitialContinue(t, ws)
	ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 5, connectPayload(streamTCP, host, port)))
	ws.WriteMessage(websocket.BinaryMessage, frame(pktData, 5, []byte("self")))
	for {
		typ, id, p := read(t, ws)
		if id != 5 {
			t.Fatalf("unexpected stream %d", id)
		}
		if typ == pktClose {
			t.Fatalf("allowed self host was closed, reason %x", p)
		}
		if typ == pktData && string(p) == "self" {
			break
		}
	}
	// A different loopback port stays blocked.
	ws.WriteMessage(websocket.BinaryMessage, frame(pktConnect, 6, connectPayload(streamTCP, host, port+1)))
	for {
		typ, id, p := read(t, ws)
		if id != 6 {
			continue
		}
		if typ != pktClose || p[0] != ReasonHostBlocked {
			t.Fatalf("expected HostBlocked for other port, got type=%d payload=%x", typ, p)
		}
		break
	}
}
