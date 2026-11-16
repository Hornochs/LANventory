# 0007 – OpenAPI as the single source of truth for the API

**Status:** accepted (implemented)

## Context

The REST API will be used by scripts and tools, and described for humans and agents. Hand-kept
docs drift from the code.

## Decision

The OpenAPI document is written first and embedded in the binary. Tests check that every
route is in the spec and every spec path is routed, and validate responses against the spec.
`/api/openapi.json` and the `/api/` docs are served from it, and `llms.txt` points to them.
Errors use `application/problem+json` (RFC 9457). Tokens are `lvk_…`, stored hashed, with
scopes `read:assets`, `write:assets`, `read:inspections` and `write:inspections`.

## Consequences

- Docs and behavior cannot silently diverge; a new endpoint means a spec change and a test
  call.
- An OpenAPI validation library (kin-openapi) becomes a test dependency.
- Changing the API means editing the spec first.
