-- Applied in one transaction on every start. Keep every statement idempotent:
-- append new ALTER TABLE ... ADD COLUMN IF NOT EXISTS lines rather than editing
-- existing CREATE TABLE blocks once a column has shipped.

CREATE TABLE IF NOT EXISTS users (
    id SERIAL PRIMARY KEY,
    username TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    email TEXT UNIQUE,
    email_verified BOOLEAN NOT NULL DEFAULT FALSE,
    email_code_hash TEXT,
    email_code_expires TIMESTAMPTZ,
    new_email TEXT,
    reset_code_hash TEXT,
    reset_code_expires TIMESTAMPTZ,
    admin BOOLEAN NOT NULL DEFAULT FALSE,
    sandbox BOOLEAN NOT NULL DEFAULT FALSE,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_login TIMESTAMPTZ
);

CREATE TABLE IF NOT EXISTS session (
    token_hash TEXT PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires TIMESTAMPTZ NOT NULL,
    last_seen TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    ip TEXT,
    user_agent TEXT
);
CREATE INDEX IF NOT EXISTS session_user_idx ON session(user_id);

CREATE TABLE IF NOT EXISTS ban (
    id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    reason TEXT NOT NULL DEFAULT '',
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires TIMESTAMPTZ,
    lifted TIMESTAMPTZ,
    actor TEXT
);
CREATE INDEX IF NOT EXISTS ban_user_idx ON ban(user_id);

CREATE TABLE IF NOT EXISTS thread (
    id TEXT PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    title TEXT NOT NULL DEFAULT 'New chat',
    model TEXT NOT NULL,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS thread_user_idx ON thread(user_id, updated DESC);

CREATE TABLE IF NOT EXISTS thread_message (
    id SERIAL PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES thread(id) ON DELETE CASCADE,
    seq INT NOT NULL,
    role TEXT NOT NULL,
    content TEXT NOT NULL,
    model TEXT,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (thread_id, seq)
);

CREATE TABLE IF NOT EXISTS ai_usage (
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    day DATE NOT NULL,
    model TEXT NOT NULL,
    requests INT NOT NULL DEFAULT 0,
    prompt_chars BIGINT NOT NULL DEFAULT 0,
    sandbox BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, day, model)
);

