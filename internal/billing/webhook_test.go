package billing

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/joho/godotenv"
	"github.com/stripe/stripe-go/v82"
	"github.com/stripe/stripe-go/v82/webhook"

	"elichat/internal/db"
)

// These tests need a database (DATABASE_URL, or the repo's .env). They create a
// throwaway user, drive the webhook with signed sandbox events, and clean up.

const testWebhookSecret = "whsec_test_" + "estro_billing"

func testDB(t *testing.T) *db.DB {
	t.Helper()
	godotenv.Load("../../.env")
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL unset")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Skipf("database unreachable: %v", err)
	}
	if err := d.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(d.Close)
	return d
}

// signedEvent builds a Stripe event, signs it like Stripe would, and verifies it
// the way the webhook route does, so the signature path is exercised too.
func signedEvent(t *testing.T, id, typ string, object map[string]any) *stripe.Event {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"id": id, "object": "event", "type": typ, "livemode": false, "created": time.Now().Unix(), "api_version": stripe.APIVersion,
		"data": map[string]any{"object": object},
	})
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: body, Secret: testWebhookSecret})
	ev, err := webhook.ConstructEventWithOptions(signed.Payload, signed.Header, testWebhookSecret, webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true})
	if err != nil {
		t.Fatalf("construct event: %v", err)
	}
	return &ev
}

