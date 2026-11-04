# Deployment

A production install is the Go binary, the built site next to it, PostgreSQL, and for Kali Epsilon a machine that can run headed Chromium under Xvfb.

## Checklist

1. **Build.** `make build` runs `npm install`, generates `frontend/public/dusk` (the workspace runtime assets, copied from the npm package by `frontend/scripts/dusk-assets.mjs`), builds the site into `frontend/dist`, and compiles `bin/elichat`. Ship `bin/elichat`, `frontend/dist`, `ext/`, and the persona files are embedded in the binary already.
2. **PostgreSQL.** Create a database and a dedicated role with a generated password; put it in `DATABASE_URL`. The schema is applied at start and is idempotent. Back up with `pg_dump` on a schedule; the only state outside the database is the workspace file system, which lives in each user's browser (OPFS), and `zai_config.json`.
3. **Environment.** Start from `.env.example`. Set `HOST=0.0.0.0` only if the proxy is on another machine; otherwise keep loopback. Set `PUBLIC_HOST` to the public host so the in-chat shell can reach the API through the relay. Set `ADMIN_USERNAME` for the first admin and remove it afterwards if you like. See [Configuration](configuration.md).
4. **Reverse proxy.** Terminate TLS in front and forward `X-Forwarded-Proto: https` so session cookies are marked Secure, and `X-Forwarded-For` or `CF-Connecting-IP` so rate limits and session records see real client addresses. Proxy `/wisp` as a WebSocket. The application already sends `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: credentialless`; do not strip them, the workspace depends on cross-origin isolation. Do not add a conflicting COEP.
5. **Mail.** Set the `SMTP_*` variables with a real password; until both host and password are present, verification codes only go to the log and sign-ups cannot complete without an admin marking the address verified.
6. **Epsilon prerequisites** (skip with `ZAI_DISABLED=true`): Python 3 with the `patchright` package, a Chromium build it can launch, `xvfb-run`, and `ext/zai_browser.py` reachable from the working directory (or `ZAI_BROWSER_SCRIPT`). Each pool worker is a browser process; start with `ZAI_BROWSER_POOL_SIZE=2` and watch memory. Put a signed-in token in `zai_config.json` for the better models, or leave it out for guest access.
7. **Process management.** Run the binary from the repository directory (it reads `.env`, `STATIC_DIR` and `ext/` relative to the working directory) under systemd or similar with restart on failure. It shuts down cleanly on SIGTERM.
8. **Rate limits.** The defaults suit a small deployment. Buckets are per process, so if you run replicas the effective limits multiply.

## Example systemd unit

```ini
[Unit]
Description=Estro
After=network-online.target postgresql.service

[Service]
WorkingDirectory=/srv/estro
ExecStart=/srv/estro/bin/elichat
Restart=on-failure
Environment=HOME=/srv/estro

[Install]
WantedBy=multi-user.target
```

## Updating

Rebuild (`make build`), replace the binary and `frontend/dist`, restart. Schema changes are additive and applied on start.

See also: [Architecture](architecture.md).

## Continuous deployment

`.github/workflows/ci.yml` runs `go vet`, `go test`, `astro check` and a build on every push and pull request. Pushes to `master` that pass then run a deploy job on a self-hosted runner that lives on the production host (label `chat`). The runner builds the checkout there and calls `sudo /usr/local/sbin/estro-deploy`, a small root gate that copies the binary, `frontend/dist` and `ext/` into `/opt/elichat`, installs the unit and nginx site from `deploy/`, restarts the service and checks `/api/health`. The host needs no inbound access and the workflow needs no secrets; the runner only makes outbound calls to GitHub.

Install the runner once with `scripts/runner-install.sh <registration-token>` as root on the host (the token comes from the repository's Actions settings). `scripts/deploy.sh` remains for a manual push from a workstation.
