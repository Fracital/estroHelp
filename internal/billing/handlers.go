package billing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"

	"elichat/internal/auth"
	"elichat/internal/db"
	"elichat/internal/ratelimit"
)

func jsonUnmarshal(raw json.RawMessage, v any) error { return json.Unmarshal(raw, v) }

type Handlers struct {
	DB    *db.DB
	Store *auth.Store
}

var changeLimit = ratelimit.New(6, 6)

func (h *Handlers) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/billing/plans", h.plans)
	mux.HandleFunc("POST /api/stripe/checkout", h.checkout)
	mux.HandleFunc("POST /api/stripe/portal", auth.RequireUser(h.portal))
	mux.HandleFunc("GET /api/stripe/change/preview", auth.RequireUser(h.changePreview))
	mux.HandleFunc("POST /api/stripe/change", auth.RequireUser(h.change))
	mux.HandleFunc("POST /api/stripe/change/undo", auth.RequireUser(h.changeUndo))
	mux.HandleFunc("POST /api/stripe/webhook", h.webhook)
	mux.HandleFunc("GET /api/account/plan/me", auth.RequireUser(h.me))
	mux.HandleFunc("GET /api/admin/billing/overview", auth.RequireAdmin(h.adminOverview))
	mux.HandleFunc("POST /api/admin/users/{id}/plan", auth.RequireAdmin(h.adminPlan))
	mux.HandleFunc("GET /api/admin/revenue", auth.RequireAdmin(h.adminRevenue))
	mux.HandleFunc("GET /api/admin/transactions", auth.RequireAdmin(h.adminTransactions))
	mux.HandleFunc("GET /api/admin/series", auth.RequireAdmin(h.adminSeries))
}

// adminSeries feeds the admin overview graphs: one value per UTC day for the
// last N days (oldest first), zero-filled so every series has the same length.
func (h *Handlers) adminSeries(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days < 1 || days > 365 {
		days = 30
	}
	start := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -(days - 1))
	labels := make([]string, days)
	index := map[string]int{}
	for i := range labels {
		labels[i] = start.AddDate(0, 0, i).Format("2006-01-02")
		index[labels[i]] = i
	}
	series := func(query string) ([]float64, error) {
		out := make([]float64, days)
		rows, err := h.DB.Query(ctx, query, start)
		if err != nil {
			return out, err
		}
		defer rows.Close()
		for rows.Next() {
			var day time.Time
			var v float64
			if err := rows.Scan(&day, &v); err != nil {
				return out, err
			}
			if i, ok := index[day.UTC().Format("2006-01-02")]; ok {
				out[i] = v
			}
		}
		return out, rows.Err()
	}
	requests, err := series(`SELECT day, sum(requests)::float8 FROM ai_usage WHERE day >= $1::date GROUP BY day`)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	units, _ := series(`SELECT (created AT TIME ZONE 'UTC')::date, sum(units)::float8 FROM usage_event WHERE created >= $1 GROUP BY 1`)
	active, _ := series(`SELECT day, count(DISTINCT user_id)::float8 FROM ai_usage WHERE day >= $1::date GROUP BY day`)
	signups, _ := series(`SELECT (created AT TIME ZONE 'UTC')::date, count(*)::float8 FROM users WHERE created >= $1 GROUP BY 1`)
	revenue, _ := series(`SELECT (created AT TIME ZONE 'UTC')::date, sum(amount_cents)::float8 FROM payment WHERE source='stripe' AND amount_cents > 0 AND created >= $1 GROUP BY 1`)
	auth.WriteJSON(w, 200, map[string]any{"days": labels, "requests": requests, "units": units, "activeUsers": active, "signups": signups, "revenueCents": revenue})
}

