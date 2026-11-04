# Configuration

All configuration is environment variables. `.env` in the working directory is loaded at start (values already in the environment win). Copy `.env.example` and edit it; never commit `.env`.

## Server

| Variable | Default | Meaning |
|---|---|---|
| `HOST` | `127.0.0.1` | Bind address. Use `0.0.0.0` behind a reverse proxy or to reach it from other machines. |
| `PORT` | `3000` | Listen port. |
| `DEBUG` | `false` | `true` enables debug logging, including one line per HTTP request. |
| `STATIC_DIR` | `frontend/dist` | Where the built site is served from. |
| `DATABASE_URL` | `postgres://postgres@localhost:5432/elichat` | PostgreSQL connection string. The example file points at a dedicated user; generate a real password. |
| `PUBLIC_HOST` | empty | The public host of this deployment, with port if not 80/443. The workspace relay otherwise refuses private addresses; this lets the in-chat shell call Estro's own API. Loopback and the listen address are always allowed. |

## Accounts and caps

| Variable | Default | Meaning |
|---|---|---|
| `ADMIN_USERNAME` | empty | On every start, the user with this name is made an admin if not already. Set it once to bootstrap. |
| `REQUIRE_VERIFIED_EMAIL` | `true` | Refuse chat until the account's email is verified. Admins are exempt. |
| `DAILY_REQUEST_LIMIT` | `300` | Chat requests per account per UTC day across all models. `0` disables. Admins and sandbox users are exempt. |
| `RATE_AUTH` | `10,10` | Per-minute limit and burst for `POST /api/auth/*`, keyed by client IP. |
| `RATE_LLM` | `120,120` | Per-minute limit and burst for `POST /v1/*`, keyed by account. Keep this comfortably above what one agent loop produces. |
| `RATE_APP` | `300,300` | Per-minute limit and burst for the rest of `/api/`, keyed by account. |

A rate value is `perMinute` or `perMinute,burst`; `0` disables that tier. Buckets are per process.

## Billing

| Variable | Default | Meaning |
|---|---|---|
| `STRIPE_SECRET_KEY` | empty | Live Stripe secret. Unset, or containing `CHANGE_ME`, disables checkout (`503`) and the webhook. |
| `STRIPE_WEBHOOK_SECRET` | empty | Signing secret for `POST /api/stripe/webhook`. |
| `STRIPE_PORTAL_CONFIGURATION` | empty | Billing portal configuration id (`bpc_...`); required for the portal button. |
| `STRIPE_PRICE_<PLAN>_<MONTHLY\|ANNUAL>` | empty | Recurring price ids for `STARTER`, `PLUS`, `PRO`, `MAX`, `ULTRA`. |
| `STRIPE_SANDBOX_*` | empty | The same names for accounts flagged sandbox. |
| `SALES_OPEN` | `true` | `false` closes checkout and upgrades for everyone except sandbox accounts. |
| `PUBLIC_URL` | `https://PUBLIC_HOST` | Origin used in Checkout and portal return URLs. |

Plans, prices and window caps are code (`internal/billing/plans.go`), not configuration. See [Billing](billing.md).

### Security

`SESSION_SECRET` (optional) signs the short-lived token used between a password and its two-factor code. Set it to a long random string so that step survives a restart; unset, a random secret is generated per process.

`PUBLIC_HOST` also fixes the WebAuthn relying party id and origin for passkeys. Without it the request host is used, which is fine on `localhost` but must be set behind a real domain.

## Kali Gamma and Kali Beta

These two models are served through an HTTP relay provider.

| Variable | Default | Meaning |
|---|---|---|
| `CLIPROXY_BASE_URL` | the provider's base URL from the example file | Base URL of the relay; the three dialect paths are appended. |
| `CLIPROXY_API_KEY` | empty | Credential for the relay. When unset, Gamma and Beta are reported as unavailable and the server logs a warning. |

## Kali Epsilon

Epsilon is served by a browser-based provider in `internal/zai`: a pool of headed Chromium workers driven by `ext/zai_browser.py`. The common knobs:

| Variable | Default | Meaning |
|---|---|---|
| `ZAI_DISABLED` | `false` | `true` leaves Epsilon out entirely (no pool, model reported unavailable). |
| `ZAI_TRANSPORT` | `patchright` | `patchright` runs the browser pool; `browserless` uses direct HTTP and needs a captcha token source (`ZAI_EXT_TOKEN` or `ZAI_CAPTCHA_PARAM`). |
| `ZAI_BROWSER_POOL_SIZE` | `3` (example sets 2) | Concurrent browser workers, max 16. Each is a Chromium process; size to RAM. |
| `ZAI_TOKEN` | empty | A signed-in account token for the provider. Empty uses guest sessions, which allow only the default model. |
| `ZAI_CONFIG_FILE` | `zai_config.json` | File holding that token as `{"token":"..."}` when `ZAI_TOKEN` is unset. Git-ignored. |
| `ZAI_STREAM_HOLD` | `2000` (example sets 160) | Characters of answer text the worker buffers before streaming, so a turn that turns into a tool call can still be retried unseen. The default hides streaming on short replies; 160 is a good interactive value. |
| `ZAI_EXT_TOKEN` | empty | Shared secret for the captcha browser extension's WebSocket (browserless transport). Unset disables the endpoint. |

Less common: `ZAI_BROWSER_PROXY` (comma-separated SOCKS5/HTTP proxies, or `mullvad`), `ZAI_BROWSER_SCRIPT`, `ZAI_PYTHON`, `ZAI_CAPTCHA_PARAM`, `ZAI_ENABLE_THINKING` (force thinking on or off instead of letting the client decide), `ZAI_REMINDER_BUDGET` and `ZAI_HISTORY_RESULT_BUDGET` (character budgets when long histories are flattened), `ZAI_SEND_TIMEOUT`, `ZAI_STALL_SECONDS`, `ZAI_STALL_RETRIES`, `ZAI_DEBUG`. They are documented in the package comments of `internal/zai`.

## Mail

| Variable | Default | Meaning |
|---|---|---|
| `SMTP_HOST` | empty | SMTP server. With no host, or no password, codes are logged instead of sent. |
| `SMTP_PORT` | `587` | `465` implies implicit TLS. |
| `SMTP_SECURE` | derived from port | `true` forces implicit TLS (SMTPS); otherwise STARTTLS is used when offered. |
| `SMTP_USER` | the `SMTP_FROM` address | Login name. |
| `SMTP_PASS` | empty | Password. Required for the mailer to be enabled. |
| `SMTP_FROM` | `Estro <noreply@localhost>` | Sender, with optional display name. |

See also: [Deployment](deployment.md).
