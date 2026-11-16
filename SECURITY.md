# Security policy

LANventory stores the inventory of a LAN party, including the results of electrical safety
inspections (DGUV V3), and has user accounts with different roles. Security reports are taken
seriously and handled with priority.

## Supported versions

Only the latest release and the `main` branch receive security fixes.

## Reporting a vulnerability

Please **do not open a public issue** for security problems.

Report privately through GitHub: open the repository's **Security** tab and choose
**Report a vulnerability** (private vulnerability reporting). Include:

- what you found and where (file, endpoint, version or commit),
- how to reproduce it,
- what an attacker could achieve.

You can expect an acknowledgement within 7 days. Once a fix is available it is released together
with an advisory that credits you, unless you prefer to stay anonymous.

## Security model in brief

The threat model, mitigations and residual risks are in [docs/security.md](docs/security.md).

- Every page except the login page, `/health` and static files requires a login. Passwords are
  hashed with Argon2id; failed logins lead to a temporary lockout. Sessions are server-side;
  the cookie is `HttpOnly`, `SameSite=Strict` and `Secure` with an `https://` public URL.
- State-changing requests need a CSRF token and a same-origin `Origin` header.
- A strict Content Security Policy forbids inline scripts and styles; all output is escaped.
- Secrets stored in the database (TOTP secrets) are encrypted with AES-256-GCM, with a
  key stored outside the database.

## Known limitations

- Without HTTPS (reverse proxy or built-in TLS) passwords travel unencrypted; the UI warns
  about it.
- Second factors (TOTP, passkeys) are optional unless an admin sets an MFA policy; without a
  policy a password can be the only factor.
- API tokens are bearer credentials: use HTTPS and give them the fewest scopes and an expiry.

## Out of scope

- Attacks that require an already compromised admin account or root on the LANventory host.
- Denial of service by a user with network access to a non-public installation.
