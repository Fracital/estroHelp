package billing

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stripe/stripe-go/v82"

	"elichat/internal/db"
)

type Mode string

const (
	Live    Mode = "live"
	Sandbox Mode = "sandbox"
)

var customerColumn = map[Mode]string{Live: "plan_stripe_customer_id", Sandbox: "plan_stripe_sandbox_customer_id"}

// Keys for one mode. Live reads STRIPE_*; sandbox reads STRIPE_SANDBOX_*.
type Keys struct {
	SecretKey           string
	WebhookSecret       string
	PortalConfiguration string
	Prices              map[string]string // tier -> price id
}

func isSet(v string) bool { return strings.TrimSpace(v) != "" && !strings.Contains(v, "CHANGE_ME") }

func keysFromEnv(prefix string) *Keys {
	k := &Keys{SecretKey: os.Getenv(prefix + "SECRET_KEY"), WebhookSecret: os.Getenv(prefix + "WEBHOOK_SECRET"), PortalConfiguration: os.Getenv(prefix + "PORTAL_CONFIGURATION"), Prices: map[string]string{}}
	if !isSet(k.SecretKey) {
		return nil
	}
	if !isSet(k.PortalConfiguration) {
		k.PortalConfiguration = ""
	}
	for _, p := range Plans {
		up := strings.ToUpper(string(p.ID))
		if v := os.Getenv(prefix + "PRICE_" + up + "_MONTHLY"); isSet(v) {
			k.Prices[Tier(p.ID, Monthly)] = v
		}
		if v := os.Getenv(prefix + "PRICE_" + up + "_ANNUAL"); isSet(v) {
			k.Prices[Tier(p.ID, Annual)] = v
		}
	}
	return k
}

// KeysFor returns the configured keys for a mode, or nil when billing is off for it.
func KeysFor(mode Mode) *Keys {
	if mode == Sandbox {
		return keysFromEnv("STRIPE_SANDBOX_")
	}
	return keysFromEnv("STRIPE_")
}

func ModeFor(sandbox bool) Mode {
	if sandbox {
		return Sandbox
	}
	return Live
}

func (k *Keys) PriceID(plan PlanID, interval Interval) string { return k.Prices[Tier(plan, interval)] }

func (k *Keys) PlanForPrice(priceID string) (PlanID, Interval, bool) {
	if priceID == "" {
		return "", "", false
	}
	for tier, id := range k.Prices {
		if id == priceID {
			plan, interval, _ := ParseTier(tier)
			return plan, interval, true
		}
	}
	return "", "", false
}

var (
	clientsMu sync.Mutex
	clients   = map[string]*stripe.Client{}
)

// Client is one Stripe client per secret key; live and sandbox calls alternate.
func Client(secretKey string) *stripe.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	c, ok := clients[secretKey]
	if !ok {
		c = stripe.NewClient(secretKey)
		clients[secretKey] = c
	}
	return c
}

// PublicURL is the site origin for return URLs (PUBLIC_URL, else https://PUBLIC_HOST).
func PublicURL(fallbackHost string) string {
	if v := strings.TrimRight(os.Getenv("PUBLIC_URL"), "/"); v != "" {
		return v
	}
	if h := os.Getenv("PUBLIC_HOST"); h != "" {
		return "https://" + h
	}
	return fallbackHost
}

// grantingStatuses keep the plan until the period end; past_due because Stripe is still retrying.
var grantingStatuses = map[stripe.SubscriptionStatus]bool{stripe.SubscriptionStatusActive: true, stripe.SubscriptionStatusTrialing: true, stripe.SubscriptionStatusPastDue: true}

// State mirrors the Stripe subscription the site shows; it never decides entitlement.
type State struct {
	SubscriptionID string
	Plan           PlanID
	Interval       Interval
	Status         string
	PeriodEnd      time.Time
	CancelAt       *time.Time
	ScheduleID     string
	Next           *NextChange
}

type NextChange struct {
	Plan     PlanID
	Interval Interval
	At       time.Time
}

type entitlementChange struct {
	userID     int
	customerID string
	plan       PlanID // empty when the change ends the plan
	until      time.Time
	state      *State
	endedSubID string
}

type BillingRow struct {
	Plan              *string
	PlanUntil         *time.Time
	LiveCustomerID    *string
	SandboxCustomerID *string
}

func (r *BillingRow) Customer(mode Mode) string {
	if r == nil {
		return ""
	}
	if mode == Sandbox {
		if r.SandboxCustomerID != nil {
			return *r.SandboxCustomerID
		}
		return ""
	}
	if r.LiveCustomerID != nil {
		return *r.LiveCustomerID
	}
	return ""
}

