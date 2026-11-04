# Estro

Estro is a chat workspace for coding with three models, Kali Gamma, Kali Beta and Kali Epsilon. It runs as a single Go binary that serves a static Astro site, talks to PostgreSQL, and relays chat requests to the model providers. Besides the chat UI it offers an OpenAI-compatible API (chat completions, Anthropic-style messages and the Responses dialect), an in-browser workspace with a shell and a file system that the model can drive with tools, accounts with email verification, API keys, an admin panel, and a theme system.

Public host: `estro.help`.

## Screenshots

_Coming soon._

## Quick start

Requirements: Go 1.26, Node 24, PostgreSQL 16 or newer (`initdb`, `pg_ctl`, `psql` on PATH). Kali Epsilon additionally needs Python 3, `xvfb-run`, a Chromium that `patchright` can drive, and the `patchright` Python package; see [docs/deployment.md](docs/deployment.md).

```sh
cp .env.example .env         # fill in the provider key and a strong DATABASE_URL password
make db-start                # local cluster in .pgdata/ plus the elichat database
make build                   # npm install + astro build, then the Go binary
./bin/elichat                # http://127.0.0.1:3000
```

`.env` is read from the working directory at start. The database schema is applied on every start (idempotent), so there is no separate migration step.

### First admin

Register through the UI, then set `ADMIN_USERNAME=<that username>` in `.env` and restart. The flag is granted once on start; after that the user can promote others from the admin panel.

Without SMTP configured, verification and reset codes are written to the server log (`verification code is NNNNNN`), which is how you complete sign-up in a dev setup.

## Development

Two terminals:

```sh
make backend                 # go run ./cmd/elichat on :3000
cd frontend && npm run dev   # astro dev on :4321, proxies /api, /v1 and /wisp to :3000
```

The dev server sends the same cross-origin isolation headers as the Go server, so the workspace runs in dev too. `npm run build` writes `frontend/dist`, which the Go binary serves from `STATIC_DIR`.

Checks: `make test` (Go vet and tests), `cd frontend && npx astro check` (types) and `node --test tests/agent.test.mjs` (agent loop). See [docs/development.md](docs/development.md).

## Layout

```
cmd/elichat/          entry point: env, database, providers, HTTP server
internal/api/         /v1 dialects, threads, models, usage
internal/auth/        accounts, sessions, API keys, admin, mail
internal/db/          pool and schema.sql
internal/persona/     the Kali models: system prompts and upstream mapping
internal/provider/    the two upstream providers
internal/ratelimit/   token buckets
internal/server/      mux, static files, rate limit tiers, Wisp mount
internal/wisp/        Wisp relay for the browser workspace
internal/zai/         browser-based provider behind Kali Epsilon
ext/                  browser worker script and captcha extension for that provider
frontend/             Astro + Preact site (chat, docs, account, admin, settings)
docs/                 this documentation
```

## Documentation

- [Architecture](docs/architecture.md): how a request moves through the system.
- [API](docs/api.md): what clients can call.
- [Configuration](docs/configuration.md): every environment variable.
- [Deployment](docs/deployment.md): production checklist.
- [Frontend](docs/frontend.md): the Astro site and the chat app.
- [Admin](docs/admin.md): roles, bans, keys and caps.
- [Development](docs/development.md): tests, checks and gotchas.
