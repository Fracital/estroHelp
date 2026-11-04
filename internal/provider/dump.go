package provider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"elichat/internal/auth"
)

// Every upstream failure is written out in full: the request we sent, the
// upstream's status, headers and body, and who sent it. A client only ever
// sees the mapped envelope, so without this there is nothing to debug from.
// Files live under ERROR_DUMP_DIR (default /var/log/elichat/failed) and are
// pruned after dumpKeep.

const dumpKeep = 7 * 24 * time.Hour

var (
	dumpDir       = envOr("ERROR_DUMP_DIR", "/var/log/elichat/failed")
	lastDumpPrune atomic.Int64
)

func envOr(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

type failureDump struct {
	Time             time.Time       `json:"time"`
	User             string          `json:"user"`
	UserID           int             `json:"user_id"`
	Dialect          string          `json:"dialect"`
	ClientPath       string          `json:"client_path"`
	ClientHeaders    map[string]any  `json:"client_headers"`
	UpstreamURL      string          `json:"upstream_url"`
	UpstreamStatus   int             `json:"upstream_status"`
	UpstreamHeaders  http.Header     `json:"upstream_headers,omitempty"`
	UpstreamBody     string          `json:"upstream_body"`
	TransportError   string          `json:"transport_error,omitempty"`
	Request          json.RawMessage `json:"request"`
	RequestBytes     int             `json:"request_bytes"`
	RequestTruncated bool            `json:"request_truncated,omitempty"`
}

// clientHeaderKeys are the request headers worth keeping: enough to match a
// client's own error report (its request ids) and to tell which tool sent it.
var clientHeaderKeys = []string{"User-Agent", "X-Request-Id", "X-Client-Request-Id", "X-Initiator", "Openai-Organization", "Anthropic-Version", "Anthropic-Beta", "HTTP-Referer", "X-Title", "Editor-Version", "Editor-Plugin-Version", "Copilot-Integration-Id", "Content-Length"}

// DumpFailure records an upstream failure to disk and logs where. It never
// fails the request; when the directory is unusable the summary goes to the log.
func DumpFailure(r *http.Request, dialect Dialect, upstreamURL string, reqBody []byte, status int, respHeaders http.Header, respBody []byte, transportErr error) {
	d := failureDump{
		Time: time.Now().UTC(), Dialect: string(dialect), ClientPath: r.URL.Path,
		ClientHeaders: map[string]any{}, UpstreamURL: upstreamURL,
		UpstreamStatus: status, UpstreamHeaders: respHeaders, UpstreamBody: string(respBody),
		RequestBytes: len(reqBody),
	}
	if u := auth.UserFrom(r.Context()); u != nil {
		d.User, d.UserID = u.Username, u.ID
	}
	for _, k := range clientHeaderKeys {
		if v := r.Header.Get(k); v != "" {
			d.ClientHeaders[k] = v
		}
	}
	if transportErr != nil {
		d.TransportError = transportErr.Error()
	}
	if json.Valid(reqBody) {
		d.Request = reqBody
	} else {
		d.Request, _ = json.Marshal(string(reqBody))
	}

	name := d.Time.Format("20060102-150405") + "-" + safeName(d.User) + "-" + string(dialect) + "-" + itoa(status) + "-" + randomSuffix() + ".json"
	path := filepath.Join(dumpDir, name)
	attrs := []any{"path", path, "user", d.User, "dialect", dialect, "status", status, "request_bytes", len(reqBody), "upstream", truncateText(string(respBody), 2000)}
	if transportErr != nil {
		attrs = append(attrs, "err", transportErr)
	}
	if err := writeDump(path, d); err != nil {
		attrs = append(attrs, "dump_error", err, "request", truncateText(string(reqBody), 16<<10))
		slog.Error("upstream failure (dump not written)", attrs...)
		return
	}
	slog.Warn("upstream failure dumped", attrs...)
	pruneDumps()
}

func writeDump(path string, d failureDump) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	b, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o640)
}

// pruneDumps removes dumps older than dumpKeep, at most once every ten minutes.
func pruneDumps() {
	now := time.Now()
	last := lastDumpPrune.Load()
	if now.Unix()-last < 600 || !lastDumpPrune.CompareAndSwap(last, now.Unix()) {
		return
	}
	entries, err := os.ReadDir(dumpDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if now.Sub(info.ModTime()) > dumpKeep {
			os.Remove(filepath.Join(dumpDir, e.Name()))
		}
	}
}

func safeName(s string) string {
	if s == "" {
		return "anon"
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func randomSuffix() string {
	b := make([]byte, 3)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
