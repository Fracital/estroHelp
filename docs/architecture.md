# Architecture

One Go process does everything: it serves the built Astro site, exposes the API, holds the PostgreSQL pool, relays chat requests to the providers, and runs the Wisp relay for the browser workspace. There is no job queue and no realtime channel; the browser drives everything with plain HTTP.

```
browser ──HTTP/WS──> elichat (Go, :3000) ──> PostgreSQL
                         │
                         ├── provider "cliproxy": HTTP relay (Kali Gamma, Kali Beta)
                         └── provider "zai": browser pool (Kali Epsilon)
```

## Serving the site

`internal/server` builds the mux. API routes are registered first; everything else falls through to a static handler over `STATIC_DIR` (`frontend/dist`). The static handler resolves clean URLs (`/chat` to `chat.html` or `chat/index.html`), caches `/_astro/` assets for a year, serves `404.html` for unknown paths, and sends `Cross-Origin-Opener-Policy: same-origin` and `Cross-Origin-Embedder-Policy: credentialless` on every response. Those two headers make the page cross-origin isolated, which the workspace runtime needs for `SharedArrayBuffer`.

Middleware order, outermost first: request logging, authentication, rate limiting, mux.

## Authentication

Accounts live in `users`. Passwords are hashed with argon2id. There are two credential types:

- **Sessions.** `POST /api/auth/login` returns a random 256-bit token and also sets it as the `kali_session` cookie (HttpOnly, SameSite=Lax, Secure when the request came over HTTPS or carries `X-Forwarded-Proto: https`). Only the SHA-256 of the token is stored. Sessions last 30 days and record IP and user agent; users can list and revoke them.
- **API keys.** `estro_` prefixed, created from the account page or `POST /api/account/api-keys`. The secret is returned once; only its hash and an 8-character display prefix are kept. Up to 20 per account. Any request may authenticate with `Authorization: Bearer <key>` or `X-Api-Key`.

The `Authenticate` middleware accepts a bearer token or API key first, then the cookie. A cookie-authenticated request that is not GET or HEAD must carry `Content-Type: application/json`; a cross-site form post cannot do that without a CORS preflight, which is the CSRF guard. Handlers then use `RequireUser` and `RequireAdmin`.

Email verification: a six-digit code, hashed, valid for 15 minutes, with per-address and per-IP send limits recorded in `email_log`. When `REQUIRE_VERIFIED_EMAIL` is on, chat endpoints refuse unverified accounts (admins exempt). Password reset works the same way and revokes all sessions on success.

### Passkeys and two-factor

Passkeys are WebAuthn credentials stored in the `passkey` table (credential id, public key, AAGUID, sign counter, transports, name, last use). Registration and sign-in are two-step ceremonies: `begin` stores a challenge row in `webauthn_challenge` with a five minute expiry, `finish` deletes that row in the same query that reads it, so a challenge is single use. Sign-in is discoverable: the browser presents a resident credential, the server looks up its owner by credential id and checks the user handle, then opens a normal session. The sign counter is updated on every use and an assertion whose counter went backwards is refused as a possible clone. The relying party id and origin come from `PUBLIC_HOST` when it is set and from the request host otherwise, so a dev server on `localhost` works without configuration.

Two-factor uses TOTP (RFC 6238, SHA1, six digits, 30 second steps, one step of skew). Enrolment keeps the secret in `totp_pending_secret` until the user proves a code, then moves it to `totp_secret`, sets `totp_enabled` and stores ten backup codes as hashes. A password login on an account with two-factor on answers `{"mfa":"totp","mfaToken":"..."}` instead of a session; the token is an HMAC-signed `user.expiry.nonce` value good for five minutes and at most five code attempts, after which the user starts over. A passkey sign-in is already a strong factor and skips the code. Enabling two-factor signs out the account's other sessions.

## The `/v1` request path

Three dialects share one path: `/v1/chat/completions` (OpenAI chat), `/v1/messages` (Anthropic messages) and `/v1/responses` (OpenAI Responses). Each handler:

