# Billing

Estro sells five plans through Stripe. A plan buys usage in two rolling windows; accounts without a plan get Kali Epsilon on a small daily pool. Everything here lives in `internal/billing`, and the pricing page at `/pricing` reads the same table through `GET /api/billing/plans`.

## Plans

| Plan | Monthly | Annual (per month) | 5-hour window | Weekly window |
|---|---|---|---|---|
| Starter | $8 | $6.67 | ₪560,000 | ₪920,000 |
| Plus (recommended) | $24 | $20 | ₪1,360,000 | ₪2,200,000 |
| Pro | $44 | $36.67 | ₪2,160,000 | ₪3,680,000 |
| Max | $109 | $90.83 | ₪4,320,000 | ₪9,200,000 |
| Ultra | $219 | $182.50 | ₪8,000,000 | ₪18,400,000 |

Annual billing is one charge a year at ten monthly prices; the card shows that charge divided by twelve. Every paid plan has the same features (all three models, the API, IDE integrations, meters); tiers differ only in usage. The table is `Plans` in `internal/billing/plans.go`; `frontend/src/lib/plans.ts` carries the same numbers for first paint and the landing page.

## Free tier

Without an active plan an account may use **Kali Epsilon only**, from the site, within a pool of ₪50,000 a day (UTC). Kali Gamma and Kali Beta answer `402 billing_error` ("Kali Gamma requires an active Estro plan."), appear locked in the model menu, and the API (bearer tokens and keys) answers `402` for every model. Admins are exempt from all of this. The list is `FreeModels` in `plans.go` and comes back as `freeModels` from `/api/billing/plans` and `requiresPlan` on `/api/models`.

## Units

Usage is measured in shekels (₪). One request costs

```
(fresh input tokens + cached input tokens x 0.1 + output tokens x 5) x model multiplier
```

Token counts come from the upstream's `usage` block (OpenAI, Responses and Anthropic shapes are all read); when a provider reports none, the request is estimated at one token per four characters. Every Kali model is 1x today (`ModelMultiplier`).

## Windows

Each plan has a 5-hour and a weekly cap. A window starts on first use and expires exactly its length later, whether or not it was used; the next request starts a new one. Preflight refuses a request once `used >= cap` in either window with `429`:

```json
{"error":{"message":"Estro usage limit reached. 5h window: 1360000/1360000. Resets in 2h 14m.","type":"usage_limit_reached",
  "window":"5h","used":1360000,"limit":1360000,"resets_in":8040,"resets_at":1762200000,
  "limit_window_minutes":300,"model_multiplier":1}}
```

plus `Retry-After`, `x-codex-rate-limit-reached-type: rate_limit_reached` and `x-codex-promo-message`. Every admitted reply carries `x-codex-primary-used-percent`, `x-codex-primary-window-minutes`, `x-codex-primary-reset-at` and the `secondary` trio, and `GET /v1/usage` returns the same in the Codex `/wham/usage` shape. Rows live in `usage_window` (one per user and window) and `usage_event` (one per request).

## Stripe

Checkout, the billing portal, plan changes and the webhook are under `/api/stripe/*`:

| Route | Does |
|---|---|
| `POST /api/stripe/checkout?tier=estro_plus_month` | Creates a Checkout session (subscription mode, promotion codes on). `409` when the account already has a plan, `410` when sales are closed, `403` when the email is unverified, `503` when Stripe is not configured. Returns to `/pricing?checkout=complete`. |
| `POST /api/stripe/portal?tier=estro_account` | Opens the billing portal (payment method, invoices, cancel). Needs `STRIPE_PORTAL_CONFIGURATION`. |
| `GET /api/stripe/change/preview?tier=` | What a change costs today (upgrade, prorated) or when it takes effect (downgrade, period end). Nothing changes. |
| `POST /api/stripe/change?tier=` | Applies an upgrade now (`error_if_incomplete`; a declined card answers `402` with `paymentFailed: true`) or schedules a downgrade through a Subscription Schedule. |
| `POST /api/stripe/change/undo` | Releases a scheduled downgrade. |
| `POST /api/stripe/webhook` | Signed events. Tries the live secret, then the sandbox one. |

Tiers are `estro_<plan>_<month|year>`. The webhook handles `checkout.session.completed`, `customer.subscription.updated`, `customer.subscription.deleted`, `invoice.paid` and `invoice.payment_failed`, records every event id in `billing_event` and applies the plan change, the `estro_subscription` row and the `payment` row in one transaction, so a replayed event is a no-op. Point the endpoint at `https://<host>/api/stripe/webhook` and subscribe it to those five events.

Accounts flagged `sandbox` use the `STRIPE_SANDBOX_*` keys and their own customer id column, and may buy while `SALES_OPEN=false`.

## Grants

`POST /api/admin/users/{id}/plan` with `{"plan":"pro","days":30}` sets `users.plan` and `users.plan_until` directly (no Stripe), writes a `payment` row with `source='grant'`, and audits `plan_grant`; `{"plan":null}` clears it (`plan_clear`). A `plan_until` decades away is treated as a plan with no end: the account page says "Ultra, no renewal date" and offers no portal. Setting those two columns by hand in SQL behaves the same way.

## Environment

| Variable | Meaning |
|---|---|
| `STRIPE_SECRET_KEY`, `STRIPE_WEBHOOK_SECRET` | Live keys. Unset or containing `CHANGE_ME` disables checkout (`503`). |
| `STRIPE_PORTAL_CONFIGURATION` | `bpc_...` id for the portal. |
| `STRIPE_PRICE_<PLAN>_<MONTHLY|ANNUAL>` | Ten recurring price ids. |
| `STRIPE_SANDBOX_*` | The same names for sandbox accounts. |
| `SALES_OPEN` | Default `true`. `false` closes checkout and upgrades for everyone but sandbox accounts. |
| `PUBLIC_URL` | Origin for Checkout and portal return URLs; falls back to `https://PUBLIC_HOST`. |

## Admin

The admin panel shows each user's plan, grants and clears plans from the Manage dialog, and has a Revenue tab (monthly revenue by tier, recurring line, daily revenue, transactions). Numbers come from `GET /api/admin/billing/overview`, `GET /api/admin/revenue`, `GET /api/admin/transactions?page=` and `GET /api/admin/series?days=30`. Revenue counts real Stripe payments only; grants are listed but never summed.
