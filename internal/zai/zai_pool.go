package zai

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The patchright transport: a pool of warm headed browsers under xvfb-run,
// each running ext/zai_browser.py and serving one turn at a time. The pool
// size is the number of concurrent turns; Claude Code subagent fan-out needs
// several, and a single serialized browser starves it.

const (
	// zaiBrowserTurnSeconds bounds one turn inside the script. It is long on
	// purpose: on a final-report turn GLM reasons for minutes (40KB live), and
	// stall detection, not this deadline, is what guards liveness.
	zaiBrowserTurnSeconds = 600
	// Go allows 30s more so the script can answer with a clean error first.
	zaiBrowserTurnTimeout = (zaiBrowserTurnSeconds + 30) * time.Second
	// zaiRequestTimeout covers queueing for a worker plus one turn.
	zaiRequestTimeout = 15 * time.Minute
	// zaiBrowserStartTimeout bounds launch plus first page load; a cold
	// Mullvad relay took 16s to load the page, live.
	zaiBrowserStartTimeout = 90 * time.Second
)

// zaiBrowserTurn is one stdin line for the script.
type zaiBrowserTurn struct {
	Model           string `json:"model"`
	Prompt          string `json:"prompt"`
	EnableThinking  bool   `json:"enable_thinking"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Timeout         int    `json:"timeout"`
	Stream          bool   `json:"stream,omitempty"`
}

// zaiBrowserResult is one stdout line from the script.
type zaiBrowserResult struct {
	Delta    *string `json:"delta"` // a streamed text fragment, not a result
	Think    *string `json:"think"` // a streamed thinking fragment, not a result
	Streamed bool    `json:"-"`     // deltas preceded this result
	OK       bool    `json:"ok"`
	Answer   string  `json:"answer"`
	Thinking string  `json:"thinking"`
	Error    string  `json:"error"`
	Status   int     `json:"status"`
	// RelayBlocked: the captcha hosts are unreachable from this exit IP.
	RelayBlocked bool `json:"relay_blocked"`
	// Rotated: the worker's account answered 429 and was swapped; nothing was
	// generated, so the turn can be resent once.
	Rotated bool `json:"-"`
	Usage   struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func zaiRunBrowserTransport(ctx context.Context, upstreamModel, prompt string, enableThinking bool, effort string) (*zaiStreamResult, error) {
	onDelta := zaiDeltaFunc(ctx)
	turn, _ := json.Marshal(zaiBrowserTurn{Model: upstreamModel, Prompt: prompt, EnableThinking: enableThinking, ReasoningEffort: effort,
		Timeout: zaiBrowserTurnSeconds, Stream: onDelta != nil})
	out, err := zaiGetBrowserPool().send(ctx, turn, onDelta)
	// A relay block, a WAF 405, a busy account or the model at capacity all
	// fail before z.ai generates anything, so resending costs no duplicate
	// output. The worker has already moved relay or account where that helps;
	// keep going until the client's own deadline, with a short backoff so a
	// capacity wait does not hammer the next relay.
	started := time.Now()
	for attempt := 1; err == nil && !out.OK && !out.Streamed && zaiRetryable(out); attempt++ {
		wait := zaiRetryBackoff(attempt, out)
		if dl, ok := ctx.Deadline(); ok && time.Until(dl) < wait+zaiRetryMinRemaining {
			break
		}
		logf("event=zai_turn_retry attempt=%d after=%s elapsed=%s status=%d err=%q", attempt, wait, time.Since(started).Round(time.Second), out.Status, truncate(out.Error, 120))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, fmt.Errorf("zai browser transport: %w", ctx.Err())
		}
		out, err = zaiGetBrowserPool().send(ctx, turn, onDelta)
	}
	if err != nil {
		return nil, fmt.Errorf("zai browser transport: %w", err)
	}
	if !out.OK {
		return nil, zaiTransportError(out)
	}
	return &zaiStreamResult{
		Thinking:         out.Thinking,
		Answer:           out.Answer,
		PromptTokens:     out.Usage.PromptTokens,
		CompletionTokens: out.Usage.CompletionTokens,
	}, nil
}

// zaiRetryMinRemaining is how much of the client's deadline a retry must
// leave for the turn itself; below that the last error is returned.
const zaiRetryMinRemaining = 90 * time.Second

// zaiRetryable reports whether a failed worker result can be resent without
// duplicating output: nothing was generated for these.
func zaiRetryable(out *zaiBrowserResult) bool {
	switch {
	case out.RelayBlocked, out.Rotated:
		return true
	case out.Status == http.StatusMethodNotAllowed, out.Status == http.StatusTooManyRequests:
		return true
	case strings.Contains(out.Error, "MODEL_CONCURRENCY_LIMIT"), strings.Contains(out.Error, "at capacity"):
		return true
	case strings.Contains(out.Error, "never fired"), strings.Contains(out.Error, "blocked by upstream WAF"):
		return true
	}
	return false
}

// zaiRetryBackoff: a relay or account swap already took a few seconds, so a
// WAF/relay retry goes straight back out; a capacity wait backs off 5s to 30s.
func zaiRetryBackoff(attempt int, out *zaiBrowserResult) time.Duration {
	if strings.Contains(out.Error, "MODEL_CONCURRENCY_LIMIT") || strings.Contains(out.Error, "at capacity") || out.Status == http.StatusTooManyRequests {
		return min(30*time.Second, 5*time.Second*time.Duration(attempt))
	}
	if attempt <= 3 {
		return 500 * time.Millisecond
	}
	return min(15*time.Second, 2*time.Second*time.Duration(attempt-3))
}

// zaiTransportError maps a failed worker result to the error clients see. The
// raw worker text stays in Detail for the failure dump; Public is plain.
func zaiTransportError(out *zaiBrowserResult) *zaiUpstreamError {
	e := &zaiUpstreamError{Status: http.StatusBadGateway, Detail: "zai browser transport: " + out.Error,
		Public: "The upstream request failed. Please retry."}
	switch {
	case out.Status == http.StatusTooManyRequests, strings.Contains(out.Error, "MODEL_CONCURRENCY_LIMIT"), strings.Contains(out.Error, "at capacity"):
		e.Status, e.Code = http.StatusTooManyRequests, "server_busy"
		e.Public = "The model is at capacity right now. Retry in about a minute."
	case out.Status == http.StatusMethodNotAllowed, strings.Contains(out.Error, "blocked by upstream WAF"):
		e.Code = "upstream_refused"
		e.Public = "The upstream refused this request from our side. Please retry."
	case strings.Contains(out.Error, "timed out"), strings.Contains(out.Error, "never fired"):
		e.Status, e.Code = http.StatusGatewayTimeout, "upstream_timeout"
		e.Public = "The model took too long to answer. Please retry."
	}
	return e
}

type zaiBrowserPool struct {
	workers chan *zaiBrowserWorker
}

var (
	zaiBrowserPoolInstance *zaiBrowserPool
	zaiBrowserPoolOnce     sync.Once
)

func zaiGetBrowserPool() *zaiBrowserPool {
	zaiBrowserPoolOnce.Do(func() {
		size := zaiBrowserPoolSize()
		proxies, mullvad := zaiBrowserProxies(size)
		zaiBrowserPoolInstance = newZaiBrowserPool(size, proxies, mullvad)
	})
	return zaiBrowserPoolInstance
}

// zaiBrowserPoolSize reads ZAI_BROWSER_POOL_SIZE (default 3, max 16).
func zaiBrowserPoolSize() int {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZAI_BROWSER_POOL_SIZE")))
	if err != nil || n <= 0 {
		n = 3
	}
	return min(n, 16)
}

// zaiBrowserProxies reads ZAI_BROWSER_PROXY: a comma-separated proxy list, or
// "mullvad" for one live Mullvad SOCKS5 relay per worker. Workers take entries
// round-robin so concurrent turns leave from different exit IPs; Aliyun's WAF
// blocks one IP that sends many turns at once (HTTP 405, live).
func zaiBrowserProxies(size int) (proxies []string, mullvad bool) {
	raw := strings.TrimSpace(os.Getenv("ZAI_BROWSER_PROXY"))
	if strings.EqualFold(raw, "mullvad") {
		getLiveMullvadSocks5Proxy() // loads the relay cache if empty
		mullvadCachedRelaysMu.RLock()
		relays := append([]mullvadRelay(nil), mullvadCachedRelays...)
		mullvadCachedRelaysMu.RUnlock()
		rand.Shuffle(len(relays), func(i, j int) { relays[i], relays[j] = relays[j], relays[i] })
		for _, r := range relays[:min(size, len(relays))] {
			proxies = append(proxies, fmt.Sprintf("socks5://%s:%d", r.SocksName, r.SocksPort))
		}
		switch {
		case len(proxies) == 0:
			logf("event=zai_mullvad_unavailable browsers=direct")
		case len(proxies) < size:
			logf("event=zai_mullvad_shared_relays relays=%d workers=%d", len(proxies), size)
		}
		return proxies, len(proxies) > 0
	}
	for _, p := range strings.Split(raw, ",") {
		if p = strings.TrimSpace(p); p != "" {
			proxies = append(proxies, p)
		}
	}
	return proxies, false
}

func newZaiBrowserPool(size int, proxies []string, mullvad bool) *zaiBrowserPool {
	p := &zaiBrowserPool{workers: make(chan *zaiBrowserWorker, size)}
	for i := 0; i < size; i++ {
		w := &zaiBrowserWorker{id: i, mullvad: mullvad}
		if len(proxies) > 0 {
			w.proxy = proxies[i%len(proxies)]
		}
		p.workers <- w
	}
	return p
}

// prewarm starts every worker's browser in parallel, so the first burst of
// turns (a subagent fan-out) does not pay a 10-30s cold start each.
func (p *zaiBrowserPool) prewarm() {
	for i := 0; i < cap(p.workers); i++ {
		w := <-p.workers
		go func() {
			defer func() { p.workers <- w }()
			if err := w.ensure(); err != nil {
				logf("event=zai_browser_prewarm_error worker=%d err=%v", w.id, err)
			}
		}()
	}
}

// send waits for a free worker (bounded by ctx), then runs one turn with its
// own deadline so time spent queued never eats into the turn budget.
func (p *zaiBrowserPool) send(ctx context.Context, turn []byte, onDelta func(string)) (*zaiBrowserResult, error) {
	var w *zaiBrowserWorker
	select {
	case w = <-p.workers:
	case <-ctx.Done():
		return nil, fmt.Errorf("waiting for a browser: %w", ctx.Err())
	}
	defer func() { p.workers <- w }()
	turnCtx, cancel := context.WithTimeout(ctx, zaiBrowserTurnTimeout)
	defer cancel()
	return w.send(turnCtx, turn, onDelta)
}

// zaiBrowserWorker is one warm browser process. Only the goroutine holding it
// (taken from the pool channel) touches stdin/stdout; aliveMu guards the
// fields the exit reaper also writes.
type zaiBrowserWorker struct {
	id         int
	proxy      string
	mullvad    bool        // proxy came from ZAI_BROWSER_PROXY=mullvad and may be rotated
	account    *zaiAccount // leased from the account pool; nil means the shared ZAI_TOKEN or a guest
	stdin      io.WriteCloser
	stdout     *bufio.Scanner
	stdoutFile *os.File

	aliveMu sync.Mutex
	cmd     *exec.Cmd
	alive   bool
}

func (w *zaiBrowserWorker) isAlive() bool {
	w.aliveMu.Lock()
	defer w.aliveMu.Unlock()
	return w.alive
}

func (w *zaiBrowserWorker) ensure() error {
	// A worker that started as a guest because no account was free yet moves
	// onto one as soon as the pool has it; the respawn costs a few seconds once.
	if w.isAlive() && w.account == nil {
		if pool := zaiGetAccountPool(); pool != nil && pool.hasAccounts() {
			if a := pool.lease(context.Background(), w.id); a != nil {
				w.account = a
				w.kill()
			}
		}
	}
	if w.isAlive() {
		return nil
	}
	err := w.spawn()
	// A Mullvad relay that is down fails every start (ERR_SOCKS_CONNECTION_FAILED,
	// live); respawning on it would fail every turn this worker takes.
	for i := 0; err != nil && w.mullvad && i < 2; i++ {
		next := getLiveMullvadSocks5Proxy()
		if next == "" || next == w.proxy {
			break
		}
		logf("event=zai_browser_proxy_rotated worker=%d reason=start_failed", w.id)
		w.proxy = next
		err = w.spawn()
	}
	// A token z.ai no longer accepts retires its account; the worker takes another.
	for i := 0; err != nil && w.account != nil && strings.Contains(err.Error(), "rejected the signed-in token") && i < 2; i++ {
		if pool := zaiGetAccountPool(); pool != nil {
			pool.dead(context.Background(), w.account, "token_rejected")
		}
		w.account = nil
		err = w.spawn()
	}
	return err
}

func (w *zaiBrowserWorker) send(ctx context.Context, turn []byte, onDelta func(string)) (*zaiBrowserResult, error) {
	if err := w.ensure(); err != nil {
		return nil, err
	}
	if _, err := w.stdin.Write(append(turn, '\n')); err != nil {
		// The browser died between turns and the reaper has not marked it yet.
		w.kill()
		if err := w.spawn(); err != nil {
			return nil, err
		}
		if _, err := w.stdin.Write(append(turn, '\n')); err != nil {
			w.kill()
			return nil, fmt.Errorf("write turn: %w", err)
		}
	}
	var out zaiBrowserResult
	streamed := false
	for {
		line, err := w.readResult(ctx)
		if err != nil {
			return nil, err
		}
		out = zaiBrowserResult{}
		if err := json.Unmarshal([]byte(line), &out); err != nil {
			w.kill()
			return nil, fmt.Errorf("bad result line %q: %w", truncate(line, 200), err)
		}
		if out.Think != nil {
			if onThink := zaiThinkingFunc(ctx); onThink != nil {
				onThink(*out.Think)
			}
			continue
		}
		if out.Delta == nil {
			out.Streamed = streamed
			break
		}
		streamed = true
		if onDelta != nil {
			onDelta(*out.Delta)
		}
	}
	if !out.OK && (out.Status == http.StatusMethodNotAllowed || out.RelayBlocked) && w.mullvad {
		// WAF block page or unreachable captcha: this exit IP is unusable,
		// so move the worker to a fresh one.
		w.rotateProxy()
	}
	if !out.OK && out.Status == http.StatusMethodNotAllowed && !out.Streamed {
		// The WAF ate the turn before anything was generated. With Mullvad the
		// worker just rotated onto a fresh relay; without it we still recycle
		// the browser so a sticky block page doesn't poison the session.
		// Either way the resend below costs no duplicate generation.
		if !w.mullvad {
			w.kill()
		}
		out.Rotated = true
	}
	if pool := zaiGetAccountPool(); pool != nil && w.account != nil {
		if !out.OK && out.Status == http.StatusTooManyRequests {
			// The account hit its concurrency cap: cool it down and respawn on another.
			pool.busy(context.Background(), w.account)
			w.account = nil
			w.kill()
			out.Rotated = true
		} else if out.OK {
			pool.ok(context.Background(), w.account)
		}
	}
	return &out, nil
}

// readResult returns the next JSON line from the script, logging the log lines
// before it (xvfb-run merges the script's stderr into stdout). On ctx expiry
// or a dead process the worker is killed: the protocol is out of sync and
// only a restart recovers it.
func (w *zaiBrowserWorker) readResult(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	done := make(chan result, 1)
	go func() {
		for w.stdout.Scan() {
			line := strings.TrimSpace(w.stdout.Text())
			if strings.HasPrefix(line, "{") {
				done <- result{line: line}
				return
			}
			if debugEnabled && line != "" {
				logf("event=zai_browser_log worker=%d line=%q", w.id, truncate(line, 300))
			}
		}
		err := w.stdout.Err()
		if err == nil {
			err = errors.New("browser process exited")
		}
		done <- result{err: err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			w.kill()
		}
		return r.line, r.err
	case <-ctx.Done():
		w.kill()
		<-done
		return "", fmt.Errorf("browser turn: %w", ctx.Err())
	}
}

// rotateProxy restarts the worker on another Mullvad relay after a WAF block.
func (w *zaiBrowserWorker) rotateProxy() {
	next := getLiveMullvadSocks5Proxy()
	if next == "" || next == w.proxy {
		return
	}
	logf("event=zai_browser_proxy_rotated worker=%d", w.id)
	w.proxy = next
	w.kill()
}

func zaiBrowserScriptPath() string {
	if v := strings.TrimSpace(os.Getenv("ZAI_BROWSER_SCRIPT")); v != "" {
		return v
	}
	candidates := []string{filepath.Join("ext", "zai_browser.py")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "ext", "zai_browser.py"))
	}
	// Under `go run` / `go test` the binary lives in a temp dir: fall back to the repo root this file sits in.
	if _, file, _, ok := runtime.Caller(0); ok {
		candidates = append(candidates, filepath.Join(filepath.Dir(file), "..", "..", "ext", "zai_browser.py"))
	}
	for _, cand := range candidates {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return candidates[0]
}

func (w *zaiBrowserWorker) spawn() error {
	xvfb, err := exec.LookPath("xvfb-run")
	if err != nil {
		return errors.New("xvfb-run not found (the browser must run headed, never in a visible window)")
	}
	script, err := filepath.Abs(zaiBrowserScriptPath())
	if err != nil {
		return err
	}
	cmd := exec.Command(xvfb, "-a", zaiPythonCmd(), script)
	// Own process group so kill() also reaps python and chromium under xvfb-run.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	// Our own pipe, not StdoutPipe: Wait closes a StdoutPipe once the process
	// exits, and the reaper below would then drop a last line (the startup
	// error) before readResult reads it. EOF comes when the child's end closes.
	stdout, childOut, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	cmd.Stdout = childOut
	err = cmd.Start()
	_ = childOut.Close()
	if err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return err
	}
	// Only the goroutine holding the worker reads stdout, and no read is in
	// flight between turns, so the previous process's pipe can close here.
	if w.stdoutFile != nil {
		_ = w.stdoutFile.Close()
	}
	if w.stdin != nil {
		_ = w.stdin.Close()
	}
	w.stdin = stdin
	w.stdoutFile = stdout
	w.stdout = bufio.NewScanner(stdout)
	w.stdout.Buffer(make([]byte, 0, 256*1024), 8*1024*1024)
	w.aliveMu.Lock()
	w.cmd, w.alive = cmd, true
	w.aliveMu.Unlock()
	// Reap from the start, so every failure path below leaves no zombie.
	reaped := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(reaped)
		w.aliveMu.Lock()
		if w.cmd == cmd {
			w.cmd, w.alive = nil, false
		}
		w.aliveMu.Unlock()
		logf("event=zai_browser_exited worker=%d pid=%d", w.id, cmd.Process.Pid)
	}()

	// The pipe buffer takes this line without blocking; the process only reads
	// it once Python starts, which readResult below bounds.
	token := zaiAccountToken()
	if pool := zaiGetAccountPool(); pool != nil {
		if w.account == nil {
			w.account = pool.lease(context.Background(), w.id)
		}
		if w.account != nil {
			token = w.account.Token
		}
	}
	session, _ := json.Marshal(map[string]any{"headless": false, "proxy": w.proxy, "token": token})
	if _, err := stdin.Write(append(session, '\n')); err != nil {
		w.kill()
		return err
	}

	if zaiAfterReapHook != nil {
		zaiAfterReapHook(reaped)
	}
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), zaiBrowserStartTimeout)
	defer cancel()
	line, err := w.readResult(ctx)
	var ready struct {
		Ready bool   `json:"ready"`
		Error string `json:"error"`
	}
	if err == nil {
		if jerr := json.Unmarshal([]byte(line), &ready); jerr != nil {
			ready.Error = line
		}
	}
	if !ready.Ready {
		w.kill()
		if err == nil {
			err = fmt.Errorf("browser failed to start: %s", truncate(ready.Error, 200))
		}
		return err
	}
	logf("event=zai_browser_ready worker=%d pid=%d proxied=%t elapsed=%s", w.id, cmd.Process.Pid, w.proxy != "", time.Since(started).Round(time.Millisecond))
	return nil
}

// zaiAfterReapHook lets a test hold spawn until the child has been reaped.
var zaiAfterReapHook func(reaped <-chan struct{})

func (w *zaiBrowserWorker) kill() {
	w.aliveMu.Lock()
	defer w.aliveMu.Unlock()
	w.alive = false
	cmd := w.cmd
	// Signal(0) goes through a pidfd, so it fails once the reaper has waited:
	// the pid may then belong to an unrelated process and must not be signalled.
	if cmd != nil && cmd.Process != nil && cmd.Process.Signal(syscall.Signal(0)) == nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