// ActivePlan is the plan a user is entitled to now, or "".
func (r *BillingRow) ActivePlan(now time.Time) PlanID {
	if r == nil || r.Plan == nil || r.PlanUntil == nil || !IsPlanID(*r.Plan) || !r.PlanUntil.After(now) {
		return ""
	}
	return PlanID(*r.Plan)
}

func ReadBillingRow(ctx context.Context, d *db.DB, userID int) (*BillingRow, error) {
	var r BillingRow
	err := d.QueryRow(ctx, `SELECT plan, plan_until, plan_stripe_customer_id, plan_stripe_sandbox_customer_id FROM users WHERE id=$1`, userID).
		Scan(&r.Plan, &r.PlanUntil, &r.LiveCustomerID, &r.SandboxCustomerID)
	if err != nil {
		return nil, err
	}
	return &r, nil
}

type userRef struct {
	ID       int
	Username string
	Email    string
}

// customerFor returns the mode's Stripe customer, creating and storing it on first use.
func customerFor(ctx context.Context, d *db.DB, c *stripe.Client, u userRef, mode Mode, existing string) (string, error) {
	if existing != "" {
		return existing, nil
	}
	params := &stripe.CustomerCreateParams{Name: stripe.String(u.Username), Metadata: map[string]string{"userId": strconv.Itoa(u.ID), "username": u.Username, "product": "estro"}}
	if u.Email != "" {
		params.Email = stripe.String(u.Email)
	}
	cust, err := c.V1Customers.Create(ctx, params)
	if err != nil {
		return "", err
	}
	col := customerColumn[mode]
	if _, err := d.Exec(ctx, `UPDATE users SET `+col+` = $1 WHERE id = $2 AND `+col+` IS NULL`, cust.ID, u.ID); err != nil {
		return "", err
	}
	row, err := ReadBillingRow(ctx, d, u.ID)
	if err != nil {
		return cust.ID, nil
	}
	if stored := row.Customer(mode); stored != "" {
		return stored, nil
	}
	return cust.ID, nil
}

func idOfSub(s *stripe.Subscription) string {
	if s == nil {
		return ""
	}
	return s.ID
}

func customerIDOf(c *stripe.Customer) string {
	if c == nil {
		return ""
	}
	return c.ID
}

func firstItem(s *stripe.Subscription) *stripe.SubscriptionItem {
	if s == nil || s.Items == nil || len(s.Items.Data) == 0 {
		return nil
	}
	return s.Items.Data[0]
}

func periodEndOf(s *stripe.Subscription) time.Time {
	if it := firstItem(s); it != nil && it.CurrentPeriodEnd > 0 {
		return time.Unix(it.CurrentPeriodEnd, 0)
	}
	return time.Time{}
}

func priceIDOf(s *stripe.Subscription) string {
	if it := firstItem(s); it != nil && it.Price != nil {
		return it.Price.ID
	}
	return ""
}

// stateFor reads a Stripe subscription into the stored state. Nil when its price is not ours.
func stateFor(ctx context.Context, k *Keys, c *stripe.Client, s *stripe.Subscription) (*State, error) {
	plan, interval, ok := k.PlanForPrice(priceIDOf(s))
	end := periodEndOf(s)
	if !ok || end.IsZero() {
		return nil, nil
	}
	st := &State{SubscriptionID: s.ID, Plan: plan, Interval: interval, Status: string(s.Status), PeriodEnd: end}
	if s.Schedule != nil && s.Schedule.ID != "" {
		st.ScheduleID = s.Schedule.ID
		next, err := pendingChange(ctx, k, c, s.Schedule.ID)
		if err != nil {
			return nil, err
		}
		st.Next = next
	}
	if s.CancelAtPeriodEnd {
		t := end
		st.CancelAt = &t
	} else if s.CancelAt > 0 {
		t := time.Unix(s.CancelAt, 0)
		st.CancelAt = &t
	}
	return st, nil
}

func pendingChange(ctx context.Context, k *Keys, c *stripe.Client, scheduleID string) (*NextChange, error) {
	sched, err := c.V1SubscriptionSchedules.Retrieve(ctx, scheduleID, nil)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	for _, ph := range sched.Phases {
		if ph.StartDate <= now || len(ph.Items) == 0 || ph.Items[0].Price == nil {
			continue
		}
		plan, interval, ok := k.PlanForPrice(ph.Items[0].Price.ID)
		if !ok {
			return nil, nil
		}
		return &NextChange{Plan: plan, Interval: interval, At: time.Unix(ph.StartDate, 0)}, nil
	}
	return nil, nil
}

func userIDFor(ctx context.Context, d *db.DB, customerID, fallback string, mode Mode) (int, error) {
	var id int
	if customerID != "" {
		err := d.QueryRow(ctx, `SELECT id FROM users WHERE `+customerColumn[mode]+` = $1`, customerID).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}
	if n, err := strconv.Atoi(fallback); err == nil && n > 0 {
		err := d.QueryRow(ctx, `SELECT id FROM users WHERE id = $1`, n).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}
	return 0, nil
}

