# Administration

## Roles

- **Admin.** Can open `/admin`, is exempt from the daily cap and from email verification, and cannot ban or demote itself. The first admin is created with `ADMIN_USERNAME`; after that admins promote others from the panel.
- **Sandbox.** A flag for test or internal accounts. Sandbox users are exempt from the daily cap and their usage rows are marked, so they can be excluded from reporting.

## The admin panel

`/admin` is reachable from the Administration card on the account page. Three tabs:

- **Overview.** Counts of users, verified users, sandbox and banned accounts, requests today and over seven days, active users today, threads; usage by day and model for the last 14 days.
- **Users.** Search by username or email, paged. Each row shows email verification, flags, requests today, thread count and join date, with a Manage action: ban (reason and optional duration in hours), lift ban, toggle sandbox, toggle admin, mark email verified, sign out everywhere, delete.
- **Audit.** Every admin action and the security-relevant account events (register, password reset, delete, key create and revoke) with actor, action, target, detail and time.

## Bans

A ban records reason, actor and optional expiry, revokes all of the user's sessions immediately, and refuses sign-in and every chat request while active. Lifting a ban keeps the record. Bans are per user, not per IP.

## Daily caps

`DAILY_REQUEST_LIMIT` is the number of chat requests an account may make per UTC day, counted across `/v1/chat/completions`, `/v1/messages` and `/v1/responses` and across models. The account page shows the running count. Admins and sandbox users have no cap. Per-minute rate limits are a separate, operator-level control; see [Configuration](configuration.md).

## API keys

Users manage their own keys on the account page (name, prefix, created, last used; create returns the secret once; revoke). Admins do not see or manage other users' keys; deleting a user removes them. The in-chat shell creates and reuses a key named `Shell` on the user's behalf. Keys authenticate exactly like a session and are logged in the audit trail when created or revoked.

## Account security

Every account can add passkeys and turn on two-factor authentication from the Security section of the account page.

- Passkeys use the browser's WebAuthn support: a fingerprint, face, device PIN or a hardware key. A passkey signs the user in without a password and without a two-factor code. Users can keep up to ten, rename them and remove them. A passkey whose counter goes backwards is treated as cloned and refused; the event is written to the audit log.
- Two-factor adds an authenticator app code after the password. Turning it on shows ten single-use backup codes exactly once; the user can regenerate them with a current code. Turning it on also signs out the account's other sessions.
- The audit log records `passkey_add`, `passkey_remove`, `passkey_clone_warning`, `totp_enable`, `totp_disable` and `totp_backup_regenerate`.

Admins have no button to reset a user's two-factor. If someone has lost both the authenticator and the backup codes, confirm their identity out of band and clear it in the database: `UPDATE users SET totp_enabled=false, totp_secret=NULL, totp_backup_codes='{}' WHERE username='...'`. "Sign out everywhere" in the Manage modal still works as usual.

Passkeys need the relying party id to match the site's host. Set `PUBLIC_HOST` in production (see configuration); on a dev server the request host is used.

## Email

With SMTP configured, registration, address changes and password resets send six-digit codes that expire after 15 minutes, with sending limited to 5 per address and 20 per IP per hour. Without SMTP, codes appear in the server log and an admin can mark an address verified from the Users tab.

See also: [Architecture](architecture.md).
