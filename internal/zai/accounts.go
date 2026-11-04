package zai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"elichat/internal/db"
)

// Account pool for the patchright transport, adapted from goldenticket's
// Notion account pool. z.ai caps concurrent generations per account, so one
// shared account across every browser worker (and across dev and prod) turns
// into 429 "concurrent conversation limit" answers. Each worker now leases its
// own provisioned account; a 429 cools that account down and moves the worker
// to another; a rejected token retires the account. A background filler keeps
// a target number of healthy accounts by running ext/zai_signup.py, which
// drives the real signup form (captcha included) and returns the account JWT.
// Accounts live in the zai_account table so restarts and other processes
// share them.

const (
	// zaiAccountRateCooldown is how long an account sits out after a 429.
	zaiAccountRateCooldown = 90 * time.Second
	// zaiAccountDeadStrikes retires an account that keeps answering 429 while
	// nothing else uses it; its cap is then shared with something we cannot see.
	zaiAccountDeadStrikes = 8
	zaiSignupMinGap       = 60 * time.Second
	zaiSignupBackoffMax   = 15 * time.Minute
	zaiSignupTimeout      = 6 * time.Minute
	zaiPoolFillInterval   = 20 * time.Second
)

type zaiAccount struct {
	ID            int
	Email         string
	Password      string
	Token         string
	UserID        string
	Status        string // active | dead
	Strikes       int
	CooldownUntil time.Time
	LastUsed      time.Time
}

func (a *zaiAccount) healthy(now time.Time) bool {
	return a != nil && a.Status == "active" && !now.Before(a.CooldownUntil)
}

// zaiAccountStore is the durable side of the pool; the Postgres implementation
// is used in production and a memory one in tests.
type zaiAccountStore interface {
	list(ctx context.Context) ([]*zaiAccount, error)
	insert(ctx context.Context, a *zaiAccount, name, mailProvider, mailPassword string) (int, error)
	update(ctx context.Context, a *zaiAccount) error
}

type zaiPGStore struct{ db *db.DB }