func (h *Handlers) plans(w http.ResponseWriter, r *http.Request) {
	type card struct {
		Plan
		Monthly struct{ ChargeCents, PerMonthCents int } `json:"monthly"`
		Annual  struct{ ChargeCents, PerMonthCents int } `json:"annual"`
	}
	out := []card{}
	for _, p := range Plans {
		var c card
		c.Plan = p
		c.Monthly.ChargeCents, c.Monthly.PerMonthCents = PriceFor(p, Monthly)
		c.Annual.ChargeCents, c.Annual.PerMonthCents = PriceFor(p, Annual)
		out = append(out, c)
	}
	sandbox := false
	if u := auth.UserFrom(r.Context()); u != nil {
		sandbox = u.Sandbox
	}
	auth.WriteJSON(w, 200, map[string]any{"plans": out, "annualMonthsCharged": AnnualMonthsCharged, "freeUnitsPerDay": FreeUnitsPerDay, "freeModels": FreeModels, "salesOpen": SalesOpenFor(sandbox)})
}

func originOf(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		return o
	}
	scheme := "http"
	if r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func (h *Handlers) checkout(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	if !SalesOpenFor(u != nil && u.Sandbox) {
		auth.WriteError(w, 410, "Plans open soon.")
		return
	}
	if u == nil {
		auth.WriteError(w, 401, "Sign in to choose a plan.")
		return
	}
	if u.Email == nil || !u.EmailVerified {
		auth.WriteJSON(w, 403, map[string]any{"error": "Verify your email before choosing a plan.", "requiresEmail": true})
		return
	}
	plan, interval, ok := ParseTier(r.URL.Query().Get("tier"))
	if !ok {
		auth.WriteError(w, 400, "Unknown plan or billing period.")
		return
	}
	mode := ModeFor(u.Sandbox)
	k := KeysFor(mode)
	if k == nil || k.PriceID(plan, interval) == "" {
		auth.WriteError(w, 503, "Billing is not configured for this plan yet.")
		return
	}
	row, err := ReadBillingRow(r.Context(), h.DB, u.ID)
	if err != nil {
		auth.WriteError(w, 500, "Billing request failed.")
		return
	}
	if row.ActivePlan(time.Now()) != "" {
		auth.WriteError(w, 409, "You already have an Estro plan. Change it on the pricing page.")
		return
	}
	c := Client(k.SecretKey)
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	customerID, err := customerFor(r.Context(), h.DB, c, userRef{u.ID, u.Username, email}, mode, row.Customer(mode))
	if err != nil {
		slog.Error("stripe customer", "err", err)
		auth.WriteError(w, 500, "Could not start checkout.")
		return
	}
	tier := Tier(plan, interval)
	meta := map[string]string{"product": "estro", "tier": tier, "estro_plan": string(plan), "userId": strconv.Itoa(u.ID)}
	base := PublicURL(originOf(r))
	session, err := c.V1CheckoutSessions.Create(r.Context(), &stripe.CheckoutSessionCreateParams{
		Mode:                stripe.String(string(stripe.CheckoutSessionModeSubscription)),
		Customer:            stripe.String(customerID),
		LineItems:           []*stripe.CheckoutSessionCreateLineItemParams{{Price: stripe.String(k.PriceID(plan, interval)), Quantity: stripe.Int64(1)}},
		ClientReferenceID:   stripe.String(strconv.Itoa(u.ID)),
		Metadata:            meta,
		SubscriptionData:    &stripe.CheckoutSessionCreateSubscriptionDataParams{Metadata: meta},
		SuccessURL:          stripe.String(base + "/pricing?checkout=complete"),
		CancelURL:           stripe.String(base + "/pricing"),
		AllowPromotionCodes: stripe.Bool(false),
	})
	if err != nil || session.URL == "" {
		slog.Error("stripe checkout", "err", err)
		auth.WriteError(w, 500, "Could not start checkout.")
		return
	}
	auth.WriteJSON(w, 200, map[string]any{"url": session.URL})
}

