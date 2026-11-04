package zai

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newTestPool(t *testing.T, n int) (*zaiAccountPool, *zaiMemStore) {
	t.Helper()
	st := &zaiMemStore{}
	for i := 0; i < n; i++ {
		st.insert(context.Background(), &zaiAccount{Email: "a" + string(rune('0'+i)) + "@x", Token: "t" + string(rune('0'+i)), Status: "active"}, "", "", "")
	}
	return &zaiAccountPool{store: st, leased: map[int]int{}, target: 0}, st
}

func TestLeaseGivesEachWorkerADistinctAccount(t *testing.T) {
	p, _ := newTestPool(t, 2)
	a := p.lease(context.Background(), 0)
	b := p.lease(context.Background(), 1)
	c := p.lease(context.Background(), 2)
	if a == nil || b == nil || a.ID == b.ID {
		t.Fatalf("workers share or miss accounts: %+v %+v", a, b)
	}
	if c != nil {
		t.Fatalf("third worker got an account the first two hold: %+v", c)
	}
	p.release(a.ID)
	if d := p.lease(context.Background(), 2); d == nil || d.ID != a.ID {
		t.Fatalf("released account not reused: %+v", d)
	}
}

func TestBusyCoolsDownAndRotates(t *testing.T) {
	p, st := newTestPool(t, 2)
	a := p.lease(context.Background(), 0)
	p.busy(context.Background(), a)
	rows, _ := st.list(context.Background())
	for _, r := range rows {
		if r.ID == a.ID && (r.Strikes != 1 || time.Until(r.CooldownUntil) < time.Minute) {
			t.Fatalf("cooldown not persisted: %+v", r)
		}
	}
	if b := p.lease(context.Background(), 0); b == nil || b.ID == a.ID {
		t.Fatalf("worker did not move to the other account: %+v", b)
	}
	if c := p.lease(context.Background(), 1); c != nil {
		t.Fatalf("cooled-down account leased: %+v", c)
	}
	p.ok(context.Background(), p.leased0(t))
}

func (p *zaiAccountPool) leased0(t *testing.T) *zaiAccount {
	t.Helper()
	rows, _ := p.store.list(context.Background())
	for _, r := range rows {
		if _, ok := p.leased[r.ID]; ok {
			return r
		}
	}
	t.Fatal("nothing leased")
	return nil
}

func TestDeadAccountIsNeverLeased(t *testing.T) {
	p, _ := newTestPool(t, 1)
	a := p.lease(context.Background(), 0)
	p.dead(context.Background(), a, "token_rejected")
	if b := p.lease(context.Background(), 0); b != nil {
		t.Fatalf("dead account leased: %+v", b)
	}
}

func TestEnsureFillProvisionsUpToTargetWithBackoff(t *testing.T) {
	p, st := newTestPool(t, 0)
	p.target = 2
	calls := 0
	p.signup = func(context.Context) (*zaiAccount, string, string, string, error) {
		calls++
		if calls == 1 {
			return nil, "", "", "", errors.New("captcha")
		}
		return &zaiAccount{Email: "n@x", Token: "tok"}, "N", "mailtm", "mp", nil
	}
	p.ensureFill() // first attempt fails
	if calls != 1 || p.failures != 1 {
		t.Fatalf("failure not counted: calls=%d failures=%d", calls, p.failures)
	}
	p.ensureFill() // inside the backoff gap: no attempt
	if calls != 1 {
		t.Fatalf("backoff ignored: calls=%d", calls)
	}
	p.lastTry = time.Time{}
	p.ensureFill()
	rows, _ := st.list(context.Background())
	if calls != 2 || len(rows) != 1 || rows[0].Email != "n@x" || rows[0].Status != "active" {
		t.Fatalf("account not stored: calls=%d rows=%+v", calls, rows)
	}
	if p.failures != 0 {
		t.Fatalf("failures not reset: %d", p.failures)
	}
}