func (s *zaiPGStore) list(ctx context.Context) ([]*zaiAccount, error) {
	rows, err := s.db.Query(ctx, `SELECT id, email, password, token, user_id, status, strikes, coalesce(cooldown_until, 'epoch'), coalesce(last_used, 'epoch') FROM zai_account ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*zaiAccount
	for rows.Next() {
		a := &zaiAccount{}
		if err := rows.Scan(&a.ID, &a.Email, &a.Password, &a.Token, &a.UserID, &a.Status, &a.Strikes, &a.CooldownUntil, &a.LastUsed); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *zaiPGStore) insert(ctx context.Context, a *zaiAccount, name, mailProvider, mailPassword string) (int, error) {
	var id int
	err := s.db.QueryRow(ctx, `INSERT INTO zai_account (email, password, token, user_id, name, mail_provider, mail_password) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id`,
		a.Email, a.Password, a.Token, a.UserID, name, mailProvider, mailPassword).Scan(&id)
	return id, err
}

func (s *zaiPGStore) update(ctx context.Context, a *zaiAccount) error {
	_, err := s.db.Exec(ctx, `UPDATE zai_account SET status=$2, strikes=$3, cooldown_until=$4, last_used=$5 WHERE id=$1`,
		a.ID, a.Status, a.Strikes, nullTime(a.CooldownUntil), nullTime(a.LastUsed))
	return err
}

func nullTime(t time.Time) any {
	if t.IsZero() || t.Unix() <= 0 {
		return nil
	}
	return t
}

type zaiMemStore struct {
	mu   sync.Mutex
	rows []*zaiAccount
	next int
}

func (s *zaiMemStore) list(context.Context) ([]*zaiAccount, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*zaiAccount, 0, len(s.rows))
	for _, r := range s.rows {
		c := *r
		out = append(out, &c)
	}
	return out, nil
}

func (s *zaiMemStore) insert(_ context.Context, a *zaiAccount, _, _, _ string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next++
	c := *a
	c.ID = s.next
	if c.Status == "" {
		c.Status = "active"
	}
	s.rows = append(s.rows, &c)
	return c.ID, nil
}

func (s *zaiMemStore) update(_ context.Context, a *zaiAccount) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, r := range s.rows {
		if r.ID == a.ID {
			c := *a
			s.rows[i] = &c
			return nil
		}
	}
	return errors.New("no such account")
}

// zaiAccountPool is the in-process view: leases (which worker holds which
// account) are per process; status, strikes and cooldowns are persisted.
type zaiAccountPool struct {
	mu       sync.Mutex
	store    zaiAccountStore
	leased   map[int]int // account id -> worker id
	signup   func(ctx context.Context) (*zaiAccount, string, string, string, error)
	filling  bool
	lastTry  time.Time
	failures int
	target   int
}

var (
	zaiPoolMu sync.Mutex
	zaiPool   *zaiAccountPool
)

// SetDB enables the account pool on top of the given database and starts the
// filler. Call before Prewarm.
func SetDB(d *db.DB) {
	if d == nil || zaiEnvBool("ZAI_ACCOUNT_POOL", true) == false {
		return
	}
	p := &zaiAccountPool{store: &zaiPGStore{db: d}, leased: map[int]int{}, signup: zaiRunSignupScript, target: zaiAccountPoolTarget()}
	zaiPoolMu.Lock()
	zaiPool = p
	zaiPoolMu.Unlock()
	p.importShared(context.Background())
	if zaiTransport() == "patchright" {
		go p.fillerLoop()
	}
}

// importShared seeds an empty table with the account from ZAI_TOKEN or
// zai_config.json, so a deployment that had one shared account keeps it as
// the pool's first row and nothing changes until provisioning adds more.
func (p *zaiAccountPool) importShared(ctx context.Context) {
	token := zaiAccountToken()
	if token == "" {
		return
	}
	rows, err := p.store.list(ctx)
	if err != nil || len(rows) > 0 {
		return
	}
	email, userID := zaiJWTClaims(token)
	if email == "" {
		email = "shared@" + userID
	}
	if _, err := p.store.insert(ctx, &zaiAccount{Email: email, Token: token, UserID: userID, Status: "active"}, "shared", "config", ""); err != nil {
		logf("event=zai_account_import_error err=%v", err)
		return
	}
	logf("event=zai_account_imported email=%s", email)
}

// zaiJWTClaims reads the email and id claims of a z.ai JWT without verifying it.
func zaiJWTClaims(token string) (email, id string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return "", ""
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return "", ""
	}
	var c struct {
		Email string `json:"email"`
		ID    string `json:"id"`
	}
	json.Unmarshal(raw, &c)
	return c.Email, c.ID
}

func zaiAccountPoolTarget() int {
	if n, err := strconv.Atoi(strings.TrimSpace(os.Getenv("ZAI_ACCOUNT_POOL_TARGET"))); err == nil && n >= 0 {
		return n
	}
	return zaiBrowserPoolSize() + 1
}

func zaiGetAccountPool() *zaiAccountPool {
	zaiPoolMu.Lock()
	defer zaiPoolMu.Unlock()
	return zaiPool
}

// lease hands worker w the least recently used healthy account that no other
// worker holds, or nil when there is none (the caller then falls back to the
// shared ZAI_TOKEN account, or a guest). A miss asks the filler for more.
func (p *zaiAccountPool) lease(ctx context.Context, worker int) *zaiAccount {
	rows, err := p.store.list(ctx)
	if err != nil {
		logf("event=zai_account_list_error err=%v", err)
		return nil
	}
	now := time.Now()
	p.mu.Lock()
	defer p.mu.Unlock()
	var free []*zaiAccount
	for _, a := range rows {
		if _, held := p.leased[a.ID]; held || !a.healthy(now) {
			continue
		}
		free = append(free, a)
	}
	if len(free) == 0 {
		logf("event=zai_account_none worker=%d accounts=%d", worker, len(rows))
		go p.ensureFill()
		return nil
	}
	sort.Slice(free, func(i, j int) bool { return free[i].LastUsed.Before(free[j].LastUsed) })
	a := free[0]
	a.LastUsed = now
	p.leased[a.ID] = worker
	if err := p.store.update(ctx, a); err != nil {
		logf("event=zai_account_update_error id=%d err=%v", a.ID, err)
	}
	logf("event=zai_account_leased worker=%d account=%d email=%s", worker, a.ID, a.Email)
	return a
}

func (p *zaiAccountPool) release(id int) {
	p.mu.Lock()
	delete(p.leased, id)
	p.mu.Unlock()
}

// busy records a 429 on the account: it cools down and the worker moves on.
func (p *zaiAccountPool) busy(ctx context.Context, a *zaiAccount) {
	a.Strikes++
	a.CooldownUntil = time.Now().Add(zaiAccountRateCooldown)
	if a.Strikes >= zaiAccountDeadStrikes {
		a.Status = "dead"
	}
	if err := p.store.update(ctx, a); err != nil {
		logf("event=zai_account_update_error id=%d err=%v", a.ID, err)
	}
	logf("event=zai_account_busy account=%d strikes=%d status=%s", a.ID, a.Strikes, a.Status)
	p.release(a.ID)
	go p.ensureFill()
}

// ok clears strikes after a successful turn so an occasional 429 never adds up.
func (p *zaiAccountPool) ok(ctx context.Context, a *zaiAccount) {
	if a.Strikes == 0 {
		return
	}
	a.Strikes = 0
	if err := p.store.update(ctx, a); err != nil {
		logf("event=zai_account_update_error id=%d err=%v", a.ID, err)
	}
}

// dead retires an account whose token z.ai no longer accepts.
func (p *zaiAccountPool) dead(ctx context.Context, a *zaiAccount, reason string) {
	a.Status = "dead"
	if err := p.store.update(ctx, a); err != nil {
		logf("event=zai_account_update_error id=%d err=%v", a.ID, err)
	}
	logf("event=zai_account_dead account=%d email=%s reason=%s", a.ID, a.Email, reason)
	p.release(a.ID)
	go p.ensureFill()
}

func (p *zaiAccountPool) healthyCount(ctx context.Context) (healthy, total int) {
	rows, err := p.store.list(ctx)
	if err != nil {
		return 0, 0
	}
	now := time.Now()
	for _, a := range rows {
		if a.healthy(now) {
			healthy++
		}
		if a.Status == "active" {
			total++
		}
	}
	return healthy, total
}

func (p *zaiAccountPool) hasAccounts() bool {
	_, total := p.healthyCount(context.Background())
	return total > 0
}

func (p *zaiAccountPool) fillerLoop() {
	t := time.NewTicker(zaiPoolFillInterval)
	defer t.Stop()
	p.ensureFill()
	for range t.C {
		p.ensureFill()
	}
}

// ensureFill runs at most one signup at a time, paced by a minimum gap and an
// exponential backoff after failures, until healthy accounts reach the target.
func (p *zaiAccountPool) ensureFill() {
	if p.target <= 0 {
		return
	}
	p.mu.Lock()
	if p.filling {
		p.mu.Unlock()
		return
	}
	gap := zaiSignupMinGap
	if p.failures > 0 {
		gap = min(zaiSignupBackoffMax, zaiSignupMinGap<<min(p.failures, 6))
	}
	if time.Since(p.lastTry) < gap {
		p.mu.Unlock()
		return
	}
	p.filling = true
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.filling = false
		p.mu.Unlock()
	}()
	ctx := context.Background()
	healthy, total := p.healthyCount(ctx)
	if healthy >= p.target {
		return
	}
	p.mu.Lock()
	p.lastTry = time.Now()
	p.mu.Unlock()
	logf("event=zai_signup_start healthy=%d active=%d target=%d failures=%d", healthy, total, p.target, p.failures)
	sctx, cancel := context.WithTimeout(ctx, zaiSignupTimeout)
	defer cancel()
	a, name, provider, mailPass, err := p.signup(sctx)
	if err != nil {
		p.mu.Lock()
		p.failures++
		p.mu.Unlock()
		logf("event=zai_signup_failed err=%v", err)
		return
	}
	a.Status = "active"
	id, err := p.store.insert(ctx, a, name, provider, mailPass)
	if err != nil {
		logf("event=zai_signup_store_error err=%v", err)
		return
	}
	p.mu.Lock()
	p.failures = 0
	p.mu.Unlock()
	logf("event=zai_signup_ok account=%d email=%s healthy=%d target=%d", id, a.Email, healthy+1, p.target)
}

// zaiSignupScriptPath finds ext/zai_signup.py the way the browser script is found.
func zaiSignupScriptPath() string {
	if v := strings.TrimSpace(os.Getenv("ZAI_SIGNUP_SCRIPT")); v != "" {
		return v
	}
	candidates := []string{filepath.Join("ext", "zai_signup.py")}
	if exe, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(exe), "ext", "zai_signup.py"))
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return candidates[0]
}

// zaiRunSignupScript provisions one account through ext/zai_signup.py. The
// script's contract: one JSON line on stdin, event lines then one JSON result
// line on stdout (xvfb-run merges stderr into stdout, so the result is the last
// line that starts with "{").
func zaiRunSignupScript(ctx context.Context) (*zaiAccount, string, string, string, error) {
	xvfb, err := exec.LookPath("xvfb-run")
	if err != nil {
		return nil, "", "", "", errors.New("xvfb-run not found")
	}
	script, err := filepath.Abs(zaiSignupScriptPath())
	if err != nil {
		return nil, "", "", "", err
	}
	if _, err := os.Stat(script); err != nil {
		return nil, "", "", "", fmt.Errorf("signup script: %w", err)
	}
	req := map[string]any{"timeout": int(zaiSignupTimeout.Seconds()) - 30}
	if proxy := strings.TrimSpace(os.Getenv("ZAI_SIGNUP_PROXY")); proxy != "" {
		req["proxy"] = proxy
	} else if strings.EqualFold(strings.TrimSpace(os.Getenv("ZAI_BROWSER_PROXY")), "mullvad") {
		if p := getLiveMullvadSocks5Proxy(); p != "" {
			req["proxy"] = p
		}
	}
	in, _ := json.Marshal(req)
	cmd := exec.CommandContext(ctx, xvfb, "-a", zaiPythonCmd(), script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.Stdin = bytes.NewReader(append(in, '\n'))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	var last string
	sc := bufio.NewScanner(bytes.NewReader(out.Bytes()))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "{"):
			last = line
		case strings.HasPrefix(line, "[zai_signup]"):
			logf("%s", strings.TrimPrefix(line, "[zai_signup] "))
		}
	}
	var res struct {
		OK           bool   `json:"ok"`
		Stage        string `json:"stage"`
		Error        string `json:"error"`
		Email        string `json:"email"`
		Password     string `json:"password"`
		Token        string `json:"token"`
		UserID       string `json:"user_id"`
		Name         string `json:"name"`
		MailProvider string `json:"mail_provider"`
		MailPassword string `json:"mail_password"`
	}
	if last == "" || json.Unmarshal([]byte(last), &res) != nil {
		if runErr != nil {
			return nil, "", "", "", fmt.Errorf("signup script: %v: %s", runErr, truncate(out.String(), 300))
		}
		return nil, "", "", "", fmt.Errorf("signup script gave no result: %s", truncate(out.String(), 300))
	}
	if !res.OK || res.Token == "" {
		return nil, "", "", "", fmt.Errorf("signup %s: %s", res.Stage, res.Error)
	}
	return &zaiAccount{Email: res.Email, Password: res.Password, Token: res.Token, UserID: res.UserID}, res.Name, res.MailProvider, res.MailPassword, nil
}
