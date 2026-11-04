package billing

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"elichat/internal/db"
)

type WindowKind string

const (
	FiveHour WindowKind = "five_hour"
	Weekly   WindowKind = "weekly"
)

var WindowKinds = []WindowKind{FiveHour, Weekly}

var WindowLength = map[WindowKind]time.Duration{
	FiveHour: 5 * time.Hour,
	Weekly:   7 * 24 * time.Hour,
}

// WindowName is the wire name in 429 bodies and messages.
var WindowName = map[WindowKind]string{FiveHour: "5h", Weekly: "weekly"}

func PlanCap(plan PlanID, kind WindowKind) int {
	p, _ := PlanByID(string(plan))
	if kind == FiveHour {
		return p.FiveHourUnits
	}
	return p.WeeklyUnits
}

// WindowRow is a stored window: when it started and the units used so far.
type WindowRow struct {
	Start time.Time
	Used  float64
}

// Snapshot is both windows for one user plus the database clock.
type Snapshot struct {
	Now  time.Time
	Rows map[WindowKind]*WindowRow
}

// IsExpired: no row, or the full length has passed (strict).
func IsExpired(kind WindowKind, row *WindowRow, now time.Time) bool {
	return row == nil || now.Sub(row.Start) > WindowLength[kind]
}

// Read gives effective usage now; an expired window counts as empty and resets in full length.
func Read(kind WindowKind, row *WindowRow, now time.Time) (used float64, resetsIn time.Duration) {
	if IsExpired(kind, row, now) {
		return 0, WindowLength[kind]
	}
	return row.Used, row.Start.Add(WindowLength[kind]).Sub(now)
}