func subscriptionChange(ctx context.Context, d *db.DB, k *Keys, c *stripe.Client, s *stripe.Subscription, fallbackUserID string, mode Mode) (*entitlementChange, error) {
	customerID := customerIDOf(s.Customer)
	userID, err := userIDFor(ctx, d, customerID, fallbackUserID, mode)
	if err != nil || userID == 0 {
		return nil, err
	}
	if !grantingStatuses[s.Status] {
		return &entitlementChange{userID: userID, customerID: customerID, until: time.Now(), endedSubID: s.ID}, nil
	}
	st, err := stateFor(ctx, k, c, s)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("subscription uses a price that is not configured, or has no period end")
	}
	return &entitlementChange{userID: userID, customerID: customerID, plan: st.Plan, until: st.PeriodEnd, state: st}, nil
}

func writeState(ctx context.Context, tx pgx.Tx, userID int, mode Mode, st *State) error {
	var nextPlan, nextInterval *string
	var nextAt *time.Time
	if st.Next != nil {
		p, i := string(st.Next.Plan), string(st.Next.Interval)
		nextPlan, nextInterval, nextAt = &p, &i, &st.Next.At
	}
	var scheduleID *string
	if st.ScheduleID != "" {
		scheduleID = &st.ScheduleID
	}
	_, err := tx.Exec(ctx, `INSERT INTO estro_subscription
		(user_id, mode, subscription_id, plan, billing_interval, status, period_end, cancel_at, schedule_id, next_plan, next_billing_interval, next_at, updated)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,NOW())
		ON CONFLICT (user_id) DO UPDATE SET mode=EXCLUDED.mode, subscription_id=EXCLUDED.subscription_id, plan=EXCLUDED.plan,
		  billing_interval=EXCLUDED.billing_interval, status=EXCLUDED.status, period_end=EXCLUDED.period_end, cancel_at=EXCLUDED.cancel_at,
		  schedule_id=EXCLUDED.schedule_id, next_plan=EXCLUDED.next_plan, next_billing_interval=EXCLUDED.next_billing_interval, next_at=EXCLUDED.next_at, updated=NOW()`,
		userID, string(mode), st.SubscriptionID, string(st.Plan), string(st.Interval), st.Status, st.PeriodEnd, st.CancelAt, scheduleID, nextPlan, nextInterval, nextAt)
	return err
}

