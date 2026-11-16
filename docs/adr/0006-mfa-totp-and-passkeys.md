# 0006 – MFA with TOTP and WebAuthn passkeys

**Status:** accepted, implemented

## Context

The inventory contains little personal data but admins can change everything; passwords alone
are weak, and many teams already use authenticator apps or hardware keys.

## Decision

- Second factor: TOTP (RFC 6238) and WebAuthn passkeys, using `go-webauthn`. Passkeys can also
  be used for passwordless login (discoverable credentials).
- TOTP seeds are encrypted with AES-256-GCM using the key in `secret.key`; they are never
  logged or shown again after enrollment. The last used time step is stored to reject replays.
- Recovery codes are generated once, shown once and usable once. They are stored as HMAC-SHA256
  under a subkey of `secret.key` (not a plain hash), so a stolen database alone does not allow
  offline guessing; their high entropy makes a slow hash unnecessary.
- An administrator can require MFA for admins or everyone, and can reset a user's MFA.
- Sensitive actions require recent re-authentication.
- The WebAuthn browser code is a small own script without third-party libraries.

## Consequences

- Passkeys need HTTPS and a stable `public_url`; the relying-party ID is derived from it, and
  changing the host invalidates passkeys.
- Losing `secret.key` makes TOTP seeds unreadable (users fall back to recovery codes or an admin
  reset); it must be backed up with the database.
- TOTP is implemented in-house (~100 lines, RFC test vectors) instead of adding a library.
  New dependencies: `go-webauthn` (first pinned to v0.15.0 for Go 1.24; v0.18 since the move
  to Go 1.26) and
  `skip2/go-qrcode` for the enrollment QR code.