func (h *Handlers) portal(w http.ResponseWriter, r *http.Request) {
	u := auth.UserFrom(r.Context())
	mode := ModeFor(u.Sandbox)
	k := KeysFor(mode)
	if k == nil {
		auth.WriteError(w, 503, "Billing is not configured.")
		return
	}
	if k.PortalConfiguration == "" {
		auth.WriteError(w, 503, "Billing portal is not configured.")
		return
	}
	row, err := ReadBillingRow(r.Context(), h.DB, u.ID)
	if err != nil {
		auth.WriteError(w, 500, "Billing request failed.")
		return
	}
	existing := row.Customer(mode)
	if existing == "" && mode == Live {
		auth.WriteError(w, 400, "No billing account yet.")
		return
	}
	c := Client(k.SecretKey)
	email := ""
	if u.Email != nil {
		email = *u.Email
	}
	customerID, err := customerFor(r.Context(), h.DB, c, userRef{u.ID, u.Username, email}, mode, existing)
	if err != nil {
		auth.WriteError(w, 500, "Could not open billing.")
		return
	}
	portal, err := c.V1BillingPortalSessions.Create(r.Context(), &stripe.BillingPortalSessionCreateParams{
		Customer:      stripe.String(customerID),
		Configuration: stripe.String(k.PortalConfiguration),
		ReturnURL:     stripe.String(PublicURL(originOf(r)) + "/pricing"),
	})
	if err != nil {
		slog.Error("stripe portal", "err", err)
		auth.WriteError(w, 500, "Could not open billing.")
		return
	}
	auth.WriteJSON(w, 200, map[string]any{"url": portal.URL})
}

// ---- plan changes ----

type liveSub struct {
	client     *stripe.Client
	customerID string
	sub        *stripe.Subscription
	plan       PlanID
	interval   Interval
}

func (h *Handlers) liveSubscription(ctx context.Context, k *Keys, u *auth.User, mode Mode) (*liveSub, error) {
	row, err := ReadBillingRow(ctx, h.DB, u.ID)
	if err != nil {
		return nil, err
	}
	customerID := row.Customer(mode)
	if customerID == "" {
		return nil, nil
	}
	c := Client(k.SecretKey)
	var subID string
	err = h.DB.QueryRow(ctx, `SELECT subscription_id FROM estro_subscription WHERE user_id=$1`, u.ID).Scan(&subID)
	var sub *stripe.Subscription
	if err == nil {
		sub, _ = c.V1Subscriptions.Retrieve(ctx, subID, nil)
	} else if errors.Is(err, pgx.ErrNoRows) {
		for cand, lerr := range c.V1Subscriptions.List(ctx, &stripe.SubscriptionListParams{Customer: stripe.String(customerID), ListParams: stripe.ListParams{Limit: stripe.Int64(20)}}) {
			if lerr != nil {
				return nil, lerr
			}
			if _, _, ok := k.PlanForPrice(priceIDOf(cand)); ok && grantingStatuses[cand.Status] {
				sub = cand
				break
			}
		}
	} else {
		return nil, err
	}
	if sub == nil || !grantingStatuses[sub.Status] {
		return nil, nil
	}
	plan, interval, ok := k.PlanForPrice(priceIDOf(sub))
	if !ok {
		return nil, nil
	}
	return &liveSub{client: c, customerID: customerID, sub: sub, plan: plan, interval: interval}, nil
}

// changeContext gates the change routes and loads the live subscription and target.
func (h *Handlers) changeContext(w http.ResponseWriter, r *http.Request, needTarget bool) (*auth.User, Mode, *Keys, *liveSub, PlanID, Interval, bool) {
	u := auth.UserFrom(r.Context())
	if u.Email == nil || !u.EmailVerified {
		auth.WriteJSON(w, 403, map[string]any{"error": "Verify your email before changing your plan.", "requiresEmail": true})
		return nil, "", nil, nil, "", "", false
	}
	mode := ModeFor(u.Sandbox)
	k := KeysFor(mode)
	if k == nil {
		auth.WriteError(w, 503, "Billing is not configured.")
		return nil, "", nil, nil, "", "", false
	}
	if ok, wait := changeLimit.Allow("change:" + strconv.Itoa(u.ID)); !ok {
		ratelimit.Reject(w, wait, `{"error":"Too many plan changes. Wait a minute and try again."}`)
		return nil, "", nil, nil, "", "", false
	}
	live, err := h.liveSubscription(r.Context(), k, u, mode)
	if err != nil {
		slog.Error("stripe live subscription", "err", err)
		auth.WriteError(w, 500, "Billing request failed.")
		return nil, "", nil, nil, "", "", false
	}
	if live == nil {
		auth.WriteError(w, 409, "You do not have a plan to change. Choose one first.")
		return nil, "", nil, nil, "", "", false
	}
	var plan PlanID
	var interval Interval
	if needTarget {
		var ok bool
		plan, interval, ok = ParseTier(r.URL.Query().Get("tier"))
		if !ok {
			auth.WriteError(w, 400, "Unknown plan or billing period.")
			return nil, "", nil, nil, "", "", false
		}
	}
	return u, mode, k, live, plan, interval, true
}

