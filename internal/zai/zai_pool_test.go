package zai

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeZaiBrowser speaks ext/zai_browser.py's stdio contract: a session line,
// then one JSON result per turn line, with log noise on stderr (merged into
// stdout by xvfb-run). It answers "<prompt>:<pid>:<proxy>"; prompt "hang" never
// answers and prompt "waf" returns the WAF block status.
const fakeZaiBrowser = `import json, os, sys, time
session = json.loads(sys.stdin.readline())
if "dead.relay" in (session.get("proxy") or ""):
    print(json.dumps({"error": "Page.goto: net::ERR_SOCKS_CONNECTION_FAILED"}), flush=True)
    sys.exit(1)
time.sleep(1)
sys.stderr.write("[zai_browser] event=ready\n"); sys.stderr.flush()
print(json.dumps({"ready": True}), flush=True)
for line in sys.stdin:
    p = json.loads(line)["prompt"]
    if p == "hang":
        time.sleep(600)
    if p == "waf":
        print(json.dumps({"ok": False, "status": 405, "error": "blocked"}), flush=True)
        continue
    if "blocked.relay" in (session.get("proxy") or ""):
        print(json.dumps({"ok": False, "relay_blocked": True, "error": "captcha network error"}), flush=True)
        continue
    time.sleep(1)
    print(json.dumps({"ok": True, "answer": "%s:%d:%s" % (p, os.getpid(), session.get("proxy") or "")}), flush=True)
`

func setupFakeZaiBrowser(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("xvfb-run"); err != nil {
		t.Skip("xvfb-run not installed")
	}
	script := filepath.Join(t.TempDir(), "fake_zai_browser.py")
	if err := os.WriteFile(script, []byte(fakeZaiBrowser), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZAI_BROWSER_SCRIPT", script)
}

func zaiTurn(prompt string) []byte {
	b, _ := json.Marshal(zaiBrowserTurn{Prompt: prompt, Timeout: 5})
	return b
}

func killPool(p *zaiBrowserPool) {
	for i := 0; i < cap(p.workers); i++ {
		(<-p.workers).kill()
	}
}

func TestZaiBrowserPoolPrewarmRunsTurnsConcurrently(t *testing.T) {
	setupFakeZaiBrowser(t)
	pool := newZaiBrowserPool(3, nil, false)
	defer killPool(pool)

	pool.prewarm()
	deadline := time.Now().Add(20 * time.Second)
	for {
		alive := 0
		for i := 0; i < 3; i++ {
			w := <-pool.workers
			if w.isAlive() {
				alive++
			}
			pool.workers <- w
		}
		if alive == 3 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("prewarm left %d of 3 workers alive", alive)
		}
		time.Sleep(50 * time.Millisecond)
	}

	start := time.Now()
	var wg sync.WaitGroup
	answers := make(chan string, 3)
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			out, err := pool.send(context.Background(), zaiTurn("x"), nil)
			if err != nil || !out.OK {
				t.Errorf("send: %v %+v", err, out)
				return
			}
			answers <- out.Answer
		}()
	}
	wg.Wait()
	close(answers)
	if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
		t.Fatalf("turns did not run concurrently on warm workers: %v", elapsed)
	}
	seen := map[string]bool{}
	for a := range answers {
		seen[a] = true
	}
	// Three sends overlap on at least two workers. A worker that finishes its
	// turn before the third send claims one may legitimately serve it, so
	// exactly three is not guaranteed on a slow or busy runner.
	if len(seen) < 2 {
		t.Fatalf("want turns spread over at least 2 worker processes, got %v", seen)
	}
}

