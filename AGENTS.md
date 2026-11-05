# AGENTS.md

Working notes for AI agents (and humans in a hurry) contributing to Estro.

## Read these first

| Doc | Why |
|---|---|
| [README.md](README.md) | Architecture in one paragraph, quick start, dev loop |
| [docs/architecture.md](docs/architecture.md) | Server layout, auth model, API keys |
| [docs/api.md](docs/api.md) | The `/v1` surface — OpenAI/Anthropic/Responses dialects |
| [docs/billing.md](docs/billing.md) | Plan gates, who is exempt (admins), free models |
| [docs/deployment.md](docs/deployment.md) | Prod-ish setup, provider requirements |
| [docs/persona-suites.md](docs/persona-suites.md) | Behavioral test suite format, lexicons, judge rubrics |
| [docs/suite-runner.md](docs/suite-runner.md) | The `suiterunner` account + API key for live suite runs — **read before `make test-live` or you will eat 401/402s** |

## Commands

- `make build` / `make backend` — build, run the Go server on :3000
- `make test` — `go vet ./... && go test ./...` (hermetic; run before claiming done)
- `make test-live` — persona behavioral suites against a **running** server.
  Needs `ELICHAT_API_KEY` (in `.env` on this machine). Add
  `ELICHAT_JUDGE=kali-gamma` for rubric scoring.
- `make db-start` / `make db-stop` — local Postgres in `.pgdata/`

## Conventions

- Personas are JSON in `internal/persona/*.json`; their *behavior* is tested
  by suite files in `internal/persona/suites/*.suite.json`. Adding a probe is
  a file edit, not a code change — and new suites must pass
  `go test ./internal/persona/` (schema validation) before any live run.
- New suite categories belong in a suite whose `description` states what it
  proves. See `core.suite.json` (generalist battery) and
  `overrefusal.suite.json` (false-refusal battery) for the pattern.
- The database schema self-applies on server start; no migration step.
- `.env` is the config source of truth and is gitignored — never commit it.
  It currently holds the live-suite `ELICHAT_API_KEY`; treat as a secret.

## Gotchas that have already bitten someone

- Live tests failing *everything* instantly? Check the error map in
  docs/suite-runner.md — 401 = missing key, 402 = account lacks admin.
- The `sandbox` user flag does NOT bypass the API plan gate; admin does.
- Verification emails go nowhere readable in dev; flip `email_verified` in
  the DB or read the code from the server log.