func (h *Handlers) changePreview(w http.ResponseWriter, r *http.Request) {
	u, _, k, live, plan, interval, ok := h.changeContext(w, r, true)
	if !ok {
		return
	}
	kind := CompareChange(live.plan, live.interval, plan, interval)
	if kind == Same {
		auth.WriteError(w, 400, "That is your current plan.")
		return
	}
	priceID := k.PriceID(plan, interval)
	if priceID == "" {
		auth.WriteError(w, 503, "Billing is not configured for this plan yet.")
		return
	}
	p, _ := PlanByID(string(plan))
	charge, _ := PriceFor(p, interval)
	base := map[string]any{"kind": kind, "plan": plan, "name": p.Name, "interval": interval, "nextChargeCents": charge}
	if kind == Downgrade {
		base["effectiveAt"] = periodEndOf(live.sub).UTC().Format(time.RFC3339)
		auth.WriteJSON(w, 200, base)
		return
	}
	if !SalesOpenFor(u.Sandbox) {
		auth.WriteError(w, 410, "Plans open soon.")
		return
	}
	item := firstItem(live.sub)
	details := &stripe.InvoiceCreatePreviewSubscriptionDetailsParams{
		Items:             []*stripe.InvoiceCreatePreviewSubscriptionDetailsItemParams{{ID: stripe.String(item.ID), Price: stripe.String(priceID)}},
		ProrationBehavior: stripe.String("always_invoice"),
	}
	if live.interval != interval {
		details.BillingCycleAnchorNow = stripe.Bool(true)
	} else {
		details.BillingCycleAnchorUnchanged = stripe.Bool(true)
	}
	inv, err := live.client.V1Invoices.CreatePreview(r.Context(), &stripe.InvoiceCreatePreviewParams{
		Customer:            stripe.String(live.customerID),
		Subscription:        stripe.String(live.sub.ID),
		SubscriptionDetails: details,
	})
	if err != nil {
		slog.Error("stripe preview", "err", err)
		auth.WriteError(w, 500, "Could not preview the change.")
		return
	}
	base["dueTodayCents"] = inv.AmountDue
	auth.WriteJSON(w, 200, base)
}

