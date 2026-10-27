# AGENTS.md — Runnix

Repo map, commands, and hard conventions for agentic contributors.
Human onboarding lives in `CONTRIBUTING.md`; repo truth lives in `README.md`.

## What this is

Go monorepo: multi-tenant sandboxed code execution. Gateway (stateless HTTP)
publishes to NATS JetStream; dispatcher runs each execution in its own gVisor
(`runsc`) container and writes results to Postgres. Supported languages are
defined in code — see `SupportedLanguages` (surfaced by `GET /languages`).

- `cmd/gateway`, `cmd/dispatcher` — service entrypoints
- `internal/{auth,config,tenants,executions,http,nats,store,dispatcher,webhooks}` — core
- `internal/store/{migrations,queries}` + `storedb/` (**sqlc-generated, never hand-edit**)
- `runner/docker/` — sandbox image (`python:3.12-slim`, UID 65534)
- `api/openapi.yaml` — full API contract; `docs/webhooks.md`
- `deploy/compose.yaml` — laptop stack; `compose.{caddy,prod,dev}.yaml` — hosted VM

## Commands (`just --list` shows all)

```bash
just compose-up     # full laptop stack (postgres:16 + nats:2 + gateway/dispatcher)
just test           # go test -race ./... (testcontainers; skips sandbox tests w/o Docker)
just vet            # go vet ./...
just lint           # golangci-lint (CI pins v2.13.2)
just build          # binaries to dist/ (gitignored)
just generate       # sqlc generate — run after touching queries/ or migrations/
just migrate-up     # apply migrations to a standalone DB (compose auto-migrates)
just deploy-check   # render-check hosted compose files without starting anything
```

CI (`ci.yml`) runs vet + race tests + lint + build on pull requests and
pushes to `main` (feature-branch pushes are not a second run).
`docker.yml` builds GHCR images for those same events (PRs build-only, no
push) and on `v*` tags. `release.yml` is tag-gated (`v*`) with a
vet/test/build gate first.

## Conventions (enforced in review)

- Commit style: `<prefix>: <subject>` — `feat/fix/chore/docs/ci`. Sign every
  commit: `git commit -s -S`. Never commit secrets (`.env` files stay local).
- Never add agent attribution. No `Co-authored-by:` trailers for any AI,
  agent, or tool (Cursor, Claude, OpenCode, Codex, etc.), and no "Generated
  with" or "Made with" lines in commits, PR or issue titles and bodies, or
  comments. Strip any that a tool inserts automatically.
- No `phase` wording in commits/code/docs — use `deferred: <topic>` tags.
  Stacked PRs: reword any draft commit that would land in `main` with
  `git rebase -i` + `squash`/`reword` before the base PR merges, or the
  no-`phase` rule fails on first push.
- Storage changes: new `internal/store/migrations/NNNN_*.sql` (embedded,
  idempotent, replica-safe) + `queries/*.sql`, then `just generate`.
  Migrations apply automatically at service startup.
- API changes: update `api/openapi.yaml` + handler + `docs/` in the same commit.
- `CHANGELOG.md` (Keep a Changelog): user-facing changes get an entry.
- Error envelope is always `{"error": "..."}`. Auth: `401` bad token,
  `400` missing `X-Tenant-ID`, `403` not-a-member, cross-tenant reads `404`.
- Tests live next to code (`*_test.go`); use table-driven tests where shapes repeat.

## Gotchas

- Dispatcher needs the Docker socket + `RUNNER_RUNTIME=runsc` locally; the
  sandbox image must exist (`compose-up` builds it via the `runner`
  service).
- Work files enter the sandbox as a tar over the container's stdin — no bind
  mounts (they don't resolve through a mounted socket), `CopyToContainer`
  refuses read-only rootfs.
- Queue is at-least-once: keep the DB claim guard (`MarkExecutionRunning`
  conditional claim) — duplicates ack, never re-run. Consumer `MaxDeliver 5`,
  `AckWait 90s` (must exceed the 60 s max `timeout_s`).
- `WEBHOOK_ALLOW_PRIVATE=true` is dev/tests only; never enable in prod compose.
  Webhook delivery never follows redirects; signatures are HMAC-SHA256
  `sha256=<hex>` over `<timestamp>.<body>`.
- `ENV != development` refuses to boot without `JWT_SECRET`.
- Integration tests boot real Postgres/NATS containers; NATS monitor port 8222
  can reset connections in some setups — verify stream state via a throwaway
  Go inspector, not the monitor port.
- Go toolchain is 1.26 (`go-version-file` in CI); local 1.27 works, don't bump
  `go.mod` casually.
