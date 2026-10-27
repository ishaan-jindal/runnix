# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Polyglot executions: `POST /executions` now accepts `python`, `javascript`,
  `go`, `java`, `ruby`, `rust`, and `cpp` (see `GET /languages`). All seven
  run in one `runnix-runner` sandbox image (renamed from
  `runnix-runner-python`); the dispatcher picks the source filename and
  toolchain per language. Sandbox is now 192 MB RAM (+192 MB swap) with
  exec-enabled tmpfs so compiled languages can build and run.

## [0.1.2] - 2026-10-24

### Added

- Execution attribution: every new `executions` row records the submitting
  user (`submitted_by_user_id`) and, when the request was authenticated with
  an API key, the key id (`submitted_by_api_key_id`). Both columns are
  nullable with `ON DELETE SET NULL`: pre-existing and dev-mode runs have no
  actor, and deleting a user or key keeps the execution history intact.
- Per-tenant submit quotas: `POST /executions` enforces a trailing-hour
  submit cap plus a concurrent (queued + running) execution cap per tier
  (free 60/hr + 2 active, starter 600/hr + 5, professional 6000/hr + 20,
  enterprise unlimited). Over-limit submits return `429` with a
  `Retry-After` header (seconds). Checks are serialized per tenant via a
  Postgres advisory lock and fail open when the quota path errors.
  Disable with `QUOTAS_ENABLED=false`.
- Stale-queued reaper: when a JetStream message is dropped (e.g.
  `MaxDeliver` exceeded) but the row stays `queued`, the dispatcher's
  reaper sweep now marks it `failed` with reason `stale_queued` after
  `STALE_QUEUED_AFTER` (default `10m`, must exceed `AckWait 90s` ×
  `MaxDeliver 5`; set `0` to disable). Counted as `failed` for quota
  purposes, so a poison submit no longer permanently consumes a free
  tier's active cap with a misleading `429`.

### Changed

- `ENV` no longer defaults to `development`. Only an explicit
  `ENV=development` may run without `JWT_SECRET`; an unset or misspelled
  `ENV` now refuses to boot instead of silently serving tenant routes
  unauthenticated. The laptop compose stack already sets
  `ENV=development`, so `just compose-up` is unchanged.

### Fixed

- Gateway HTTP server sets `ReadHeaderTimeout` (5 s), `ReadTimeout` (15 s),
  `WriteTimeout` (30 s), and `IdleTimeout` (60 s), so slow or stuck clients
  can no longer hold connections open indefinitely.
- Quota checks bound the per-tenant advisory-lock wait to 2 s
  (`SET LOCAL lock_timeout`). A wedged backend can no longer pile up
  blocked submits — and with them shared pool connections — until unrelated
  tenants stall; a timeout fails open as before.
- Reaper sweeps use partial indexes on `executions(updated_at)` where
  `status='running'` and `executions(created_at)` where `status='queued'`
  instead of scanning the whole table.
- Dispatcher retries: a sandbox failure before the container starts
  (`create`/`attach`/`start`) returns the execution to `queued` and lets
  JetStream redelivery re-claim it, so a transient Docker blip no longer
  permanently fails the run. Failures after start are recorded as `failed`
  and acked instead of re-run, so code is never executed twice. A failed
  result write is retried in place three times before giving up.
- The submit queue carries references only (`execution_id`, `tenant_id`,
  `language`). The dispatcher loads source, stdin, timeout, and
  `webhook_url` from the row it claims, so webhooks now fire for the
  persisted URL and the queue no longer duplicates up to 100 KiB of source
  per message.

## [0.1.1] - 2026-09-12

### Added

- Auth sessions: `POST /auth/logout` revokes one refresh token (idempotent
  `200 {"status":"logged out"}` even for unknown tokens),
  `POST /auth/logout-all` revokes every refresh token for the caller, and
  `POST /auth/refresh` now rotates (old jti revoked, unknown/revoked/expired
  jti returns `401`). Logout revokes refresh tokens only; 15-min access
  tokens live out their window.
- Tenant API keys: `POST /tenants/{id}/api-keys` (tenant-scoped, full key
  returned once), `GET /tenants/{id}/api-keys` (no secrets, revoked keys
  included with `revoked_at`), and
  `DELETE /tenants/{id}/api-keys/{keyId}` (idempotent revoke; `404` unknown
  or cross-tenant). Tenant routes accept `Bearer sk_live_...` keys via
  `RequireAuthWithAPIKeys`.
- Users: `GET /users/me` returns `{user: {id, username, email}, tenants}`
  behind `RequireUser` (no `X-Tenant-ID` needed).
- Deployment: GHCR images (`runnix-gateway`, `runnix-dispatcher`,
  `runnix-runner-python`, version plus `latest` on releases, `edge` on
  main), Caddy with automatic TLS, and prod/dev compose stacks on the
  `runnix-public` network (see `deploy/README.md`). The dev stack runs
  Watchtower (scope `runnix-dev`) to auto-update gateway/dispatcher on new
  `edge` images; prod pins versions and updates by hand.

## [0.1.0] - 2026-09-12

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

[Unreleased]: https://github.com/ishaan-jindal/runnix/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/ishaan-jindal/runnix/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/ishaan-jindal/runnix/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/ishaan-jindal/runnix/releases/tag/v0.1.0