CREATE TABLE IF NOT EXISTS audit_log (
    id SERIAL PRIMARY KEY,
    actor TEXT NOT NULL,
    action TEXT NOT NULL,
    target TEXT,
    detail TEXT,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS audit_log_created_idx ON audit_log(created DESC);

CREATE TABLE IF NOT EXISTS email_log (
    id SERIAL PRIMARY KEY,
    email TEXT NOT NULL,
    ip TEXT,
    kind TEXT NOT NULL,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS email_log_email_idx ON email_log(email, created DESC);

-- Tool calls, tool results, reasoning and attachments ride along as JSON.
ALTER TABLE thread_message ADD COLUMN IF NOT EXISTS data JSONB;

-- Long-lived bearer credentials for API clients and the in-chat shell. Only
-- the hash is stored; the prefix is for display.
CREATE TABLE IF NOT EXISTS api_key (
    id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    prefix TEXT NOT NULL,
    key_hash TEXT NOT NULL UNIQUE,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS api_key_user_idx ON api_key(user_id);

-- Account security: passkeys (WebAuthn credentials) and TOTP two-factor.
CREATE TABLE IF NOT EXISTS passkey (
    id SERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    credential_id BYTEA NOT NULL UNIQUE,
    public_key BYTEA NOT NULL,
    aaguid BYTEA,
    sign_count BIGINT NOT NULL DEFAULT 0,
    transports TEXT[] NOT NULL DEFAULT '{}',
    attestation_type TEXT NOT NULL DEFAULT '',
    backup_eligible BOOLEAN NOT NULL DEFAULT FALSE,
    name TEXT NOT NULL DEFAULT 'Passkey',
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_used TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS passkey_user_idx ON passkey(user_id);

-- One row per in-flight WebAuthn ceremony; consumed on finish, swept on expiry.
CREATE TABLE IF NOT EXISTS webauthn_challenge (
    id TEXT PRIMARY KEY,
    user_id INT REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL,
    challenge BYTEA NOT NULL,
    session_data JSONB NOT NULL,
    expires TIMESTAMPTZ NOT NULL
);

ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_secret TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_enabled BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_backup_codes TEXT[] NOT NULL DEFAULT '{}';
ALTER TABLE users ADD COLUMN IF NOT EXISTS totp_pending_secret TEXT;

-- Billing. users.plan / plan_until is the entitlement the relay reads; a plan is
-- active while plan_until is in the future. The sandbox Stripe customer is kept
-- apart from the live one.
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan_until TIMESTAMPTZ;
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan_stripe_customer_id TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS plan_stripe_sandbox_customer_id TEXT;
CREATE INDEX IF NOT EXISTS users_plan_stripe_customer_idx ON users (plan_stripe_customer_id) WHERE plan_stripe_customer_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS users_plan_stripe_sandbox_customer_idx ON users (plan_stripe_sandbox_customer_id) WHERE plan_stripe_sandbox_customer_id IS NOT NULL;

-- Written by the Stripe webhook on invoice.paid (source='stripe') and by admin
-- grants (source='grant'). Revenue reports read source='stripe'.
CREATE TABLE IF NOT EXISTS payment (
    invoice_id TEXT PRIMARY KEY,
    subscription_id TEXT,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    tier TEXT NOT NULL,
    period_start TIMESTAMPTZ,
    period_end TIMESTAMPTZ,
    stripe_renew BOOLEAN NOT NULL DEFAULT TRUE,
    amount_cents INT,
    currency TEXT NOT NULL DEFAULT 'usd',
    source TEXT NOT NULL DEFAULT 'stripe',
    actor TEXT,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS payment_user_idx ON payment(user_id);

-- Each Stripe event id is recorded once, so webhook retries are no-ops.
CREATE TABLE IF NOT EXISTS billing_event (
    stripe_event_id TEXT PRIMARY KEY,
    received TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- Usage windows: fixed, not sliding. A window starts at its first recorded use
-- and resets as a whole at window_start + length.
CREATE TABLE IF NOT EXISTS usage_window (
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('five_hour', 'weekly')),
    window_start TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    used_units NUMERIC NOT NULL DEFAULT 0,
    PRIMARY KEY (user_id, kind)
);

-- One row per metered request, the audit trail for the window totals.
CREATE TABLE IF NOT EXISTS usage_event (
    id BIGSERIAL PRIMARY KEY,
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    model TEXT NOT NULL,
    multiplier NUMERIC NOT NULL,
    units NUMERIC NOT NULL,
    input_tokens INT NOT NULL DEFAULT 0,
    cached_tokens INT NOT NULL DEFAULT 0,
    output_tokens INT NOT NULL DEFAULT 0,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS usage_event_user_idx ON usage_event (user_id, created DESC);

-- What the site shows and changes for a plan: the Stripe subscription behind it,
-- its interval, a pending cancel and a pending downgrade (a Subscription
-- Schedule). Stripe is the source; the billing routes and the webhook refresh it.
CREATE TABLE IF NOT EXISTS estro_subscription (
    user_id INT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    mode TEXT NOT NULL CHECK (mode IN ('live', 'sandbox')),
    subscription_id TEXT NOT NULL,
    plan TEXT NOT NULL,
    billing_interval TEXT NOT NULL CHECK (billing_interval IN ('monthly', 'annual')),
    status TEXT NOT NULL,
    period_end TIMESTAMPTZ NOT NULL,
    cancel_at TIMESTAMPTZ,
    schedule_id TEXT,
    next_plan TEXT,
    next_billing_interval TEXT,
    next_at TIMESTAMPTZ,
    updated TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS estro_subscription_sub_idx ON estro_subscription (subscription_id);

-- Responses API chain: the items of every relayed /v1/responses reply, so a
-- request naming previous_response_id can be sent with its history after a
-- restart or from another process. Rows expire after RESPONSES_CHAIN_TTL.
CREATE TABLE IF NOT EXISTS response_chain (
    user_id INT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    response_id TEXT NOT NULL,
    items JSONB NOT NULL,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, response_id)
);
CREATE INDEX IF NOT EXISTS response_chain_created_idx ON response_chain (created);

-- Provisioned chat.z.ai accounts for Kali Epsilon's browser workers: one per
-- worker so z.ai's per-account concurrency cap never turns into a 429. The
-- pool filler (internal/zai/accounts.go) adds rows through ext/zai_signup.py;
-- a 429 sets cooldown_until, a rejected token sets status 'dead'.
CREATE TABLE IF NOT EXISTS zai_account (
    id SERIAL PRIMARY KEY,
    email TEXT NOT NULL UNIQUE,
    password TEXT NOT NULL,
    token TEXT NOT NULL,
    user_id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    mail_provider TEXT NOT NULL DEFAULT '',
    mail_password TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'dead')),
    strikes INT NOT NULL DEFAULT 0,
    cooldown_until TIMESTAMPTZ,
    last_used TIMESTAMPTZ,
    created TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
