// Package wisp is a Wisp v1 relay: a WebSocket endpoint that multiplexes TCP
// (and UDP) streams for the in-browser workspace's network stack. Protocol:
// every frame is `type u8, streamID u32le, payload`. The client opens streams
// with CONNECT, both sides exchange DATA, the server grants send credit with
// CONTINUE, and either side ends a stream with CLOSE(reason).
package wisp

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	pktConnect  = 0x01
	pktData     = 0x02
	pktContinue = 0x03
	pktClose    = 0x04

	streamTCP = 0x01
	streamUDP = 0x02

	// Close reasons from the Wisp spec.
	ReasonUnknown         = 0x01
	ReasonVoluntary       = 0x02
	ReasonNetworkError    = 0x03
	ReasonUnreachableHost = 0x42
	ReasonNoResponse      = 0x43
	ReasonConnRefused     = 0x44
	ReasonHostBlocked     = 0x48
	ReasonConnThrottled   = 0x49

	// bufferSize is the send credit granted to the client per stream.
	bufferSize = 128
	// maxFrame bounds one WebSocket message (5 header bytes + payload).
	maxFrame = 1<<20 + 5
	readSize = 32 * 1024
)

// Handler relays Wisp streams. The zero value is usable; New sets the defaults.
type Handler struct {
	// Dial opens the destination. Defaults to a net.Dialer with a 10s timeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	// AllowPrivate permits loopback, link-local and RFC1918 destinations.
	// Off by default so the relay cannot be used to reach the host's own network.
	AllowPrivate bool
	// AllowHosts lists host:port destinations exempt from the private-address
	// block, so the sandbox can call the application's own API.
	AllowHosts []string
	// MaxStreams caps concurrent streams per WebSocket connection.
	MaxStreams int
	// CheckOrigin decides whether a browser origin may connect. Defaults to
	// same-host only.
	CheckOrigin func(r *http.Request) bool
}

func New() *Handler {
	return &Handler{MaxStreams: 64}
}

func (h *Handler) dial(ctx context.Context, network, addr string) (net.Conn, error) {
	if h.Dial != nil {
		return h.Dial(ctx, network, addr)
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	return d.DialContext(ctx, network, addr)
}

func (h *Handler) maxStreams() int {
	if h.MaxStreams <= 0 {
		return 64
	}
	return h.MaxStreams
}

func sameHost(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	o := strings.TrimPrefix(strings.TrimPrefix(origin, "https://"), "http://")
	return strings.EqualFold(o, r.Host)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	check := h.CheckOrigin
	if check == nil {
		check = sameHost
	}
	up := websocket.Upgrader{CheckOrigin: check, ReadBufferSize: readSize, WriteBufferSize: readSize}
	ws, err := up.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{h: h, ws: ws, streams: map[uint32]*stream{}}
	c.run(r.Context())
}

type conn struct {
	h       *Handler
	ws      *websocket.Conn
	wmu     sync.Mutex
	smu     sync.Mutex
	streams map[uint32]*stream
}

func (c *conn) send(typ byte, id uint32, payload []byte) error {
	frame := make([]byte, 5+len(payload))
	frame[0] = typ
	binary.LittleEndian.PutUint32(frame[1:5], id)
	copy(frame[5:], payload)
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.ws.WriteMessage(websocket.BinaryMessage, frame)
}

func (c *conn) sendContinue(id uint32, remaining uint32) error {
	var p [4]byte
	binary.LittleEndian.PutUint32(p[:], remaining)
	return c.send(pktContinue, id, p[:])
}

func (c *conn) sendClose(id uint32, reason byte) error {
	return c.send(pktClose, id, []byte{reason})
}

func (c *conn) run(ctx context.Context) {
	defer c.ws.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	c.ws.SetReadLimit(maxFrame)
	c.ws.SetPongHandler(func(string) error { c.ws.SetReadDeadline(time.Now().Add(90 * time.Second)); return nil })
	c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
	if err := c.sendContinue(0, bufferSize); err != nil {
		return
	}
	go c.pinger(ctx)
	defer c.closeAll()
	for {
		mt, frame, err := c.ws.ReadMessage()
		if err != nil {
			return
		}
		c.ws.SetReadDeadline(time.Now().Add(90 * time.Second))
		if mt != websocket.BinaryMessage || len(frame) < 5 {
			continue
		}
		typ, id, payload := frame[0], binary.LittleEndian.Uint32(frame[1:5]), frame[5:]
		switch typ {
		case pktConnect:
			c.connect(ctx, id, payload)
		case pktData:
			if s := c.get(id); s != nil {
				s.write(payload)
			}
		case pktClose:
			if s := c.remove(id); s != nil {
				s.close()
			}
		}
	}
}

func (c *conn) pinger(ctx context.Context) {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			c.wmu.Lock()
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.wmu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *conn) get(id uint32) *stream {
	c.smu.Lock()
	defer c.smu.Unlock()
	return c.streams[id]
}

func (c *conn) remove(id uint32) *stream {
	c.smu.Lock()
	defer c.smu.Unlock()
	s := c.streams[id]
	delete(c.streams, id)
	return s
}

func (c *conn) closeAll() {
	c.smu.Lock()
	all := make([]*stream, 0, len(c.streams))
	for _, s := range c.streams {
		all = append(all, s)
	}
	c.streams = map[uint32]*stream{}
	c.smu.Unlock()
	for _, s := range all {
		s.close()
	}
}

func (c *conn) connect(ctx context.Context, id uint32, payload []byte) {
	if len(payload) < 3 {
		c.sendClose(id, ReasonUnknown)
		return
	}
	kind := payload[0]
	port := binary.LittleEndian.Uint16(payload[1:3])
	host := strings.TrimSpace(string(payload[3:]))
	network := "tcp"
	if kind == streamUDP {
		network = "udp"
	} else if kind != streamTCP {
		c.sendClose(id, ReasonUnknown)
		return
	}
	if host == "" || port == 0 {
		c.sendClose(id, ReasonUnreachableHost)
		return
	}
	c.smu.Lock()
	if _, dup := c.streams[id]; dup || len(c.streams) >= c.h.maxStreams() {
		c.smu.Unlock()
		c.sendClose(id, ReasonConnThrottled)
		return
	}
	s := &stream{c: c, id: id, pending: make(chan []byte, bufferSize)}
	c.streams[id] = s
	c.smu.Unlock()

	go func() {
		if !c.h.AllowPrivate && !c.h.allowedHost(host, port) && hostBlocked(host) {
			c.finish(s, ReasonHostBlocked)
			return
		}
		nc, err := c.h.dial(ctx, network, net.JoinHostPort(host, strconv.Itoa(int(port))))
		if err != nil {
			c.finish(s, dialReason(err))
			return
		}
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			nc.Close()
			return
		}
		s.nc = nc
		s.mu.Unlock()
		go s.pump()
		s.readLoop(network == "udp")
	}()
}

