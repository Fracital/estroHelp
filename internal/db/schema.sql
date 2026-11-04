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
