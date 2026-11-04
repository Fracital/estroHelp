# API

Estro speaks the OpenAI chat-completions dialect and also accepts Anthropic-style messages and the OpenAI Responses shape. Point any client at `https://estro.help/v1` with a key and it works. This page lists the supported surface; other `/api` routes exist for the web app and are not a stable contract.

## Authentication

Two credentials, same headers:

- An API key from the account page or the calls below. Keys start with `estro_` and do not expire until revoked.
- A session token from `POST /api/auth/login`, valid for 30 days.

Send either as `Authorization: Bearer <token>` or `X-Api-Key: <token>`.

```sh
curl https://estro.help/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"you","password":"yourpassword"}'
# {"token":"...","user":{...},"requireVerifiedEmail":true}
```

Keys:

| Method | Path | Description |
|---|---|---|
| GET | `/api/account/api-keys` | List keys: id, name, prefix, created, lastUsed |
| POST | `/api/account/api-keys` | Body `{"name":"..."}`; returns `{"key":{...},"secret":"estro_..."}`; the secret is shown once |
| DELETE | `/api/account/api-keys/{id}` | Revoke |

Usage: `GET /api/usage` returns `{"requestsToday":n,"dailyLimit":n}`; `dailyLimit` is 0 when the account has no cap.

### Passkeys and two-factor sign-in

`POST /api/auth/login` returns a session as described above unless the account has two-factor on, in which case the body is `{"mfa":"totp","mfaToken":"..."}` and no session exists yet. Send the code to finish:

```
POST /api/auth/login/totp   {"mfaToken":"...","code":"123456"}
```

`code` is a six digit authenticator code or an unused backup code. The token lives five minutes and allows five attempts.

Passkey sign-in needs no username. Fetch options, run the browser ceremony (`navigator.credentials.get`), and post the assertion:

```
POST /api/auth/passkey/begin    {}                      -> {"challengeId":"...","options":{...}}
POST /api/auth/passkey/finish   {"challengeId":"...","credential":{...}}
```

`finish` answers exactly like `login`. Passkey management while signed in:

| Method | Path | Description |
|---|---|---|
| `GET` | `/api/account/passkeys` | List passkeys (name, created, last used) |
| `POST` | `/api/account/passkeys/register/begin` | Creation options for `navigator.credentials.create` |
| `POST` | `/api/account/passkeys/register/finish` | `{challengeId, name, credential}`; saves the passkey |
| `PATCH` | `/api/account/passkeys/:id` | `{name}` |
| `DELETE` | `/api/account/passkeys/:id` | Remove |

Two-factor management while signed in:

| Method | Path | Description |
|---|---|---|
| `POST` | `/api/account/totp/begin` | Returns `secret`, an `otpauth://` URL and a PNG data URL for the QR code |
| `POST` | `/api/account/totp/enable` | `{code}`; turns it on and returns `backupCodes` once |
| `POST` | `/api/account/totp/disable` | `{code}`; a current code or a backup code |
| `POST` | `/api/account/totp/backup-codes` | `{code}`; replaces the backup codes |

`GET /api/auth/me` reports `totpEnabled` and `passkeyCount` on the user. All of these share the auth rate limit tier.

## Models

`GET /v1/models` (no auth) returns the OpenAI list shape with `name` and `description` added:

| id | Name | Best for |
|---|---|---|
| `kali-gamma` | Kali Gamma | Deep reasoning, multi-step work, large codebases |
| `kali-beta` | Kali Beta | Everyday coding and writing, long context |
| `kali-epsilon` | Kali Epsilon | Fast iteration |

Model names are case-insensitive and `Kali Gamma` is accepted for `kali-gamma`.

## Chat completions

`POST /v1/chat/completions`. Standard request fields: `model`, `messages`, `stream`, `temperature`, `max_tokens`, `top_p`, `stop`, `tools`, `tool_choice`. Message content may be a string or an array of parts; image parts use `{"type":"image_url","image_url":{"url":"data:..."}}`.

```sh
curl -N https://estro.help/v1/chat/completions \
  -H "Authorization: Bearer $ESTRO_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"model":"kali-gamma","stream":true,
       "messages":[{"role":"user","content":"Refactor this function and explain the tradeoffs."}]}'
```

Non-streaming responses are `chat.completion` objects. Streaming responses are server-sent events, one `data:` line per `chat.completion.chunk`, ending with `data: [DONE]`. Chunks for models that think before answering carry `delta.reasoning_content` before any `delta.content`. The non-streaming message carries the same text as `message.reasoning_content`. The `model` field in every response is the Kali id you asked for.

Tool calling uses the standard function-tool schema. Streamed calls arrive as `delta.tool_calls` entries: the first fragment carries `id`, `type` and `function.name`, later fragments append to `function.arguments`, and the turn ends with `finish_reason: "tool_calls"`. Answer with `tool` role messages that reference `tool_call_id`.

## Anthropic messages

`POST /v1/messages` with the Anthropic request shape: `model`, `max_tokens`, `system` (string or content blocks), `messages`, `stream`, `tools`. Send `anthropic-version` if your client does; `2023-06-01` is assumed otherwise. Thinking appears as `thinking` content blocks. Clients such as Claude Code work with `ANTHROPIC_BASE_URL=https://estro.help` and `ANTHROPIC_AUTH_TOKEN` set to a key.

## Responses

`POST /v1/responses` with `model`, `input` (string or item list), `instructions`, `stream`. Reasoning arrives as `reasoning` output items before the `message` item.

## Errors

Error bodies follow the OpenAI shape, `{"error":{"message":"...","type":"...","code":"..."}}` (Anthropic shape on `/v1/messages`).

| Status | Code | Meaning |
|---|---|---|
| 400 | `bad_request` | Malformed JSON, empty `messages` or `input` |
| 401 | none | Missing, expired or revoked credential |
| 403 | `email_unverified` | Verify the account's email first |
| 403 | `banned` | Account suspended |
| 404 | `model_not_found` | Unknown model id |
| 429 | `rate_limit_exceeded` | Per-minute limit or daily cap reached; see `Retry-After` |
| 502 | none | The model provider is unavailable |
| 503 | `model_unavailable` | That model is disabled on this deployment |

## Rate limits

Model endpoints allow 120 requests a minute per account with a burst of 120, enough for agent loops that fire calls in sequence. Sign-in and password endpoints allow 10 a minute per IP. Over the limit you get `429` with `Retry-After`. The daily request cap is separate, shown by `GET /api/usage`, and resets at midnight UTC. Operators can change all of these; see [Configuration](configuration.md).

## IDE and tool setup

The docs page of the running site has ready-made configuration for editors and CLI tools (opencode, Continue, VS Code, Cursor, Windsurf, Cline, Roo, Kilo, Claude Code, Codex, Aider, Zed, JetBrains, Neovim, LibreChat, Open WebUI and others). Each one is the same idea: base URL `https://estro.help/v1` (or the bare host for Anthropic-style clients), a key, and one of the three model ids.
