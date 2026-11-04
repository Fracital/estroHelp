// Package billing holds Estro's plans, usage windows and Stripe integration.
// Plans are the one source of truth for prices and caps; the pricing page
// reads the same numbers through /api/billing/plans.
package billing

import (
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
)

type PlanID string

const (
	Starter PlanID = "starter"
	Plus    PlanID = "plus"
	Pro     PlanID = "pro"
	Max     PlanID = "max"
	Ultra   PlanID = "ultra"
)

var PlanIDs = []PlanID{Starter, Plus, Pro, Max, Ultra}

type Interval string

const (
	Monthly Interval = "monthly"
	Annual  Interval = "annual"
)

// AnnualMonthsCharged: annual billing is one charge a year at this many monthly prices.
const AnnualMonthsCharged = 10

// Cost weights per token: a cache hit is a tenth of fresh input, output is five
// times fresh input. One unit (shekel) is one fresh input token on a 1x model.
const (
	CachedInputWeight = 0.1
	OutputWeight      = 5
)

// FreeUnitsPerDay is the daily pool for accounts without a plan.
const FreeUnitsPerDay = 50_000

// FreeModels are the only models an account without a plan may use (their
// upstream costs nothing). Everything else needs an active plan.
var FreeModels = []string{"kali-epsilon"}

func IsFreeModel(id string) bool {
	for _, m := range FreeModels {
		if m == id {
			return true
		}
	}
	return false
}

type Plan struct {
	ID            PlanID  `json:"id"`
	Name          string  `json:"name"`
	Audience      string  `json:"audience"`
	MonthlyCents  int     `json:"monthlyCents"`
	FiveHourUnits int     `json:"fiveHourUnits"`
	WeeklyUnits   int     `json:"weeklyUnits"`
	FreeUnitsDay  int     `json:"freeModelTokensPerDay"`
	AccountShare  float64 `json:"accountShare"`
	Recommended   bool    `json:"recommended"`
}

var Plans = []Plan{
	{Starter, "Starter", "To try it properly", 800, 560_000, 920_000, 250_000, 1.0 / 20, false},
	{Plus, "Plus", "For regular use", 2400, 1_360_000, 2_200_000, 500_000, 1.0 / 8, true},
	{Pro, "Pro", "For daily work", 4400, 2_160_000, 3_680_000, 1_000_000, 1.0 / 5, false},
	{Max, "Max", "For heavy use", 10900, 4_320_000, 9_200_000, 2_000_000, 1.0 / 2, false},
	{Ultra, "Ultra", "For the heaviest use", 21900, 8_000_000, 18_400_000, 5_000_000, 1, false},
}

func PlanByID(id string) (Plan, bool) {
	for _, p := range Plans {
		if string(p.ID) == id {
			return p, true
		}
	}
	return Plan{}, false
}

func IsPlanID(id string) bool { _, ok := PlanByID(id); return ok }

func planIndex(id PlanID) int {
	for i, p := range Plans {
		if p.ID == id {
			return i
		}
	}
	return -1
}

// SalesOpen is the sales gate (SALES_OPEN, default true). Sandbox accounts may
// buy while it is closed for everyone else.
func SalesOpen() bool {
	v := strings.TrimSpace(os.Getenv("SALES_OPEN"))
	if v == "" {
		return true
	}
	b, err := strconv.ParseBool(v)
	return err != nil || b
}

func SalesOpenFor(sandbox bool) bool { return SalesOpen() || sandbox }

// PriceFor: chargeCents is what Stripe bills each time; perMonthCents is what the card shows.
func PriceFor(p Plan, interval Interval) (chargeCents, perMonthCents int) {
	if interval == Monthly {
		return p.MonthlyCents, p.MonthlyCents
	}
	charge := p.MonthlyCents * AnnualMonthsCharged
	return charge, int(math.Round(float64(charge) / 12))
}

// Tier is the string the billing routes take, e.g. estro_plus_year.
func Tier(plan PlanID, interval Interval) string {
	suffix := "month"
	if interval == Annual {
		suffix = "year"
	}
	return fmt.Sprintf("estro_%s_%s", plan, suffix)
}

var tierRe = regexp.MustCompile(`^estro_([a-z]+)_(month|year)$`)

// ParseTier reads estro_<plan>_<month|year>.
func ParseTier(tier string) (PlanID, Interval, bool) {
	m := tierRe.FindStringSubmatch(tier)
	if m == nil || !IsPlanID(m[1]) {
		return "", "", false
	}
	if m[2] == "year" {
		return PlanID(m[1]), Annual, true
	}
	return PlanID(m[1]), Monthly, true
}

type Change string

const (
	Same      Change = "same"
	Upgrade   Change = "upgrade"
	Downgrade Change = "downgrade"
)

// CompareChange: a higher plan is an upgrade and a lower plan a downgrade, whatever
// the period. On the same plan, monthly to annual is an upgrade and annual to monthly a downgrade.
func CompareChange(curPlan PlanID, curInterval Interval, toPlan PlanID, toInterval Interval) Change {
	from, to := planIndex(curPlan), planIndex(toPlan)
	if from != to {
		if to > from {
			return Upgrade
		}
		return Downgrade
	}
	if curInterval == toInterval {
		return Same
	}
	if toInterval == Annual {
		return Upgrade
	}
	return Downgrade
}

// UnitsFor: (fresh input + cached input x 0.1 + output x 5) x multiplier, rounded to 1e-6.
// inputTokens is the whole prompt, cache hits included; cachedTokens is the part read from the cache.
func UnitsFor(inputTokens, cachedTokens, outputTokens int, multiplier float64) float64 {
	in := math.Max(0, float64(inputTokens))
	cached := math.Min(math.Max(0, float64(cachedTokens)), in)
	fresh := in - cached
	weighted := fresh + cached*CachedInputWeight + math.Max(0, float64(outputTokens))*OutputWeight
	return math.Round(weighted*multiplier*1e6) / 1e6
}

// AccountsPaidFor sums each active plan's share of a pool account.
func AccountsPaidFor(active map[PlanID]int) float64 {
	total := 0.0
	for _, p := range Plans {
		total += float64(active[p.ID]) * p.AccountShare
	}
	return math.Round(total*1e6) / 1e6
}

// ModelMultiplier is the per-model billing weight. All Kali models are 1x for now.
func ModelMultiplier(model string) float64 { return 1 }

// FormatShekels renders whole units with thousands separators, prefixed ₪.
func FormatShekels(units float64) string {
	n := int64(math.Floor(units))
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return "₪" + b.String()
}
