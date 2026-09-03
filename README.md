# Runnix v2 — scaffold (v0.0.1-scaffold)

Multi-tenant sandboxed code execution on Kubernetes (Go + NATS JetStream + Postgres).
Successor to `runnix-legacy` (Node, frozen). See vault `Runnix` for design.

## Tenancy

`users <-> tenants` many-to-many via `memberships`. Every `/v1/*` tenant route
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
curl localhost:4000/v1/languages
```

Env: `PORT=4000 ENV=development DATABASE_URL=... NATS_URL=... JWT_SECRET=...`
(`JWT_SECRET` required outside development).

## Layout

`cmd/gateway`, `cmd/dispatcher`, `internal/{config,http,auth,tenants,executions,nats,store,webhooks}`,
`api/openapi.yaml`, `docs/api-parity.md`, `deploy/compose.yaml`, `charts/` (deferred: helm).

## Status

Scaffold only: routes return `501`, dispatcher idles, Helm/RuntimeClass/Job execution deferred.
Next: gateway MVP (deferred: auth-plus-postgres, nats-publish).