// finish closes a stream the server gave up on and tells the client why.
func (c *conn) finish(s *stream, reason byte) {
	if c.remove(s.id) != nil {
		s.close()
		c.sendClose(s.id, reason)
	}
}

func dialReason(err error) byte {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return ReasonNoResponse
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "refused"):
		return ReasonConnRefused
	case strings.Contains(msg, "no such host"), strings.Contains(msg, "unreachable"):
		return ReasonUnreachableHost
	}
	return ReasonNetworkError
}

func (h *Handler) allowedHost(host string, port uint16) bool {
	target := net.JoinHostPort(strings.ToLower(host), strconv.Itoa(int(port)))
	for _, a := range h.AllowHosts {
		if strings.EqualFold(a, target) || strings.EqualFold(a, host) {
			return true
		}
	}
	return false
}

// hostBlocked resolves the host and refuses anything that is not a public
// unicast address, so the relay never reaches loopback or the LAN.
func hostBlocked(host string) bool {
	if strings.EqualFold(host, "localhost") || strings.HasSuffix(strings.ToLower(host), ".localhost") {
		return true
	}
	ips := []net.IP{}
	if ip := net.ParseIP(host); ip != nil {
		ips = append(ips, ip)
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return false // the dial will report the resolution failure
		}
		for _, a := range addrs {
			ips = append(ips, a.IP)
		}
	}
	for _, ip := range ips {
		if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return true
		}
	}
	return false
}

type stream struct {
	c       *conn
	id      uint32
	mu      sync.Mutex
	nc      net.Conn
	closed  bool
	pending chan []byte
}

// write queues client data for the destination. The channel holds bufferSize
// packets, matching the credit granted by CONTINUE.
func (s *stream) write(p []byte) {
	buf := make([]byte, len(p))
	copy(buf, p)
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if closed {
		return
	}
	select {
	case s.pending <- buf:
	default:
		slog.Warn("wisp: client overran stream buffer", "stream", s.id)
		s.c.finish(s, ReasonConnThrottled)
	}
}

// pump drains queued client data into the destination and refills credit.
func (s *stream) pump() {
	for p := range s.pending {
		s.mu.Lock()
		nc := s.nc
		s.mu.Unlock()
		if nc == nil {
			return
		}
		nc.SetWriteDeadline(time.Now().Add(60 * time.Second))
		if _, err := nc.Write(p); err != nil {
			s.c.finish(s, ReasonNetworkError)
			return
		}
		if err := s.c.sendContinue(s.id, bufferSize); err != nil {
			return
		}
	}
}

func (s *stream) readLoop(datagram bool) {
	buf := make([]byte, readSize)
	if datagram {
		buf = make([]byte, 64*1024)
	}
	for {
		s.mu.Lock()
		nc := s.nc
		s.mu.Unlock()
		if nc == nil {
			return
		}
		n, err := nc.Read(buf)
		if n > 0 {
			if werr := s.c.send(pktData, s.id, buf[:n]); werr != nil {
				s.c.remove(s.id)
				s.close()
				return
			}
		}
		if err != nil {
			reason := byte(ReasonNetworkError)
			if errors.Is(err, io.EOF) {
				reason = ReasonVoluntary
			}
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if !closed {
				s.c.finish(s, reason)
			}
			return
		}
	}
}

func (s *stream) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	nc := s.nc
	close(s.pending)
	s.mu.Unlock()
	if nc != nil {
		nc.Close()
	}
}