func (h *Handlers) change(w http.ResponseWriter, r *http.Request) {
	u, mode, k, live, plan, interval, ok := h.changeContext(w, r, true)
	if !ok {
		return
	}
	ctx := r.Context()
	kind := CompareChange(live.plan, live.interval, plan, interval)
	if kind == Same {
		auth.WriteError(w, 400, "That is your current plan.")
		return
	}
	if live.sub.CancelAtPeriodEnd {
		auth.WriteError(w, 409, "Your plan is set to cancel. Resume it in billing first.")
		return
	}
	if kind == Upgrade && !SalesOpenFor(u.Sandbox) {
		auth.WriteError(w, 410, "Plans open soon.")
		return
	}
	priceID := k.PriceID(plan, interval)
	if priceID == "" {
		auth.WriteError(w, 503, "Billing is not configured for this plan yet.")
		return
	}
	tier := Tier(plan, interval)
	meta := map[string]string{"product": "estro", "tier": tier, "estro_plan": string(plan), "userId": strconv.Itoa(u.ID)}
	item := firstItem(live.sub)
	if live.sub.Schedule != nil && live.sub.Schedule.ID != "" {
		if _, err := live.client.V1SubscriptionSchedules.Release(ctx, live.sub.Schedule.ID, nil); err != nil {
			slog.Error("stripe schedule release", "err", err)
			auth.WriteError(w, 500, "Could not change your plan.")
			return
		}
	}
	if kind == Upgrade {
		params := &stripe.SubscriptionUpdateParams{
			Items:             []*stripe.SubscriptionUpdateItemParams{{ID: stripe.String(item.ID), Price: stripe.String(priceID)}},
			ProrationBehavior: stripe.String("always_invoice"),
			PaymentBehavior:   stripe.String("error_if_incomplete"),
			Metadata:          meta,
		}
		if live.interval != interval {
			params.BillingCycleAnchorNow = stripe.Bool(true)
		} else {
			params.BillingCycleAnchorUnchanged = stripe.Bool(true)
		}
		updated, err := live.client.V1Subscriptions.Update(ctx, live.sub.ID, params)
		if err != nil {
			var se *stripe.Error
			if errors.As(err, &se) && se.Type == stripe.ErrorTypeCard {
				auth.WriteJSON(w, 402, map[string]any{"error": se.Msg, "paymentFailed": true})
				return
			}
			slog.Error("stripe upgrade", "err", err)
			auth.WriteError(w, 500, "Could not change your plan.")
			return
		}
		ch, err := subscriptionChange(ctx, h.DB, k, live.client, updated, strconv.Itoa(u.ID), mode)
		if err != nil || ch == nil {
			auth.WriteError(w, 500, "Could not change your plan.")
			return
		}
		if err := h.inTx(ctx, func(tx pgx.Tx) error { return applyChange(ctx, tx, mode, ch) }); err != nil {
			auth.WriteError(w, 500, "Could not change your plan.")
			return
		}
		auth.WriteJSON(w, 200, map[string]any{"kind": kind, "plan": plan, "interval": interval})
		return
	}
	// Downgrade: keep the current price until the period ends, then one period of the target, then release.
	periodEnd := periodEndOf(live.sub).Unix()
	sched, err := live.client.V1SubscriptionSchedules.Create(ctx, &stripe.SubscriptionScheduleCreateParams{FromSubscription: stripe.String(live.sub.ID)})
	if err != nil {
		slog.Error("stripe schedule create", "err", err)
		auth.WriteError(w, 500, "Could not schedule your plan change.")
		return
	}
	iv := "month"
	if interval == Annual {
		iv = "year"
	}
	_, err = live.client.V1SubscriptionSchedules.Update(ctx, sched.ID, &stripe.SubscriptionScheduleUpdateParams{
		EndBehavior:       stripe.String("release"),
		ProrationBehavior: stripe.String("none"),
		Phases: []*stripe.SubscriptionScheduleUpdatePhaseParams{
			{Items: []*stripe.SubscriptionScheduleUpdatePhaseItemParams{{Price: stripe.String(item.Price.ID), Quantity: stripe.Int64(1)}}, StartDate: stripe.Int64(sched.Phases[0].StartDate), EndDate: stripe.Int64(periodEnd)},
			{Items: []*stripe.SubscriptionScheduleUpdatePhaseItemParams{{Price: stripe.String(priceID), Quantity: stripe.Int64(1)}}, Duration: &stripe.SubscriptionScheduleUpdatePhaseDurationParams{Interval: stripe.String(iv), IntervalCount: stripe.Int64(1)}},
		},
		Metadata: meta,
	})
	if err != nil {
		live.client.V1SubscriptionSchedules.Release(ctx, sched.ID, nil)
		slog.Error("stripe schedule update", "err", err)
		auth.WriteError(w, 500, "Could not schedule your plan change.")
		return
	}
	refreshed, err := live.client.V1Subscriptions.Retrieve(ctx, live.sub.ID, nil)
	if err != nil {
		auth.WriteError(w, 500, "Could not schedule your plan change.")
		return
	}
	st, err := stateFor(ctx, k, live.client, refreshed)
	if err != nil || st == nil {
		auth.WriteError(w, 500, "Could not schedule your plan change.")
		return
	}
	if err := h.inTx(ctx, func(tx pgx.Tx) error { return writeState(ctx, tx, u.ID, mode, st) }); err != nil {
		auth.WriteError(w, 500, "Could not schedule your plan change.")
		return
	}
	auth.WriteJSON(w, 200, map[string]any{"kind": kind, "plan": plan, "interval": interval, "effectiveAt": st.PeriodEnd.UTC().Format(time.RFC3339)})
}

