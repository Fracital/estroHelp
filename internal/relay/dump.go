package relay

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Every failure a client is told about is written out in full: the request as
// the relay forwarded it, the upstream's status, headers and body (or the
// relay's own reason), and who sent it. A client only ever sees the mapped
// envelope, so without this there is nothing to debug from. Files live under
// ERROR_DUMP_DIR (default /var/log/elichat/failed) and are pruned after dumpKeep.

const dumpKeep = 7 * 24 * time.Hour

var lastDumpPrune atomic.Int64

// dumpDir is read per call so a test or a restart with a new .env takes effect.
func dumpDir() string {
	if v := strings.TrimSpace(os.Getenv("ERROR_DUMP_DIR")); v != "" {
		return v
	}
	return "/var/log/elichat/failed"
}

// Failure is one dumped failure.
type Failure struct {
	Time            time.Time       `json:"time"`
	User            string          `json:"user"`
	UserID          int             `json:"user_id"`
	Provider        string          `json:"provider"`
	Dialect         string          `json:"dialect"`
	ClientPath      string          `json:"client_path"`
	ClientHeaders   map[string]any  `json:"client_headers"`
	UpstreamURL     string          `json:"upstream_url,omitempty"`
	UpstreamStatus  int             `json:"upstream_status"`
	UpstreamHeaders http.Header     `json:"upstream_headers,omitempty"`
	UpstreamBody    string          `json:"upstream_body"`
	Error           string          `json:"error,omitempty"`
	Request         json.RawMessage `json:"request"`
	RequestBytes    int             `json:"request_bytes"`
}

// clientHeaderKeys are the request headers worth keeping: enough to match a
// client's own error report (its request ids) and to tell which tool sent it.
var clientHeaderKeys = []string{"User-Agent", "X-Request-Id", "X-Client-Request-Id", "X-Initiator", "Openai-Organization", "Anthropic-Version", "Anthropic-Beta", "HTTP-Referer", "X-Title", "Editor-Version", "Editor-Plugin-Version", "Copilot-Integration-Id", "Content-Length"}

// DumpFailure records a failure to disk and logs where. It never fails the
// request; when the directory is unusable the summary goes to the log instead.
// user/userID come from the caller so this package stays free of auth.
func DumpFailure(r *http.Request, user string, userID int, provider, dialect, upstreamURL string, reqBody []byte, status int, respHeaders http.Header, respBody []byte, err error) {
	d := Failure{
		Time: time.Now().UTC(), User: user, UserID: userID, Provider: provider, Dialect: dialect, ClientPath: r.URL.Path,
		ClientHeaders: map[string]any{}, UpstreamURL: upstreamURL,
		UpstreamStatus: status, UpstreamHeaders: respHeaders, UpstreamBody: string(respBody),
		RequestBytes: len(reqBody),
	}
	for _, k := range clientHeaderKeys {
		if v := r.Header.Get(k); v != "" {
			d.ClientHeaders[k] = v
		}
	}
	if err != nil {
		d.Error = err.Error()
	}
	if json.Valid(reqBody) {
		d.Request = reqBody
	} else {
		d.Request, _ = json.Marshal(string(reqBody))
	}

	name := d.Time.Format("20060102-150405") + "-" + safeName(user) + "-" + safeName(provider) + "-" + safeName(dialect) + "-" + strconv.Itoa(status) + "-" + randomSuffix() + ".json"
	path := filepath.Join(dumpDir(), name)
	attrs := []any{"path", path, "user", user, "provider", provider, "dialect", dialect, "status", status, "request_bytes", len(reqBody), "upstream", truncateText(string(respBody), 2000)}
	if err != nil {
		attrs = append(attrs, "err", err)
	}
	if werr := writeDump(path, d); werr != nil {
		attrs = append(attrs, "dump_error", werr, "request", truncateText(string(reqBody), 16<<10))
		slog.Error("failure (dump not written)", attrs...)
		return
	}
	slog.Warn("failure dumped", attrs...)
	pruneDumps()
}

func writeDump(path string, d Failure) error {
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
	dir := dumpDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		if now.Sub(info.ModTime()) > dumpKeep {
			os.Remove(filepath.Join(dir, e.Name()))
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

func truncateText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
