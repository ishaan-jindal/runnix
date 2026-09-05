# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).
Everything below `1.0.0` is pre-release and may change without notice.

## [Unreleased]

## [0.1.0] - 2026-09-04

First working slice: authenticate, submit Python code, poll for the result —
sandboxed, metered by tenant, with signed completion webhooks.

### Added

- Multi-tenant auth: `POST /auth/register`, `/auth/login`, `/auth/refresh`
  with bcrypt passwords, JWT access/refresh pairs, and `tenants[]` claims.
  Register auto-provisions a personal tenant (owner) in one transaction.
- Executions API: `POST /executions` (submit, `202 {id, status: queued}`),
  `GET /executions` (paged list), `GET /executions/{id}` (status, stdout,
  stderr, exit code). Tenant-scoped; cross-tenant reads return `404`.
- Tenants API: `POST /tenants` (org tenant, caller becomes owner),
  `GET /tenants/{id}`. Path id must equal `X-Tenant-ID`.
- NATS JetStream pipeline: durable `EXEC_SUBMIT` / `EXEC_RESULT` streams;
  gateway publishes `exec.submit.<lang>`; queue failure marks the row
  `failed` and returns `502`.
- Dispatcher execution loop: durable pull consumer, conditional
  `queued → running` claim (duplicates acked, never re-run), one sandbox
  container per execution (gVisor `runsc`, non-root, no network, read-only
  rootfs, 128 MB / 0.5 CPU / 32 pids, 1–60 s timeout), result write-back
  plus `exec.result.<id>` summary.
- Signed webhooks: `webhook_url` on submissions, HMAC-SHA256 delivery with
  retries, SSRF guard at submit and delivery time; `503` when no signing
  secret is configured.
- Sandboxes: stale-`running` reaper, OOM detection, 64 KiB output
  truncation, boot-time leftover-container sweep.
- Runner image: `runner/docker` (`python:3.12-slim`, numeric nobody user).
- Postgres schema with embedded idempotent migrations (auto-applied at
  startup) and sqlc-generated queries; live `/readyz` checks.
- Compose stack (postgres:16, nats:2, gateway, dispatcher), CI
  (vet + race + lint + build), OpenAPI spec, webhook signing docs.

### Fixed

- Tenant creation slug-conflict retry aborted the whole transaction
  (25P02); retries now run in per-attempt savepoints (register and
  org-tenant creation).

[Unreleased]: https://github.com/ishaan-jindal/runnix/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/ishaan-jindal/runnix/releases/tag/v0.1.0