func (h *Handlers) changeUndo(w http.ResponseWriter, r *http.Request) {
	u, mode, k, live, _, _, ok := h.changeContext(w, r, false)
	if !ok {
		return
	}
	ctx := r.Context()
	if live.sub.Schedule == nil || live.sub.Schedule.ID == "" {
		auth.WriteError(w, 409, "Nothing is scheduled to change.")
		return
	}
	if _, err := live.client.V1SubscriptionSchedules.Release(ctx, live.sub.Schedule.ID, nil); err != nil {
		auth.WriteError(w, 500, "Could not undo the change.")
		return
	}
	refreshed, err := live.client.V1Subscriptions.Retrieve(ctx, live.sub.ID, nil)
	if err == nil {
		if st, err := stateFor(ctx, k, live.client, refreshed); err == nil && st != nil {
			h.inTx(ctx, func(tx pgx.Tx) error { return writeState(ctx, tx, u.ID, mode, st) })
		}
	}
	auth.WriteJSON(w, 200, map[string]any{"ok": true})
}

func (h *Handlers) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := h.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ---- webhook ----

func (h *Handlers) webhook(w http.ResponseWriter, r *http.Request) {
	payload, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		auth.WriteError(w, 400, "could not read body")
		return
	}
	sig := r.Header.Get("Stripe-Signature")
	var ev stripe.Event
	verified := false
	for _, mode := range []Mode{Live, Sandbox} {
		k := KeysFor(mode)
		if k == nil || !isSet(k.WebhookSecret) {
			continue
		}
		if e, err := webhook.ConstructEventWithOptions(payload, sig, k.WebhookSecret, webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true}); err == nil {
			ev, verified = e, true
			break
		}
	}
	if !verified {
		auth.WriteError(w, 400, "bad signature")
		return
	}
	if err := HandleWebhookEvent(r.Context(), h.DB, &ev); err != nil {
		slog.Error("stripe webhook", "event", ev.ID, "type", ev.Type, "err", err)
		auth.WriteError(w, 500, "webhook failed")
		return
	}
	auth.WriteJSON(w, 200, map[string]any{"received": true})
}

// ---- GET /api/account/plan/me ----

type meter struct {
	Used     float64 `json:"used"`
	Cap      int     `json:"cap"`
	ResetsAt *string `json:"resetsAt"`
}

func meterFor(kind WindowKind, cap int, snap Snapshot) meter {
	row := snap.Rows[kind]
	used, _ := Read(kind, row, snap.Now)
	m := meter{Used: used, Cap: cap}
	if row != nil && !IsExpired(kind, row, snap.Now) {
		s := row.Start.Add(WindowLength[kind]).UTC().Format(time.RFC3339)
		m.ResetsAt = &s
	}
	return m
}

