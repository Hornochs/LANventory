# 0002 – Server-rendered templates with htmx, no Node build

**Status:** accepted

## Context

The UI is forms, lists and detail pages for a few users. A single-page app would add a build
chain and a large dependency surface, and make a strict Content Security Policy harder.

## Decision

Pages are rendered on the server with `html/template` (escaping by default). Interactive parts,
such as search and filters in lists, use a vendored copy of htmx. Scripts and styles are files
under `internal/web/static/`; there are no inline scripts or styles, and the CSP forbids them.
There is no Node.js or npm in the build. Texts come from `i18n.go` (English and German).

## Consequences

- Strict CSP (`script-src 'self'`, no inline code) can be enforced and is tested.
- Contributors need only Go; the vendored htmx file is updated by hand.
- Small JavaScript needs (WebAuthn, theme) are written as separate plain files without
  third-party libraries.
- Rich client-side interactions are limited to what htmx and small scripts can do.
