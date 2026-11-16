# Security

## Assets

- User accounts and their roles.
- The inventory, including DGUV V3 inspection records.
- `secret.key`, which protects secrets stored in the database (TOTP secrets) and keys the MACs of
  recovery codes.

## Threats

| ID | Threat |
|---|---|
| T1 | Guessing or stealing passwords (brute force, user enumeration) |
| T2 | Session theft or fixation |
| T3 | Cross-site request forgery and clickjacking |
| T4 | Script injection through stored data (XSS) |
| T5 | Theft of the database file |
| T6 | Privilege escalation between roles |
| T7 | Misuse or leakage of API tokens |

## Mitigations

### Passwords and login (T1)

- Argon2id (m=19 MiB, t=2, p=1), minimum length 10.
- Five failed logins within 10 minutes lock the client IP and the user name for 5 minutes.
  Failures for unknown users take the same time as for known ones (dummy hash).
- Re-entering the own password is required to create users or reset passwords (also rate
  limited).

### Second factors (T1, T2)

- TOTP (RFC 6238, ±30 s): the secret is AES-GCM encrypted (associated data binds it to the
  user), shown only during enrollment, and every time step is accepted only once (conditional
  update, safe against parallel requests).
- Recovery codes: 10 × ~49 bits, shown once, stored as HMAC-SHA256 under a subkey of
  `secret.key`, single use.
- Passkeys (WebAuthn, `go-webauthn`): relying-party ID and the single accepted origin come from
  `public_url`; only public keys are stored; sign-count anomalies are flagged on the security
  page. Ceremonies are single use, bound to purpose and user and expire after 5 minutes.
  Passwordless login and passkey re-authentication require user verification (PIN/biometrics),
  so a passkey alone is two factors.
- After a correct password a user with a second factor only gets a *pending* session (5
  minutes, new session ID on completion) that reaches nothing but the second-factor page.
  Wrong codes count toward the same lockout as wrong passwords.
- Policy: admins can require a second factor for admins or everyone; affected users without
  one can only reach the enrollment pages, and cannot remove their last factor.
- Changing second factors, the policy, or another user's MFA requires an authentication younger
  than 10 minutes (passkey, or password plus code). Admins can reset a user's MFA (web or
  `lanventory user reset-mfa`), which also ends that user's sessions.
- All MFA events go to the audit log, never with secrets or codes.

### Sessions (T2, T3)

- The cookie holds a random token; only its SHA-256 is stored. A new session is created on
  every login. Sessions end on password change, disabling and deletion of the user.
- Cookie flags: `HttpOnly`, `SameSite=Strict`, `Secure` with an `https://` `public_url`.
- Every state-changing request needs the session's CSRF token and a same-origin `Origin`
  (`null` and foreign origins are rejected, `Sec-Fetch-Site: cross-site` too).
- `X-Frame-Options: DENY` and CSP `frame-ancestors 'none'`.

### Content (T4)

- Strict CSP: `default-src 'self'; script-src 'self'; style-src 'self'; …; object-src 'none'`.
  Templates contain no inline scripts, styles or event handlers (a test checks this).
- All output goes through `html/template`.

### Stored secrets (T5)

- Secrets are encrypted with AES-256-GCM, bound to associated data. The key is in a separate
  file (`secret.key`, mode 0600/0640), so a copy of the database alone does not reveal them.
  Password hashes are Argon2id.
- The audit log never contains secrets.

### Roles (T6)

- Roles are checked in the router, not only in the UI. The last enabled admin cannot be
  demoted, disabled or deleted.

### API tokens (T3, T6, T7)

- A token is `lvk_` plus 40 random base62 characters (~238 bits), shown once; only its SHA-256
  hash is stored (a database copy does not reveal usable tokens). Tokens never appear in logs,
  the audit log (only name, prefix and token ID) or later responses.
- A token acts as its owner and is limited by scopes; the effective permission is scope AND the
  owner's current role and flags, so demoting or disabling the owner takes effect at once.
  Revoked, expired and disabled-owner tokens are rejected with 401. Category writes are not
  exposed. Creating a token requires a recent authentication; users revoke their own tokens,
  admins any.
- Bearer requests carry no ambient credentials, so they need no CSRF token. Browser sessions
  calling the API need `X-CSRF-Token` and a same-origin request, are refused while a second
  factor is pending and follow the MFA policy.
- Per-token rate limit (`api.rate_limit`, `api.rate_burst`). Use HTTPS: a bearer token is a
  password in transit.

### CSV import and export

- Exports guard every cell that starts with `=`, `+`, `-`, `@`, tab or CR with a leading
  apostrophe (formula injection in spreadsheets); the import removes it again.
- The import is limited to editors; the upload (5 MB, 20 000 rows, 100 columns) is parsed in
  memory and kept there only between the wizard steps: random token bound to user and session,
  one upload per user, 30 minutes, never written to disk. Cells are untrusted text: they are
  validated like form input and escaped when shown. Imports are audited (`asset.import`,
  `asset.create`/`asset.update` with `via: import`).

## Residual risks

- A leaked token works until it expires or is revoked; give tokens the fewest scopes and an expiry.
- Without HTTPS passwords are visible on the network (the UI warns).
- Second factors are optional unless an admin sets the policy.
- WebAuthn ceremonies live in memory: a restart cancels running ones (users simply retry).
- Anyone with root on the host can read `secret.key` and the database.
- Losing `secret.key` makes TOTP secrets unreadable and recovery codes unusable; back it up
  together with the database.