// MePayload is what the account page, pricing page and chat read.
func (h *Handlers) MePayload(ctx context.Context, u *auth.User) (map[string]any, error) {
	row, err := ReadBillingRow(ctx, h.DB, u.ID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"plan": nil, "usage": nil, "salesOpen": SalesOpenFor(u.Sandbox), "admin": u.Admin}
	now := time.Now()
	planID := row.ActivePlan(now)
	if planID == "" {
		used, err := FreeUsedToday(ctx, h.DB, u.ID)
		if err != nil {
			return nil, err
		}
		out["free"] = map[string]any{"used": used, "cap": FreeUnitsPerDay, "resetsAt": NextUTCMidnight(now).Format(time.RFC3339)}
		return out, nil
	}
	p, _ := PlanByID(string(planID))
	snap, err := ReadSnapshot(ctx, h.DB, u.ID)
	if err != nil {
		return nil, err
	}
	// managed: a Stripe subscription stands behind the plan. Without one (an admin
	// grant, or a plan set by hand) there is nothing to renew or change through Stripe.
	plan := map[string]any{"id": p.ID, "name": p.Name, "until": row.PlanUntil.UTC().Format(time.RFC3339), "active": true, "managed": false, "interval": nil, "cancelAt": nil, "pending": nil}
	var interval, nextPlan, nextInterval *string
	var cancelAt, nextAt *time.Time
	err = h.DB.QueryRow(ctx, `SELECT billing_interval, cancel_at, next_plan, next_billing_interval, next_at FROM estro_subscription WHERE user_id=$1`, u.ID).Scan(&interval, &cancelAt, &nextPlan, &nextInterval, &nextAt)
	if err == nil {
		plan["managed"] = true
		if interval != nil {
			plan["interval"] = *interval
		}
		if cancelAt != nil {
			plan["cancelAt"] = cancelAt.UTC().Format(time.RFC3339)
		}
		if nextPlan != nil && nextInterval != nil && nextAt != nil && nextAt.After(now) {
			if np, ok := PlanByID(*nextPlan); ok {
				plan["pending"] = map[string]any{"plan": np.ID, "name": np.Name, "interval": *nextInterval, "at": nextAt.UTC().Format(time.RFC3339)}
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	out["plan"] = plan
	out["usage"] = map[string]any{"fiveHour": meterFor(FiveHour, p.FiveHourUnits, snap), "weekly": meterFor(Weekly, p.WeeklyUnits, snap)}
	return out, nil
}

func (h *Handlers) me(w http.ResponseWriter, r *http.Request) {
	out, err := h.MePayload(r.Context(), auth.UserFrom(r.Context()))
	if err != nil {
		slog.Error("plan/me", "err", err)
		auth.WriteError(w, 500, "Request failed.")
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	auth.WriteJSON(w, 200, out)
}

// ---- admin ----

func (h *Handlers) adminOverview(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	active := map[PlanID]int{}
	rows, err := h.DB.Query(ctx, `SELECT plan, count(*) FROM users WHERE plan IS NOT NULL AND plan_until > NOW() GROUP BY plan`)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	for rows.Next() {
		var plan string
		var n int
		rows.Scan(&plan, &n)
		active[PlanID(plan)] = n
	}
	rows.Close()
	var requests int
	var units float64
	h.DB.QueryRow(ctx, `SELECT count(*), coalesce(sum(units),0)::float8 FROM usage_event WHERE created >= date_trunc('day', NOW() AT TIME ZONE 'UTC') AT TIME ZONE 'UTC'`).Scan(&requests, &units)
	var mrr int64
	h.DB.QueryRow(ctx, `SELECT coalesce(sum(amount_cents),0) FROM payment WHERE source='stripe' AND created > NOW() - interval '30 days'`).Scan(&mrr)
	perPlan := []map[string]any{}
	for _, p := range Plans {
		perPlan = append(perPlan, map[string]any{"id": p.ID, "name": p.Name, "active": active[p.ID]})
	}
	auth.WriteJSON(w, 200, map[string]any{"plans": perPlan, "requestsToday": requests, "unitsToday": units, "accountsPaidFor": AccountsPaidFor(active), "revenue30dCents": mrr, "salesOpen": SalesOpen()})
}

func (h *Handlers) adminPlan(w http.ResponseWriter, r *http.Request) {
	actor := auth.UserFrom(r.Context())
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		auth.WriteError(w, 400, "bad id")
		return
	}
	target, err := h.Store.UserByID(r.Context(), id)
	if err != nil {
		auth.WriteError(w, 404, "no such user")
		return
	}
	var body struct {
		Plan *string `json:"plan"`
		Days int     `json:"days"`
	}
	if err := auth.ReadJSON(r, &body); err != nil {
		auth.WriteError(w, 400, "invalid json")
		return
	}
	if body.Plan == nil || *body.Plan == "" {
		if _, err := h.DB.Exec(r.Context(), `UPDATE users SET plan=NULL, plan_until=NULL WHERE id=$1`, id); err != nil {
			auth.WriteError(w, 500, "could not clear plan")
			return
		}
		h.Store.Audit(r.Context(), actor.Username, "plan_clear", target.Username, "")
		auth.WriteJSON(w, 200, map[string]any{"ok": true})
		return
	}
	if !IsPlanID(*body.Plan) {
		auth.WriteError(w, 400, "unknown plan")
		return
	}
	if body.Days <= 0 {
		body.Days = 30
	}
	until := time.Now().Add(time.Duration(body.Days) * 24 * time.Hour)
	if _, err := h.DB.Exec(r.Context(), `UPDATE users SET plan=$1, plan_until=$2 WHERE id=$3`, *body.Plan, until, id); err != nil {
		auth.WriteError(w, 500, "could not grant plan")
		return
	}
	h.DB.Exec(r.Context(), `INSERT INTO payment (invoice_id, user_id, tier, period_start, period_end, amount_cents, source, actor) VALUES ($1,$2,$3,NOW(),$4,NULL,'grant',$5)`,
		"grant_"+strconv.FormatInt(time.Now().UnixNano(), 36), id, Tier(PlanID(*body.Plan), Monthly), until, actor.Username)
	h.Store.Audit(r.Context(), actor.Username, "plan_grant", target.Username, *body.Plan+" for "+strconv.Itoa(body.Days)+" days")
	auth.WriteJSON(w, 200, map[string]any{"ok": true, "plan": *body.Plan, "until": until.UTC().Format(time.RFC3339)})
}

func (h *Handlers) adminRevenue(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	type bucket struct {
		Period string `json:"period"`
		Tier   string `json:"tier"`
		Cents  int64  `json:"cents"`
		Count  int    `json:"count"`
	}
	rows, err := h.DB.Query(ctx, `SELECT to_char(date_trunc('month', created), 'YYYY-MM'), tier, coalesce(sum(amount_cents),0), count(*)
		FROM payment WHERE source='stripe' AND amount_cents IS NOT NULL AND amount_cents > 0 GROUP BY 1, 2 ORDER BY 1 DESC, 2`)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	monthly := []bucket{}
	for rows.Next() {
		var b bucket
		rows.Scan(&b.Period, &b.Tier, &b.Cents, &b.Count)
		monthly = append(monthly, b)
	}
	daily := []bucket{}
	drows, err := h.DB.Query(ctx, `SELECT to_char(date_trunc('day', created), 'YYYY-MM-DD'), '', coalesce(sum(amount_cents),0), count(*)
		FROM payment WHERE source='stripe' AND amount_cents IS NOT NULL AND amount_cents > 0 AND created > NOW() - interval '30 days' GROUP BY 1 ORDER BY 1`)
	if err == nil {
		for drows.Next() {
			var b bucket
			drows.Scan(&b.Period, &b.Tier, &b.Cents, &b.Count)
			daily = append(daily, b)
		}
		drows.Close()
	}
	var total int64
	var tx int
	h.DB.QueryRow(ctx, `SELECT coalesce(sum(amount_cents),0), count(*) FROM payment WHERE source='stripe' AND amount_cents IS NOT NULL AND amount_cents > 0`).Scan(&total, &tx)
	auth.WriteJSON(w, 200, map[string]any{"monthly": monthly, "daily": daily, "totalCents": total, "transactions": tx})
}

func (h *Handlers) adminTransactions(w http.ResponseWriter, r *http.Request) {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	const size = 50
	rows, err := h.DB.Query(r.Context(), `SELECT p.invoice_id, u.username, p.tier, p.amount_cents, p.currency, p.source, p.actor, p.period_end, p.created
		FROM payment p JOIN users u ON u.id = p.user_id ORDER BY p.created DESC NULLS LAST, p.invoice_id DESC LIMIT $1 OFFSET $2`, size, (page-1)*size)
	if err != nil {
		auth.WriteError(w, 500, err.Error())
		return
	}
	defer rows.Close()
	type tx struct {
		ID          string     `json:"id"`
		Username    string     `json:"username"`
		Tier        string     `json:"tier"`
		AmountCents *int       `json:"amountCents"`
		Currency    string     `json:"currency"`
		Source      string     `json:"source"`
		Actor       *string    `json:"actor"`
		PeriodEnd   *time.Time `json:"periodEnd"`
		Created     time.Time  `json:"created"`
	}
	out := []tx{}
	for rows.Next() {
		var t tx
		rows.Scan(&t.ID, &t.Username, &t.Tier, &t.AmountCents, &t.Currency, &t.Source, &t.Actor, &t.PeriodEnd, &t.Created)
		out = append(out, t)
	}
	auth.WriteJSON(w, 200, map[string]any{"transactions": out, "pageSize": size})
}
