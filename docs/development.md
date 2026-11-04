# Development

## Running

```sh
make db-start                       # local PostgreSQL in .pgdata/
make backend                        # go run ./cmd/elichat on :3000
cd frontend && npm run dev          # astro dev on :4321 with /api, /v1 and /wisp proxied to :3000
```

The Astro dev server sends the same COOP/COEP headers as the Go server, so the workspace works in dev. For the production path, `npm run build` then run the binary and open :3000.

Registration in dev: with no SMTP password set, the verification code is printed in the server log. Chat is gated on verification unless `REQUIRE_VERIFIED_EMAIL=false`.

## Tests and checks

| What | Command |
|---|---|
| Go vet and tests (relay, provider parsing, pool) | `make test` or `go vet ./... && go test ./...` |
| Frontend types | `cd frontend && npx astro check` |
| Agent loop unit tests | `cd frontend && node --test tests/agent.test.mjs` |
| Build | `cd frontend && npm run build` |

`astro check` needs TypeScript 6, which is pinned in `frontend/package.json`; TypeScript 7 is not supported by the checker. The Go provider tests include pool tests that spawn fake worker scripts and take about a minute.

## Browser verification

UI changes are checked against the real app with a headless Chromium driven by `patchright` (a Playwright fork, installed in the user site-packages, so run scripts with plain `python3`, not `python3 -I`). Launch with `--use-angle=swiftshader --enable-unsafe-swiftshader`; without those flags WebGL is unavailable and the animated backgrounds render nothing. Log in as a dev user, drive the page, and assert on DOM state rather than screenshots where possible. Document Picture-in-Picture is not available headless, so popouts are tested through the popup fallback.

## Gotchas

- **Shell variables.** The workspace shell expands `$VAR` for `echo`, but its `curl` then fails with a misleading TLS error. Anything typed into the shell on the user's behalf must use literal values; `lib/run.ts` substitutes the origin and key for that reason.
- **Workspace curl flags.** The sandbox curl rejects `-w`, `-N`, `-sS` and `-I`. `toShellLine` strips the ones that appear in docs samples.
- **Relay deny list.** The Wisp relay refuses private and loopback destinations except the application's own address. If the shell cannot reach your dev server, check `PUBLIC_HOST` and the listen address.
- **Stream hold.** The Epsilon worker buffers the first `ZAI_STREAM_HOLD` characters before streaming. The upstream default hides streaming on short replies; the example `.env` sets 160.
- **Persisted islands.** The rail is a persisted island across client-side navigations. It derives its active item from the URL on each swap; do not rely on props changing.
- **Model names.** User-facing code and docs name only Kali Gamma, Beta and Epsilon. The response rewriter replaces upstream model ids in every `/v1` response; keep it in place when adding dialects.
- **Postgres port.** The example database runs on 5433 to avoid clashing with a system PostgreSQL on 5432.
- **Secrets.** `.env`, `cliproxyapi.txt` and `zai_config.json` are git-ignored. Generate passwords; do not reuse the placeholder in `.env.example`.

## Conventions

Plain language in copy, no em dashes. Go code is `gofmt` clean and vetted. Frontend components are Preact; React-written backgrounds work through `preact/compat`. New CSS goes next to the feature's stylesheet in `src/styles`; the ported chat stylesheet `ai.css` is kept close to its source so later diffs against it stay readable.

See also: [Frontend](frontend.md), [Architecture](architecture.md).