1. Reads the body (24 MB limit) and keeps every field it does not understand, so sampling parameters, `tools`, `tool_choice` and content-part arrays pass through untouched.
2. Resolves `model` to a persona in `internal/persona`. A persona is a public Kali id, a display name, a provider name, an upstream model id, a system prompt and a priming exchange, loaded from embedded JSON at start.
3. Runs the admission checks: ban, email verification, and the per-account daily request cap (`ai_usage`, one row per user, day and model; admins and sandbox users are exempt).
4. Applies the persona in the shape of the dialect: the system prompt goes first (prepended to any client system text), the primer exchange leads the messages or input, and `model` is rewritten to the upstream id.
5. Records usage, then hands the body to the provider for that persona.
6. Wraps the response writer in a rewriter that replaces every `"model":"..."` in the output, SSE or JSON, with the public Kali id, so clients never see the upstream model name.

Providers implement one method, `Serve(w, r, dialect, body)`. The HTTP relay provider forwards to the upstream path for the dialect, adds its credential, and streams the response bytes through, flushing per chunk. The browser provider in `internal/zai` runs a pool of headed Chromium workers (see its package comment) and renders the three dialects itself.

## The agent loop

Tool use is browser driven. The chat app sends `tools` with the request, accumulates streamed `tool_calls`, runs each call in the workspace, stores the assistant turn and the `tool` results through the threads API, and calls the model again with the full history. The loop stops when a turn ends without tool calls or after 25 rounds; Stop aborts both the stream and the running tool. The server stays a stateless relay.

Threads are `thread` and `thread_message` rows. `thread_message.data` is JSONB and carries whatever a message needs beyond text: `reasoning` and `reasoningMs`, `toolCalls`, `toolCallId`/`toolName`/`exitCode` for `tool` rows, and `attachments` (images as data URLs, text files inline). Roles are `user`, `assistant` and `tool`.

## The workspace

The workspace is a Node-compatible runtime that runs inside the tab. Its file system is OPFS, so files persist per browser profile. The Files and Shell panes and the seven tools (`run_shell`, `read_file`, `write_file`, `list_dir`, `delete_path`, `search_files`, `find_files`) are implemented in `frontend/src/components/workspace`; paths resolve from `/home/user/workspace` and are confined to `/home/user`. Shell output streams into the tool card; tool output is clipped at 16 KB before it goes back to the model.

Networking from inside the sandbox goes over Wisp, a small TCP-over-WebSocket protocol, to `internal/wisp` mounted at `/wisp`. The relay implements Wisp v1 (CONNECT, DATA, CONTINUE, CLOSE) for TCP and UDP, caps streams per connection at 64 with a 128-packet credit window, checks that the WebSocket origin is the same host, and refuses loopback, link-local and private destinations. The one exception is the application's own address: the listen address, `localhost`, `127.0.0.1` and `PUBLIC_HOST` are allowed so the in-chat shell can call Estro's own API.

## Rate limits

`internal/server/limits.go` applies token buckets after authentication:

| Tier | Routes | Key | Default |
|---|---|---|---|
| auth | `POST /api/auth/*` | client IP | 10 per minute, burst 10 |
| llm | `POST /v1/*` | account (or IP when anonymous) | 120 per minute, burst 120 |
| app | everything else under `/api/` | account (or IP) | 300 per minute, burst 300 |

Refusals are `429` with `Retry-After`; `/v1` returns the OpenAI error shape. Buckets are per process. The daily cap is separate and is the real budget for model use.

## Mail

`internal/auth/email.go` speaks SMTP from the standard library: implicit TLS when `SMTP_SECURE` is on or the port is 465, otherwise STARTTLS when offered. The mailer is only enabled when both a host and a password are set; otherwise messages are logged, so a half-configured server never blocks registration.

See also: [API](api.md), [Configuration](configuration.md), [Frontend](frontend.md).
