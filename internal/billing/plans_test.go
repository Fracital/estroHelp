package billing

import (
	"math"
	"testing"
	"time"
)

func TestUnitsFor(t *testing.T) {
	cases := []struct {
		in, cached, out int
		mult, want      float64
	}{
		{1000, 0, 0, 1, 1000},
		{1000, 1000, 0, 1, 100},
		{1000, 2000, 0, 1, 100},   // cached clamped to input
		{0, 0, 100, 1, 500},       // output x5
		{1000, 200, 100, 2, 2640}, // (800 + 20 + 500) x 2
		{-5, -5, -5, 1, 0},
	}
	for _, c := range cases {
		if got := UnitsFor(c.in, c.cached, c.out, c.mult); math.Abs(got-c.want) > 1e-9 {
			t.Errorf("UnitsFor(%d,%d,%d,%v) = %v, want %v", c.in, c.cached, c.out, c.mult, got, c.want)
		}
	}
}

func TestCompareChange(t *testing.T) {
	if CompareChange(Starter, Monthly, Plus, Monthly) != Upgrade {
		t.Error("starter->plus should upgrade")
	}
	if CompareChange(Ultra, Annual, Max, Annual) != Downgrade {
		t.Error("ultra->max should downgrade")
	}
	if CompareChange(Plus, Monthly, Plus, Annual) != Upgrade {
		t.Error("monthly->annual same plan should upgrade")
	}
	if CompareChange(Plus, Annual, Plus, Monthly) != Downgrade {
		t.Error("annual->monthly same plan should downgrade")
	}
	if CompareChange(Pro, Monthly, Pro, Monthly) != Same {
		t.Error("same")
	}
}

func TestTier(t *testing.T) {
	for _, p := range Plans {
		for _, iv := range []Interval{Monthly, Annual} {
			plan, interval, ok := ParseTier(Tier(p.ID, iv))
			if !ok || plan != p.ID || interval != iv {
				t.Errorf("round trip %s %s failed", p.ID, iv)
			}
		}
	}
	if _, _, ok := ParseTier("estro_gold_month"); ok {
		t.Error("unknown plan accepted")
	}
	if _, _, ok := ParseTier("abstract_plus_month"); ok {
		t.Error("foreign tier accepted")
	}
}

func TestPriceFor(t *testing.T) {
	plus, _ := PlanByID("plus")
	charge, perMonth := PriceFor(plus, Annual)
	if charge != 24000 || perMonth != 2000 {
		t.Errorf("annual plus: charge %d perMonth %d", charge, perMonth)
	}
	if AccountsPaidFor(map[PlanID]int{Starter: 20}) != 1 {
		t.Error("20 starters should pay for exactly one account")
	}
}

func TestPreflightAndStatus(t *testing.T) {
	now := time.Now()
	snap := Snapshot{Now: now, Rows: map[WindowKind]*WindowRow{
		FiveHour: {Start: now.Add(-time.Hour), Used: 560_000},
		Weekly:   {Start: now.Add(-24 * time.Hour), Used: 10},
	}}
	limit, ok := Preflight(Starter, 1, snap)
	if ok || limit.Window != "5h" || limit.ResetsIn < 4*3600-1 || limit.ResetsIn > 4*3600+1 {
		t.Errorf("expected 5h block, got ok=%v %+v", ok, limit)
	}
	// Expired window counts as empty.
	snap.Rows[FiveHour].Start = now.Add(-6 * time.Hour)
	if _, ok := Preflight(Starter, 1, snap); !ok {
		t.Error("expired window should not block")
	}
	s := WindowStatus(Starter, FiveHour, snap.Rows[FiveHour], now)
	if s.UsedPercent != 0 || s.ResetAfter != 5*3600 {
		t.Errorf("expired status %+v", s)
	}
	s = WindowStatus(Starter, Weekly, &WindowRow{Start: now.Add(-time.Hour), Used: 460_000}, now)
	if s.UsedPercent != 50 || s.Full {
		t.Errorf("weekly status %+v", s)
	}
	if FormatResets(90*time.Minute) != "1h 30m" || FormatResets(49*time.Hour) != "2d 1h" || FormatResets(5*time.Minute) != "5m" {
		t.Error("FormatResets")
	}
	if FormatShekels(1234567) != "₪1,234,567" {
		t.Errorf("FormatShekels: %s", FormatShekels(1234567))
	}
}