func TestZaiBrowserPoolCancelKillsAndRespawns(t *testing.T) {
	setupFakeZaiBrowser(t)
	pool := newZaiBrowserPool(1, nil, false)
	defer killPool(pool)

	first, err := pool.send(context.Background(), zaiTurn("a"), nil)
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := pool.send(ctx, zaiTurn("hang"), nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("hung turn: want deadline error, got %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("cancel did not unblock promptly: %v", time.Since(start))
	}
	second, err := pool.send(context.Background(), zaiTurn("a"), nil)
	if err != nil {
		t.Fatalf("turn after cancel: %v", err)
	}
	if second.Answer == first.Answer {
		t.Fatalf("want a fresh process after kill; both answered %s", first.Answer)
	}
}

func TestZaiBrowserPoolAssignsProxiesRoundRobin(t *testing.T) {
	pool := newZaiBrowserPool(3, []string{"socks5://a:1080", "socks5://b:1080"}, false)
	var got []string
	for i := 0; i < 3; i++ {
		got = append(got, (<-pool.workers).proxy)
	}
	if strings.Join(got, ",") != "socks5://a:1080,socks5://b:1080,socks5://a:1080" {
		t.Fatalf("proxies = %v", got)
	}
}

func TestZaiBrowserProxiesParsesList(t *testing.T) {
	t.Setenv("ZAI_BROWSER_PROXY", " socks5://a:1080, socks5://b:1080 ,")
	proxies, mullvad := zaiBrowserProxies(3)
	if mullvad || strings.Join(proxies, ",") != "socks5://a:1080,socks5://b:1080" {
		t.Fatalf("proxies=%v mullvad=%v", proxies, mullvad)
	}
}

// The script's startup error is its last line before exit and must reach the
// caller even when the reaper's Wait runs first. With cmd.StdoutPipe, Wait
// closes the read end and the line is lost; the hook forces that order.
func TestZaiBrowserWorkerStartupErrorSurvivesReap(t *testing.T) {
	setupFakeZaiBrowser(t)
	zaiAfterReapHook = func(reaped <-chan struct{}) {
		select {
		case <-reaped:
		case <-time.After(10 * time.Second):
		}
	}
	t.Cleanup(func() { zaiAfterReapHook = nil })
	pool := newZaiBrowserPool(1, []string{"socks5://dead.relay:1080"}, false)
	defer killPool(pool)
	_, err := pool.send(context.Background(), zaiTurn("x"), nil)
	if err == nil || !strings.Contains(err.Error(), "ERR_SOCKS_CONNECTION_FAILED") {
		t.Fatalf("want the script's startup error, got %v", err)
	}
}

// The script's startup error is its last line before exit and must reach the
// caller.
func TestZaiBrowserWorkerReportsStartupError(t *testing.T) {
	setupFakeZaiBrowser(t)
	pool := newZaiBrowserPool(1, []string{"socks5://dead.relay:1080"}, false)
	defer killPool(pool)
	for i := 0; i < 5; i++ {
		_, err := pool.send(context.Background(), zaiTurn("x"), nil)
		if err == nil || !strings.Contains(err.Error(), "ERR_SOCKS_CONNECTION_FAILED") {
			t.Fatalf("run %d: want the script's startup error, got %v", i, err)
		}
	}
}

// A relay whose captcha hosts fail sends nothing upstream: the worker moves
// to a fresh relay and the same turn succeeds there.
func TestZaiBrowserTransportRetriesOnBlockedRelay(t *testing.T) {
	setupFakeZaiBrowser(t)
	mullvadCachedRelaysMu.Lock()
	old := mullvadCachedRelays
	mullvadCachedRelays = []mullvadRelay{{SocksName: "fresh.relay", SocksPort: 1080}}
	mullvadCachedRelaysMu.Unlock()
	t.Cleanup(func() { mullvadCachedRelaysMu.Lock(); mullvadCachedRelays = old; mullvadCachedRelaysMu.Unlock() })

	pool := newZaiBrowserPool(1, []string{"socks5://blocked.relay:1080"}, true)
	defer killPool(pool)
	zaiBrowserPoolOnce.Do(func() {})
	oldPool := zaiBrowserPoolInstance
	zaiBrowserPoolInstance = pool
	t.Cleanup(func() { zaiBrowserPoolInstance = oldPool })

	res, err := zaiRunBrowserTransport(context.Background(), "m", "x", false)
	if err != nil || !strings.HasSuffix(res.Answer, ":socks5://fresh.relay:1080") {
		t.Fatalf("turn not retried on a fresh relay: %v %+v", err, res)
	}
}

// A relay that is down fails every browser start; a Mullvad worker must move
// to another relay instead of respawning on it for every turn it takes.
func TestZaiBrowserWorkerRotatesRelayOnStartFailure(t *testing.T) {
	setupFakeZaiBrowser(t)
	mullvadCachedRelaysMu.Lock()
	old := mullvadCachedRelays
	mullvadCachedRelays = []mullvadRelay{{SocksName: "fresh.relay", SocksPort: 1080}}
	mullvadCachedRelaysMu.Unlock()
	t.Cleanup(func() { mullvadCachedRelaysMu.Lock(); mullvadCachedRelays = old; mullvadCachedRelaysMu.Unlock() })

	pool := newZaiBrowserPool(1, []string{"socks5://dead.relay:1080"}, true)
	defer killPool(pool)
	out, err := pool.send(context.Background(), zaiTurn("x"), nil)
	if err != nil || !strings.HasSuffix(out.Answer, ":socks5://fresh.relay:1080") {
		t.Fatalf("worker not moved off the dead relay: %v %+v", err, out)
	}
}

// A WAF block page means the exit IP is flagged: a Mullvad worker must move
// to a fresh relay and come back up on it.
func TestZaiBrowserWorkerRotatesRelayOnWAFBlock(t *testing.T) {
	setupFakeZaiBrowser(t)
	mullvadCachedRelaysMu.Lock()
	old := mullvadCachedRelays
	mullvadCachedRelays = []mullvadRelay{{SocksName: "fresh.relay", SocksPort: 1080}}
	mullvadCachedRelaysMu.Unlock()
	t.Cleanup(func() { mullvadCachedRelaysMu.Lock(); mullvadCachedRelays = old; mullvadCachedRelaysMu.Unlock() })

	pool := newZaiBrowserPool(1, []string{"socks5://flagged.relay:1080"}, true)
	defer killPool(pool)
	out, err := pool.send(context.Background(), zaiTurn("waf"), nil)
	if err != nil || out.Status != 405 {
		t.Fatalf("waf turn: %v %+v", err, out)
	}
	out, err = pool.send(context.Background(), zaiTurn("after"), nil)
	if err != nil || !strings.HasSuffix(out.Answer, ":socks5://fresh.relay:1080") {
		t.Fatalf("worker not moved to a fresh relay: %v %+v", err, out)
	}
}
