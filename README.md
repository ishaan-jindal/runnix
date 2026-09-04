# Runnix — scaffold (v0.0.1-scaffold)

Multi-tenant sandboxed code execution on Kubernetes (Go + NATS JetStream + Postgres).
Successor to `runnix-legacy` (Node, frozen). See vault `Runnix` for design.

## Tenancy

`users <-> tenants` many-to-many via `memberships`. Every tenant route
requires `Authorization: Bearer <jwt>` + `X-Tenant-ID`. JWT carries `tenants[]`;
middleware re-checks membership so `403` means not-a-member (never assume
`user_id == tenant_id`).

## Quickstart

```bash
just compose-up          # postgres:16 + nats + gateway/dispatcher
just test                # go test -race ./...
just lint                # golangci-lint
just generate            # sqlc generate (needs sqlc binary)
curl localhost:4000/healthz
curl localhost:4000/languages
```

Env: `PORT=4000 ENV=development DATABASE_URL=... NATS_URL=... JWT_SECRET=...`
(`JWT_SECRET` required outside development; `WEBHOOK_SIGNING_SECRET` enables
webhooks; `EXEC_WORKERS`, `RUNNER_IMAGE`, `RUNNER_RUNTIME`, `REAP_INTERVAL`,
`REAP_STALE_AFTER` tune the dispatcher).

## Layout

`cmd/gateway`, `cmd/dispatcher`, `internal/{config,http,auth,tenants,executions,nats,store,webhooks,dispatcher}`,
`runner/docker` (sandbox runner image), `api/openapi.yaml`, `docs/api-parity.md`, `docs/webhooks.md`,
`deploy/compose.yaml`, `charts/` (deferred: helm).

## Status

Auth (register/login/refresh), executions (submit/list/get), and tenants
(create/get) are live against Postgres; submits publish to NATS JetStream
(`exec.submit.<lang>`). The dispatcher consumes them, runs each submission in
a Docker sandbox (gVisor `runsc` in dev, `runnix-runner-python:local` image,
`cap-drop=ALL`, read-only rootfs, no network, 128m/0.5CPU/32pids), writes
results back to Postgres, publishes a summary to `exec.result.<id>`, and
delivers signed completion webhooks (`docs/webhooks.md`; SSRF-guarded at
submit and delivery time). A reaper fails executions stranded `running` by a
lost dispatcher.
Gateway and dispatcher auto-apply migrations at startup.
Next: Helm chart, then RuntimeClass/Job execution on Kubernetes.
