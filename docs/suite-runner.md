# Suite Runner Account

Live persona suites (`make test-live`) need a real account + API key on the
running server. Doing this interactively is a gauntlet of 401s and 402s, so a
dedicated account exists: **`suiterunner`**. This doc is how it was made and
how to remake it on any fresh deployment.

## TL;DR

```sh
# 1. Register (username: 3-24 chars, letters/numbers/underscore ONLY — no hyphens)
curl -s http://localhost:3000/api/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"suiterunner","email":"suite@localhost.dev","password":"correct-horse-battery-staple-9"}'

# 2. Verify email + grant admin directly in the DB.
#    REQUIRE_VERIFIED_EMAIL=true gates chat, and SMTP on a dev box goes nowhere
#    you can read. Admin is what exempts the account from the 402 plan gate —
#    the `sandbox` flag does NOT exempt API bearer-key calls.
psql "$DATABASE_URL" -c \
  "UPDATE users SET email_verified=TRUE, admin=TRUE WHERE username='suiterunner';"

# 3. Log in, mint a key. The secret is shown ONCE.
TOKEN=$(curl -s http://localhost:3000/api/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"suiterunner","password":"correct-horse-battery-staple-9"}' \
  | python3 -c "import sys,json;print(json.load(sys.stdin)['token'])")

curl -s http://localhost:3000/api/account/api-keys \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"suite-runner"}'
# -> {"key":{...},"secret":"estro_..."}   save the secret NOW

# 4. Persist it for future runs
echo "ELICHAT_API_KEY=estro_..." >> .env

# 5. Run
ELICHAT_LIVE=1 ELICHAT_API_KEY=estro_... make test-live
# or with rubric judging:
ELICHAT_LIVE=1 ELICHAT_API_KEY=estro_... ELICHAT_JUDGE=kali-gamma make test-live
```

On this machine the key is already in `.env` as `ELICHAT_API_KEY`, so step 5
can source it: `set -a; . ./.env; set +a; make test-live`.

## Error map (learned the hard way)

| Symptom | Cause | Fix |
|---|---|---|
| `401 missing API key` | `ELICHAT_API_KEY` unset | export it (step 4/5) |
| `402 ... requires an active Estro plan` | billing gate on bearer-key API calls | `admin=TRUE` on the account — **admins are exempt from all of this** (`internal/api/metering.go`, docs/billing.md) |
| sandbox flag didn't help | sandbox only changes Stripe/checkout behavior + rate-limit exemption, NOT the plan gate | use admin |
| verification code never arrives | dev SMTP (`mail2.caan.edu`) isn't a mailbox you control; codes go to real email or the void | skip email entirely, `UPDATE users SET email_verified=TRUE` |
| `username must be 3-24 characters` | hyphens rejected | `suiterunner`, not `suite-runner` |

## Notes

- The account is a test fixture. Don't give it a plan, don't run billing flows
  through it, don't promote it beyond admin. If it gets deleted, rerun the
  TL;DR.
- Keys don't expire until revoked (`internal/auth/apikeys.go`); revoke via
  `DELETE /api/account/api-keys/{id}` if one leaks.
- Password above is a throwaway for local/dev. Use a real one anywhere the
  deployment is reachable by humans.