func applyChange(ctx context.Context, tx pgx.Tx, mode Mode, ch *entitlementChange) error {
	col := customerColumn[mode]
	var plan, customer *string
	if ch.plan != "" {
		p := string(ch.plan)
		plan = &p
	}
	if ch.customerID != "" {
		customer = &ch.customerID
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET plan = COALESCE($2, plan), plan_until = $3, `+col+` = COALESCE(`+col+`, $4) WHERE id = $1`, ch.userID, plan, ch.until, customer); err != nil {
		return err
	}
	if ch.state != nil {
		return writeState(ctx, tx, ch.userID, mode, ch.state)
	}
	if ch.endedSubID != "" {
		_, err := tx.Exec(ctx, `DELETE FROM estro_subscription WHERE user_id = $1 AND subscription_id = $2`, ch.userID, ch.endedSubID)
		return err
	}
	return nil
}

// entitlementChangeFor turns a verified event into the entitlement change it implies.
func entitlementChangeFor(ctx context.Context, d *db.DB, k *Keys, c *stripe.Client, ev *stripe.Event, mode Mode) (*entitlementChange, error) {
	switch ev.Type {
	case "checkout.session.completed":
		var session stripe.CheckoutSession
		if err := unmarshalObject(ev, &session); err != nil {
			return nil, err
		}
		if session.Mode != stripe.CheckoutSessionModeSubscription || session.Subscription == nil {
			return nil, nil
		}
		sub, err := c.V1Subscriptions.Retrieve(ctx, session.Subscription.ID, nil)
		if err != nil {
			return nil, err
		}
		fallback := session.Metadata["userId"]
		if fallback == "" {
			fallback = session.ClientReferenceID
		}
		return subscriptionChange(ctx, d, k, c, sub, fallback, mode)
	case "customer.subscription.updated":
		var sub stripe.Subscription
		if err := unmarshalObject(ev, &sub); err != nil {
			return nil, err
		}
		return subscriptionChange(ctx, d, k, c, &sub, sub.Metadata["userId"], mode)
	case "customer.subscription.deleted":
		var ended stripe.Subscription
		if err := unmarshalObject(ev, &ended); err != nil {
			return nil, err
		}
		customerID := customerIDOf(ended.Customer)
		if customerID != "" {
			for other, err := range c.V1Subscriptions.List(ctx, &stripe.SubscriptionListParams{Customer: stripe.String(customerID), ListParams: stripe.ListParams{Limit: stripe.Int64(20)}}) {
				if err != nil {
					return nil, err
				}
				if other.ID == ended.ID || !grantingStatuses[other.Status] {
					continue
				}
				if _, _, ok := k.PlanForPrice(priceIDOf(other)); !ok {
					continue
				}
				return subscriptionChange(ctx, d, k, c, other, ended.Metadata["userId"], mode)
			}
		}
		userID, err := userIDFor(ctx, d, customerID, ended.Metadata["userId"], mode)
		if err != nil || userID == 0 {
			return nil, err
		}
		return &entitlementChange{userID: userID, customerID: customerID, until: time.Now(), endedSubID: ended.ID}, nil
	}
	return nil, nil
}

// paymentFor records a paid invoice for revenue reports. Nil when it is not ours.
type paymentRow struct {
	invoiceID, subscriptionID, tier, currency string
	userID                                    int
	amountCents                               int64
	periodStart, periodEnd                    *time.Time
}

func paymentFor(ctx context.Context, d *db.DB, k *Keys, ev *stripe.Event, mode Mode) (*paymentRow, error) {
	var inv stripe.Invoice
	if err := unmarshalObject(ev, &inv); err != nil {
		return nil, err
	}
	var priceID, subID string
	var start, end *time.Time
	if inv.Lines != nil {
		for _, line := range inv.Lines.Data {
			if line.Pricing != nil && line.Pricing.PriceDetails != nil && line.Pricing.PriceDetails.Price != "" {
				priceID = line.Pricing.PriceDetails.Price
			}
			if line.Period != nil {
				s, e := time.Unix(line.Period.Start, 0), time.Unix(line.Period.End, 0)
				start, end = &s, &e
			}
		}
	}
	if inv.Parent != nil && inv.Parent.SubscriptionDetails != nil && inv.Parent.SubscriptionDetails.Subscription != nil {
		subID = inv.Parent.SubscriptionDetails.Subscription.ID
	}
	plan, interval, ok := k.PlanForPrice(priceID)
	if !ok {
		return nil, nil
	}
	userID, err := userIDFor(ctx, d, customerIDOf(inv.Customer), "", mode)
	if err != nil || userID == 0 {
		return nil, err
	}
	return &paymentRow{invoiceID: inv.ID, subscriptionID: subID, tier: Tier(plan, interval), currency: string(inv.Currency), userID: userID, amountCents: inv.AmountPaid, periodStart: start, periodEnd: end}, nil
}

// HandleWebhookEvent applies one verified event. The event id is recorded in the
// same transaction as the entitlement change, so a replayed event does nothing.
func HandleWebhookEvent(ctx context.Context, d *db.DB, ev *stripe.Event) error {
	mode := Live
	if !ev.Livemode {
		mode = Sandbox
	}
	k := KeysFor(mode)
	if k == nil {
		return fmt.Errorf("billing is not configured (%s)", mode)
	}
	var seen int
	if err := d.QueryRow(ctx, `SELECT count(*) FROM billing_event WHERE stripe_event_id=$1`, ev.ID).Scan(&seen); err != nil {
		return err
	}
	if seen > 0 {
		return nil
	}
	c := Client(k.SecretKey)
	var change *entitlementChange
	var pay *paymentRow
	var err error
	switch ev.Type {
	case "invoice.paid":
		pay, err = paymentFor(ctx, d, k, ev, mode)
	case "invoice.payment_failed":
		slog.Warn("stripe invoice payment failed", "event", ev.ID)
	default:
		change, err = entitlementChangeFor(ctx, d, k, c, ev, mode)
	}
	if err != nil {
		return err
	}
	tx, err := d.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `INSERT INTO billing_event (stripe_event_id) VALUES ($1) ON CONFLICT (stripe_event_id) DO NOTHING`, ev.ID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if change != nil {
		if err := applyChange(ctx, tx, mode, change); err != nil {
			return err
		}
	}
	if pay != nil {
		if _, err := tx.Exec(ctx, `INSERT INTO payment (invoice_id, subscription_id, user_id, tier, period_start, period_end, amount_cents, currency, source)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'stripe') ON CONFLICT (invoice_id) DO NOTHING`,
			pay.invoiceID, nullable(pay.subscriptionID), pay.userID, pay.tier, pay.periodStart, pay.periodEnd, pay.amountCents, pay.currency); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func unmarshalObject(ev *stripe.Event, v any) error {
	return jsonUnmarshal(ev.Data.Raw, v)
}
