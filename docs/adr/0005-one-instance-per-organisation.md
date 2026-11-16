# 0005 – One instance per organisation

**Status:** accepted

## Context

Multi-tenancy needs tenant columns on every table, tenant-aware authorization and careful
testing of isolation. A LAN party team runs one inventory.

## Decision

One LANventory instance serves one organisation. There are no tenants or organisation tables.
Separate organisations run separate instances (own binary, config and database).

## Consequences

- No cross-tenant data leaks are possible by design; permissions are only roles and the
  `can_inspect` flag.
- Operating several organisations means several instances, which the single binary and SQLite
  make cheap.
- Adding multi-tenancy later would be a large change and need a new decision.
