# Runnix

Multi-tenant sandboxed code execution. Clients authenticate, submit source
code, and poll (or receive a webhook) for the result. Every execution runs
isolated, resource-bounded, and auditable.

> **Pre-release — v0.1.0.** The API and sandbox posture work end to end on
> Docker Compose. Kubernetes execution (Job-per-execution, namespaces,
> quotas, Helm) is planned for a later slice.

Runnix is the successor to [`runnix-legacy`](https://github.com/ishaan-jindal/runnix-legacy),
a Node.js prototype now frozen and archived. This rewrite keeps the API shape and sandbox guarantees but
replaces the single-VM design with Go services, NATS JetStream, and
Postgres — built to scale onto Kubernetes.

## Features

- **Multi-tenant auth** — JWT access/refresh tokens; users belong to many
  tenants via `memberships`. Every tenant route requires `Authorization:
  Bearer <jwt>` plus `X-Tenant-ID`; `403` always means not-a-member.
- **Submit and poll** — `POST /executions` persists the submission and
  enqueues it on NATS JetStream; `GET /executions/{id}` returns status,
  stdout, stderr, and exit code.
- **Sandboxed runs** — each execution runs in its own container under
  gVisor (`runsc`): non-root user, no network, read-only rootfs, `cap-drop:
  ALL`, 128 MB RAM, 0.5 CPU, 32 pids, configurable 1–60 s timeout.
- **Signed webhooks** — submissions may carry a `webhook_url`. Completion
  events are HMAC-SHA256-signed, retried, and SSRF-guarded at submit and
  delivery time. See `docs/webhooks.md`.
- **Crash-safe queue** — at-least-once JetStream delivery with a
  DB-backed claim guard (duplicates are acked, never re-run), plus a
  reaper that fails executions stranded by a lost dispatcher.
- **Zero-touch schema** — gateway and dispatcher apply embedded Postgres
  migrations idempotently at startup.

## Architecture

```text
Client ──HTTP──▶ Gateway ──publish──▶ NATS JetStream ──consume──▶ Dispatcher
                    │                    exec.submit.<lang>              │
                    │                                                   │ create
              Postgres ◀── write result ── sandbox container ── run ────┘
              (users, tenants,                     │  runsc, 128m/0.5CPU
               executions, audit)                  ▼
                                        exec.result.<id> ──▶ webhook POST
```

- **Gateway** (`cmd/gateway`) — stateless HTTP API: auth, validation,
  tenancy enforcement, JetStream publishing. Live readiness checks.
- **Dispatcher** (`cmd/dispatcher`) — durable JetStream consumer, worker
  pool, one sandbox container per execution, Postgres result writer,
  webhook deliverer, stale-running reaper.
- **NATS JetStream** — durable `EXEC_SUBMIT` / `EXEC_RESULT` streams.
- **Postgres** — users, tenants, memberships, executions (via sqlc + pgx).

## API

All tenant routes require `Authorization: Bearer <jwt>` and `X-Tenant-ID`.
Full request/response shapes live in `api/openapi.yaml`.

| Method & path | Description |
|---|---|
| `GET /healthz`, `GET /readyz` | Liveness; readiness (Postgres + NATS checks) |
| `GET /languages` | Supported languages (currently `["python"]`) |
| `POST /auth/register` | Create user + personal tenant, returns token pair (`201`) |
| `POST /auth/login` | Token pair by email or username (`200`) |
| `POST /auth/refresh` | Rotated token pair, memberships re-resolved (`200`) |
| `POST /tenants` | Create org tenant, caller becomes owner (`201`) |
| `GET /tenants/{id}` | Get tenant; id must equal `X-Tenant-ID` |
| `POST /executions` | Submit code (`202 {id, status: queued}`; `502` if the queue is down) |
| `GET /executions` | List executions for the tenant, newest first (paged) |
| `GET /executions/{id}` | One execution with source, stdout, stderr, exit code (`404` across tenants) |

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
TENANT=$(echo "$REG" | python3 -c "import json,sys; print(json.load(sys.stdin)['tenants'][0]['id'])")

SUB=$(curl -s -X POST localhost:4000/executions -H "X-Tenant-ID: $TENANT" \
  -d '{"language":"python","source":"print(40+2)"}')
ID=$(echo "$SUB" | python3 -c "import json,sys; print(json.load(sys.stdin)['id'])")

curl -s localhost:4000/executions/$ID -H "X-Tenant-ID: $TENANT" | python3 -m json.tool
# status walks queued -> running -> succeeded; stdout holds "42\n"

# With a completion webhook (receiver must be reachable from the dispatcher):
curl -s -X POST localhost:4000/executions -H "X-Tenant-ID: $TENANT" \
  -d '{"language":"python","source":"print(1)","webhook_url":"https://example.com/hook"}'
```

To stop everything (drops the database volume): `just compose-down`.

## Configuration

| Variable | Default | Notes |
|---|---|---|
| `PORT` | `4000` | Gateway listen port |
| `ENV` | `development` | `development` / `staging` / `production` |
| `DATABASE_URL` | local postgres DSN | — |
| `NATS_URL` | `nats://localhost:4222` | — |
| `JWT_SECRET` | — (empty in dev) | Required outside `development` |
| `WEBHOOK_SIGNING_SECRET` | — (empty) | Enables `webhook_url` submissions; same value on gateway and dispatcher |
| `WEBHOOK_ALLOW_PRIVATE` | `false` | Allow loopback/private webhook hosts (dev/tests only) |
| `EXEC_WORKERS` | `2` | Dispatcher sandbox worker pool size |
| `RUNNER_IMAGE` | `runnix-runner-python:local` | Image untrusted code runs in |
| `RUNNER_RUNTIME` | `runsc` | Container runtime (`""` = daemon default) |
| `REAP_INTERVAL` | `1m` | Stale-running sweep frequency |
| `REAP_STALE_AFTER` | `5m` | `running` older than this is failed (must exceed the 60 s max `timeout_s`) |

In Kubernetes the same keys are provided via ConfigMap/Secret. See
`deploy/compose.yaml` for the local values.

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

Integration tests boot real Postgres/NATS containers and run real code
in the sandbox; they skip automatically when Docker is unavailable.
Conventions: no `phase` wording in commits or code — use `deferred:
<topic>` tags for follow-ups. Commits are signed off and GPG-signed.

## Security model

- **Sandbox** — per-execution container: gVisor runtime, UID/GID 65534,
  `NetworkMode: none`, read-only rootfs with writable tmpfs `/tmp` and
  `/work`, `cap-drop: ALL`, 128 MB RAM (+128 MB swap), 0.5 CPU, 32 pids.
  Code and stdin arrive as a tar over the container's stdin (no bind
  mounts, so it works identically from inside or outside Docker).
  Stdout/stderr are truncated at 64 KiB each. OOM kills are detected and
  reported as failures.
- **Tenancy** — users ↔ tenants is many-to-many via `memberships`; the
  middleware re-checks membership per request (a stale token can't reach
  a tenant you've left). Cross-tenant reads return `404`, never leak.
- **Webhooks** — `webhook_url` must be http(s) on a public host (DNS
  re-resolved at delivery; `WEBHOOK_ALLOW_PRIVATE` lifts this for local
  dev). Deliveries are HMAC-SHA256-signed over `<timestamp>.<body>`,
  retried 3× on transport errors/5xx/429, never follow redirects.

Known dev-only posture: the compose dispatcher mounts the host Docker
socket and runs as root to read it. Kubernetes execution removes the
socket entirely (Job-per-execution with a service account).

## License

MIT — see [LICENSE](LICENSE).
