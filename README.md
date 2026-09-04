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
(`JWT_SECRET` required outside development).

## Layout

`cmd/gateway`, `cmd/dispatcher`, `internal/{config,http,auth,tenants,executions,nats,store,webhooks}`,
`api/openapi.yaml`, `docs/api-parity.md`, `deploy/compose.yaml`, `charts/` (deferred: helm).

## Status

Auth (register/login/refresh), executions (submit/list/get), and tenants
(create/get) are live against Postgres; submits publish to NATS JetStream
(`exec.submit.<lang>`). Gateway auto-applies migrations at startup
(`just migrate-up` for standalone DBs). Dispatcher execution loop, runner
images, and Helm/RuntimeClass/Job execution deferred.
Next: dispatcher Docker sandbox runner.
