# Runnix

[![ci](https://github.com/ishaan-jindal/runnix/actions/workflows/ci.yml/badge.svg)](https://github.com/ishaan-jindal/runnix/actions/workflows/ci.yml)
[![release](https://img.shields.io/github/v/release/ishaan-jindal/runnix)](https://github.com/ishaan-jindal/runnix/releases)
[![license](https://img.shields.io/github/license/ishaan-jindal/runnix)](LICENSE)

Multi-tenant sandboxed code execution: authenticate, submit source, poll
or receive a webhook for the result. Go + NATS JetStream + Postgres;
every run is isolated, bounded, and auditable.

## Features

- **Multi-tenant auth** — JWT or `sk_live_...` API keys; tenant routes need
  `Authorization: Bearer <token>` plus `X-Tenant-ID`.
- **Submit and poll** — `POST /executions` enqueues on JetStream;
  `GET /executions/{id}` returns status, stdout/stderr, exit code.
- **Sandboxed runs** — one gVisor (`runsc`) container per execution, no
  network, read-only rootfs (limits in Security model).
- **Submit quotas** — per-tenant hourly + active caps; over-limit submits
  get `429` with `Retry-After` (`QUOTAS_ENABLED=false` disables).
- **Signed webhooks** — HMAC-SHA256 delivery, SSRF-guarded.
  See `docs/webhooks.md`.
- **Crash-safe queue** — at-least-once delivery with a DB claim guard
  (duplicates acked, never re-run) plus a stale-running reaper.
- **Zero-touch schema** — embedded Postgres migrations applied at startup.

## Architecture

```text
Client ──HTTP──▶ Gateway ──publish──▶ NATS JetStream ──consume──▶ Dispatcher
                    │                    exec.submit.<lang>              │
                    │                                                   │ create
              Postgres ◀── write result ── sandbox container ── run ────┘
              (users, tenants,                     │  runsc
               executions, audit)                  ▼
                                        exec.result.<id> ──▶ webhook POST
```

- **Gateway** (`cmd/gateway`) — stateless HTTP: auth, validation, tenancy, publish.
- **Dispatcher** (`cmd/dispatcher`) — JetStream consumer, sandbox runs, result writes, webhooks, reaper.
- **NATS JetStream** — durable `EXEC_SUBMIT` / `EXEC_RESULT` streams.
- **Postgres** — users, tenants, memberships, executions (sqlc + pgx).

## API

Tenant routes require `Authorization: Bearer <token>` and `X-Tenant-ID`.
Full shapes in `api/openapi.yaml`.

| Method & path | Description |
|---|---|
| `GET /healthz`, `GET /readyz` | Liveness; readiness (Postgres + NATS) |
| `GET /languages` | Supported languages (`["python"]`) |
| `POST /auth/register` | Create user + personal tenant, token pair |
| `POST /auth/login` | Token pair by email or username |
| `POST /auth/refresh` | Rotated token pair |
| `POST /auth/logout` | Revoke one refresh token (idempotent) |
| `POST /auth/logout-all` | Revoke all refresh tokens |
| `GET /users/me` | Caller identity + memberships |
| `POST /tenants` | Create org tenant (caller becomes owner) |
| `GET /tenants/{id}` | Get tenant (id must match `X-Tenant-ID`) |
| `POST /tenants/{id}/api-keys` | Mint key, full secret returned once |
| `GET /tenants/{id}/api-keys` | List key metadata (never secrets) |
| `DELETE /tenants/{id}/api-keys/{keyId}` | Revoke a key |
| `POST /executions` | Submit code (`202 queued`; `429` quota; `502` queue down) |
| `GET /executions` | List tenant executions, newest first (paged) |
| `GET /executions/{id}` | One execution with output (`404` cross-tenant) |

Error envelope is always `{"error": "..."}`. Auth errors: `401` bad
token, `400` missing `X-Tenant-ID`, `403` not-a-member.

## Quickstart

Prerequisites: Go 1.26, `just`, `sqlc`, Docker with the `runsc` runtime.

```bash
just compose-up          # postgres:16 + nats:2 + gateway/dispatcher (+ runner image build)
curl localhost:4000/healthz
curl localhost:4000/languages
```

Register, submit, poll:

```bash
REG=$(curl -s -X POST localhost:4000/auth/register \
  -d '{"username":"ada","email":"ada@example.com","password":"SecurePass123"}')
TOKEN=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['accessToken'])")
TENANT=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['tenants'][0]['id'])")
AUTH="Authorization: Bearer $TOKEN"

SUB=$(curl -s -X POST localhost:4000/executions -H "$AUTH" -H "X-Tenant-ID: $TENANT" \
  -d '{"language":"python","source":"print(40+2)"}')
ID=$(echo "$SUB" | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")

curl -s localhost:4000/executions/$ID -H "$AUTH" -H "X-Tenant-ID: $TENANT" | python3 -m json.tool
# status walks queued -> running -> succeeded; stdout holds "42\n"
```

To stop everything (drops the database volume): `just compose-down`.

## Deployment

Hosted dev/prod run on one VM behind Caddy; see `deploy/README.md`.
Laptop path stays `just compose-up` (`deploy/compose.yaml`).

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `4000` | Gateway listen port |
| `ENV` | `development` | `development` / `staging` / `production` |
| `DATABASE_URL` | local postgres DSN | — |
| `NATS_URL` | `nats://localhost:4222` | — |
| `JWT_SECRET` | — (empty in dev) | Required outside `development` |
| `WEBHOOK_SIGNING_SECRET` | — (empty) | Enables `webhook_url`; same on gateway + dispatcher |
| `WEBHOOK_ALLOW_PRIVATE` | `false` | Allow private hosts (dev/tests only) |
| `EXEC_WORKERS` | `2` | Dispatcher worker pool size |
| `RUNNER_IMAGE` | `runnix-runner-python:local` | Untrusted-code image |
| `RUNNER_RUNTIME` | `runsc` | Container runtime (`""` = default) |
| `QUOTAS_ENABLED` | `true` | Enforce quotas on `POST /executions` |
| `REAP_INTERVAL` | `1m` | Stale-running sweep frequency |
| `REAP_STALE_AFTER` | `5m` | `running` older than this fails (must exceed 60 s max `timeout_s`) |

## Development

```bash
just test                # go test -race ./... (unit + testcontainers integration)
just vet                 # go vet ./...
just lint                # golangci-lint (CI pins v2)
just build               # binaries to dist/
just generate            # sqlc generate (needs the sqlc binary)
just migrate-up          # apply migrations to a standalone DB (compose auto-migrates)
just dev                 # run the gateway locally
just dispatcher          # run the dispatcher locally (needs Docker + Postgres + NATS)
```

Integration tests use real Postgres/NATS/containers; skipped without Docker.

## Security model

- **Sandbox** — per-execution container: gVisor, UID/GID 65534, no network,
  read-only rootfs (+ writable `/tmp`, `/work`), `cap-drop: ALL`, 128 MB RAM
  (+128 MB swap), 0.5 CPU, 32 pids, 1–60 s timeout, 64 KiB output cap each,
  OOM reported as failure.
- **Tenancy** — users ↔ tenants many-to-many via `memberships`, re-checked
  per request; cross-tenant reads return `404`.
- **Webhooks** — public http(s) only (`WEBHOOK_ALLOW_PRIVATE` lifts for dev),
  HMAC-SHA256 over `<timestamp>.<body>`, 3× retry, no redirects.
  See `docs/webhooks.md`.

## License

MIT — see [LICENSE](LICENSE).
