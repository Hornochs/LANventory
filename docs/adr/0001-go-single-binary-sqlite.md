# 0001 – Go single binary with SQLite

**Status:** accepted

## Context

LANventory is run by small volunteer teams, often on a small VM or a Raspberry Pi, and must be
easy to install, back up and update. Sibling projects (LANLeecher, ERS-Proxy) use the same
stack.

## Decision

LANventory is written in Go and ships as one static binary per architecture, built with
`CGO_ENABLED=0`. Data lives in SQLite through the pure-Go driver `modernc.org/sqlite` (WAL,
foreign keys on). Migrations are embedded goose files; backups use `VACUUM INTO`.

## Consequences

- No runtime, database server or virtual environment to install; packages contain the binary,
  a systemd unit and a config file.
- Templates, static files and migrations are embedded in the binary.
- One writer at a time; fine for an inventory with a handful of users.
- No PostgreSQL or MySQL support. Moving to one later would need a new store implementation.