func FormatResets(d time.Duration) string {
	s := int(math.Max(0, math.Floor(d.Seconds())))
	days, hours, minutes := s/86400, (s%86400)/3600, (s%3600)/60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh", days, hours)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}

type UsageLimit struct {
	Window     string
	Used       float64
	Limit      int
	ResetsIn   int // seconds, rounded up
	Multiplier float64
}

func (l UsageLimit) Message() string {
	label := "5h window"
	switch l.Window {
	case "weekly":
		label = "weekly window"
	case "daily":
		label = "daily pool"
	}
	return fmt.Sprintf("Estro usage limit reached. %s: %d/%d. Resets in %s.", label, int(math.Floor(l.Used)), l.Limit, FormatResets(time.Duration(l.ResetsIn)*time.Second))
}

// Preflight blocks when a window is full (used >= cap). The request that fills a
// window is allowed and metered. With both full, the one that resets later is reported.
func Preflight(plan PlanID, multiplier float64, snap Snapshot) (UsageLimit, bool) {
	var blocked *UsageLimit
	var blockedReset time.Duration
	for _, kind := range WindowKinds {
		limit := PlanCap(plan, kind)
		used, resetsIn := Read(kind, snap.Rows[kind], snap.Now)
		if used < float64(limit) {
			continue
		}
		if blocked == nil || resetsIn > blockedReset {
			blocked = &UsageLimit{Window: WindowName[kind], Used: used, Limit: limit, ResetsIn: int(math.Ceil(math.Max(0, resetsIn.Seconds()))), Multiplier: multiplier}
			blockedReset = resetsIn
		}
	}
	if blocked == nil {
		return UsageLimit{}, true
	}
	return *blocked, false
}

// Status is one window the way Codex reports plan usage.
type Status struct {
	UsedPercent   float64 // rounded down to 0.1
	Full          bool
	WindowSeconds int
	ResetAfter    int
	ResetAt       int64
}

func WindowStatus(plan PlanID, kind WindowKind, row *WindowRow, now time.Time) Status {
	used, resetsIn := Read(kind, row, now)
	cap := float64(PlanCap(plan, kind))
	percent := math.Min(100, math.Max(0, used/cap*100))
	resetAfter := int(math.Ceil(math.Max(0, resetsIn.Seconds())))
	return Status{
		UsedPercent:   math.Floor(percent*10) / 10,
		Full:          used >= cap,
		WindowSeconds: int(WindowLength[kind].Seconds()),
		ResetAfter:    resetAfter,
		ResetAt:       now.Unix() + int64(resetAfter),
	}
}

// CodexUsageWindow is the /wham/usage window shape (integers only).
func CodexUsageWindow(s Status) map[string]any {
	return map[string]any{
		"used_percent":         int(math.Floor(s.UsedPercent)),
		"limit_window_seconds": s.WindowSeconds,
		"reset_after_seconds":  s.ResetAfter,
		"reset_at":             s.ResetAt,
	}
}

var codexSlot = map[WindowKind]string{FiveHour: "primary", Weekly: "secondary"}

// SetCodexHeaders adds x-codex-{primary,secondary}-{used-percent,window-minutes,reset-at}.
func SetCodexHeaders(h http.Header, plan PlanID, snap Snapshot) {
	for _, kind := range WindowKinds {
		s := WindowStatus(plan, kind, snap.Rows[kind], snap.Now)
		slot := codexSlot[kind]
		h.Set("x-codex-"+slot+"-used-percent", strconv.FormatFloat(s.UsedPercent, 'f', -1, 64))
		h.Set("x-codex-"+slot+"-window-minutes", strconv.Itoa(s.WindowSeconds/60))
		h.Set("x-codex-"+slot+"-reset-at", strconv.FormatInt(s.ResetAt, 10))
	}
}

// SetCodexLimitHeaders adds the extra headers on a usage-limit 429.
func SetCodexLimitHeaders(h http.Header, plan PlanID, pricingURL string) {
	h.Set("x-codex-rate-limit-reached-type", "rate_limit_reached")
	if plan != Ultra {
		h.Set("x-codex-promo-message", "Upgrade your Estro plan at "+pricingURL)
	}
}

// ---- store ----

// ReadSnapshot reads both windows and the database clock in one statement.
func ReadSnapshot(ctx context.Context, d *db.DB, userID int) (Snapshot, error) {
	rows, err := d.Query(ctx, `SELECT w.kind, w.used_units::float8, w.window_start, n.now
		FROM (SELECT NOW() AS now) n LEFT JOIN usage_window w ON w.user_id = $1`, userID)
	if err != nil {
		return Snapshot{}, err
	}
	defer rows.Close()
	snap := Snapshot{Now: time.Now(), Rows: map[WindowKind]*WindowRow{}}
	for rows.Next() {
		var kind *string
		var used *float64
		var start *time.Time
		var now time.Time
		if err := rows.Scan(&kind, &used, &start, &now); err != nil {
			return Snapshot{}, err
		}
		snap.Now = now
		if kind != nil && used != nil && start != nil {
			snap.Rows[WindowKind(*kind)] = &WindowRow{Start: *start, Used: *used}
		}
	}
	return snap, rows.Err()
}

type UsageRecord struct {
	UserID       int
	Model        string
	Multiplier   float64
	Units        float64
	InputTokens  int
	CachedTokens int
	OutputTokens int
}

func clampInt(v int) int {
	if v < 0 {
		return 0
	}
	if v > 2_147_483_647 {
		return 2_147_483_647
	}
	return v
}

// RecordUsage adds one request to both windows and writes the event row in one
// transaction. A window that has expired restarts at this request.
func RecordUsage(ctx context.Context, d *db.DB, rec UsageRecord) error {
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO usage_event (user_id, model, multiplier, units, input_tokens, cached_tokens, output_tokens) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		rec.UserID, rec.Model, rec.Multiplier, rec.Units, clampInt(rec.InputTokens), clampInt(rec.CachedTokens), clampInt(rec.OutputTokens)); err != nil {
		return err
	}
	for _, kind := range WindowKinds {
		secs := WindowLength[kind].Seconds()
		if _, err := tx.Exec(ctx, `INSERT INTO usage_window (user_id, kind, window_start, used_units) VALUES ($1, $2, NOW(), $3)
			ON CONFLICT (user_id, kind) DO UPDATE SET
			  window_start = CASE WHEN usage_window.window_start < NOW() - make_interval(secs => $4::float8) THEN NOW() ELSE usage_window.window_start END,
			  used_units = CASE WHEN usage_window.window_start < NOW() - make_interval(secs => $4::float8) THEN EXCLUDED.used_units ELSE usage_window.used_units + EXCLUDED.used_units END`,
			rec.UserID, string(kind), rec.Units, secs); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// FreeUsedToday is the units a no-plan account has used since 00:00 UTC.
func FreeUsedToday(ctx context.Context, d *db.DB, userID int) (float64, error) {
	var used float64
	err := d.QueryRow(ctx, `SELECT coalesce(sum(units),0)::float8 FROM usage_event WHERE user_id=$1 AND created >= date_trunc('day', NOW() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`, userID).Scan(&used)
	return used, err
}

// NextUTCMidnight is when the free pool resets.
func NextUTCMidnight(now time.Time) time.Time {
	u := now.UTC()
	return time.Date(u.Year(), u.Month(), u.Day()+1, 0, 0, 0, 0, time.UTC)
}