func TestWebhookActivatesReplaysAndEnds(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	t.Setenv("STRIPE_SANDBOX_SECRET_KEY", "sk_test_fake")
	t.Setenv("STRIPE_SANDBOX_WEBHOOK_SECRET", testWebhookSecret)
	t.Setenv("STRIPE_SANDBOX_PRICE_PLUS_MONTHLY", "price_test_plus_month")

	n := time.Now().UnixNano()
	username := fmt.Sprintf("billtest_%d", n)
	customer := fmt.Sprintf("cus_test_%d", n)
	var userID int
	if err := d.QueryRow(ctx, `INSERT INTO users (username, password_hash, email_verified, plan_stripe_sandbox_customer_id) VALUES ($1, '', TRUE, $2) RETURNING id`, username, customer).Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	t.Cleanup(func() {
		d.Exec(ctx, `DELETE FROM billing_event WHERE stripe_event_id LIKE $1`, fmt.Sprintf("evt_test_%d_%%", n))
		d.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	evID := func(s string) string { return fmt.Sprintf("evt_test_%d_%s", n, s) }
	periodEnd := time.Now().Add(30 * 24 * time.Hour).Unix()
	subscription := map[string]any{
		"id": "sub_test_" + username, "object": "subscription", "status": "active", "customer": customer,
		"metadata": map[string]any{"userId": fmt.Sprint(userID)},
		"items": map[string]any{"object": "list", "data": []any{map[string]any{
			"id": "si_test", "object": "subscription_item", "current_period_end": periodEnd,
			"price": map[string]any{"id": "price_test_plus_month", "object": "price", "recurring": map[string]any{"interval": "month"}},
		}}},
	}

	// Activation.
	updated := signedEvent(t, evID("sub_updated"), "customer.subscription.updated", subscription)
	if err := HandleWebhookEvent(ctx, d, updated); err != nil {
		t.Fatalf("activate: %v", err)
	}
	var plan *string
	var until *time.Time
	if err := d.QueryRow(ctx, `SELECT plan, plan_until FROM users WHERE id=$1`, userID).Scan(&plan, &until); err != nil {
		t.Fatal(err)
	}
	if plan == nil || *plan != "plus" || until == nil || until.Unix() != periodEnd {
		t.Fatalf("plan after activation = %v until %v, want plus until %d", plan, until, periodEnd)
	}
	var interval string
	if err := d.QueryRow(ctx, `SELECT billing_interval FROM estro_subscription WHERE user_id=$1`, userID).Scan(&interval); err != nil || interval != "monthly" {
		t.Fatalf("estro_subscription interval = %q, err %v", interval, err)
	}
	row, err := ReadBillingRow(ctx, d, userID)
	if err != nil || row.ActivePlan(time.Now()) != Plus {
		t.Fatalf("ActivePlan = %q, err %v", row.ActivePlan(time.Now()), err)
	}

	// Replay: same event id again does nothing and is recorded once.
	d.Exec(ctx, `UPDATE users SET plan='starter' WHERE id=$1`, userID)
	if err := HandleWebhookEvent(ctx, d, updated); err != nil {
		t.Fatalf("replay: %v", err)
	}
	d.QueryRow(ctx, `SELECT plan FROM users WHERE id=$1`, userID).Scan(&plan)
	if plan == nil || *plan != "starter" {
		t.Fatalf("replayed event changed the plan to %v", plan)
	}
	var seen int
	d.QueryRow(ctx, `SELECT count(*) FROM billing_event WHERE stripe_event_id=$1`, evID("sub_updated")).Scan(&seen)
	if seen != 1 {
		t.Fatalf("billing_event rows = %d, want 1", seen)
	}

	// A paid invoice becomes a payment row, once.
	invoice := map[string]any{
		"id": "in_test_" + username, "object": "invoice", "customer": customer, "amount_paid": 2400, "currency": "usd",
		"parent": map[string]any{"type": "subscription_details", "subscription_details": map[string]any{"subscription": "sub_test_" + username}},
		"lines": map[string]any{"object": "list", "data": []any{map[string]any{
			"id": "il_test", "object": "line_item",
			"period":  map[string]any{"start": time.Now().Unix(), "end": periodEnd},
			"pricing": map[string]any{"price_details": map[string]any{"price": "price_test_plus_month"}},
		}}},
	}
	paid := signedEvent(t, evID("invoice_paid"), "invoice.paid", invoice)
	for i := 0; i < 2; i++ {
		if err := HandleWebhookEvent(ctx, d, paid); err != nil {
			t.Fatalf("invoice.paid #%d: %v", i, err)
		}
	}
	var payments int
	var tier string
	var cents int
	d.QueryRow(ctx, `SELECT count(*), max(tier), max(amount_cents) FROM payment WHERE user_id=$1 AND source='stripe'`, userID).Scan(&payments, &tier, &cents)
	if payments != 1 || tier != "estro_plus_month" || cents != 2400 {
		t.Fatalf("payment rows = %d tier %q cents %d", payments, tier, cents)
	}

	// Deletion (no customer on the object, so no Stripe lookup for other subscriptions) ends the plan now.
	ended := map[string]any{"id": "sub_test_" + username, "object": "subscription", "status": "canceled", "metadata": map[string]any{"userId": fmt.Sprint(userID)}}
	if err := HandleWebhookEvent(ctx, d, signedEvent(t, evID("sub_deleted"), "customer.subscription.deleted", ended)); err != nil {
		t.Fatalf("delete: %v", err)
	}
	row, _ = ReadBillingRow(ctx, d, userID)
	if row.ActivePlan(time.Now().Add(time.Second)) != "" {
		t.Fatalf("plan still active after deletion: %+v", row)
	}
	var subs int
	d.QueryRow(ctx, `SELECT count(*) FROM estro_subscription WHERE user_id=$1`, userID).Scan(&subs)
	if subs != 0 {
		t.Fatalf("estro_subscription rows after deletion = %d", subs)
	}
}

func TestWebhookBadSignatureIsRejected(t *testing.T) {
	body := []byte(`{"id":"evt_x","object":"event","type":"invoice.paid","data":{"object":{}}}`)
	signed := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{Payload: body, Secret: "whsec_other"})
	if _, err := webhook.ConstructEventWithOptions(signed.Payload, signed.Header, testWebhookSecret, webhook.ConstructEventOptions{IgnoreAPIVersionMismatch: true}); err == nil {
		t.Fatal("event signed with another secret was accepted")
	}
}

// Preflight through the stored windows: a full five-hour window refuses, and the
// same row read after its expiry admits again.
func TestPreflightAgainstStoredWindow(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()
	n := time.Now().UnixNano()
	var userID int
	if err := d.QueryRow(ctx, `INSERT INTO users (username, password_hash, email_verified, plan, plan_until) VALUES ($1, '', TRUE, 'plus', NOW() + interval '30 days') RETURNING id`, fmt.Sprintf("windowtest_%d", n)).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Exec(ctx, `DELETE FROM users WHERE id=$1`, userID) })
	plan, _ := PlanByID("plus")
	if _, err := d.Exec(ctx, `INSERT INTO usage_window (user_id, kind, window_start, used_units) VALUES ($1, 'five_hour', NOW() - interval '1 hour', $2)`, userID, plan.FiveHourUnits); err != nil {
		t.Fatalf("insert window: %v", err)
	}
	snap, err := ReadSnapshot(ctx, d, userID)
	if err != nil {
		t.Fatal(err)
	}
	limit, ok := Preflight(Plus, 1, snap)
	if ok || limit.Window != "5h" || limit.ResetsIn <= 0 || limit.ResetsIn > 4*3600 {
		t.Fatalf("Preflight on a full window = ok %v, %+v", ok, limit)
	}
	if _, err := d.Exec(ctx, `UPDATE usage_window SET window_start = NOW() - interval '6 hours' WHERE user_id=$1`, userID); err != nil {
		t.Fatal(err)
	}
	snap, _ = ReadSnapshot(ctx, d, userID)
	if _, ok := Preflight(Plus, 1, snap); !ok {
		t.Fatal("Preflight refused after the window expired")
	}
}
