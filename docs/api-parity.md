# API Notes: runnix-legacy (frozen) vs runnix (fresh API)

`runnix-legacy` (Node/TS v0.1.0, Express) is frozen and archived. `runnix` is a
fresh multi-tenant API — no route is copied verbatim and there is no
compatibility contract between them. This table exists only to show where
legacy behavior went.

| Legacy (frozen) | runnix | Notes |
|---|---|---|
| `POST /auth/register\|login\|refresh\|logout\|logout-all` | `POST /auth/register\|login\|refresh` | Rebuilt; access token carries `tenants[]`, refresh re-resolves memberships. Logout = client discards + server revocation list (deferred: auth-plus-postgres). |
| `GET/PATCH /auth/me`, password, api-keys CRUD | `GET /users/me` + `POST /tenants/:id/api-keys` (deferred: tenant-api-keys) | Scoped per tenant; user-level keys optional. |
| `POST /admin/users/*` | Tenant `owner/admin` roles via `memberships` | No global admin path; bootstrap owner via register (personal tenant). |
| `POST /submit {language, code}` | `POST /executions {tenant_id, language, source, ...}` | `202 {id, status: queued}`; requires `X-Tenant-ID` (`tenant_id` must match). Persisted, then published to JetStream `exec.submit.<lang>` (`502` + `failed` when the queue is down). Python only, `timeout_s` 1–60 (default 2). |
| `GET /result/:id`, `GET /jobs` | `GET /executions/:id`, `GET /executions?tenant_id=` | Always tenant-filtered (`AND tenant_id = $2`). Dispatcher transitions `queued→running→succeeded/failed/timeout` and writes `stdout`/`stderr`/`exit_code`. |
| `GET /languages` | `GET /languages` | Same shape `{languages: [...]}`; currently `["python"]`. |
| — (v1 had no orgs) | `POST /tenants {name, slug?}`, `GET /tenants/:id` | Org tenant; caller becomes `owner`. Path id must equal `X-Tenant-ID`. |
| Webhooks (HMAC, SSRF guard) | Signed delivery on completion (`docs/webhooks.md`) | `webhook_url` is SSRF-guarded at submit and delivery (http(s), public hosts only), and completion events are HMAC-SHA256-signed over `<timestamp>.<body>` with 3 attempts. Per-tenant signing secrets remain tied to api-keys (deferred: tenant-api-keys). |
| `GET /health\|/status\|/metrics` | `GET /healthz`, `/readyz`, `/metrics` | Cloud-native naming; `/readyz` runs live Postgres + NATS checks (503 when down). Tenant labels reserved. |

Auth errors: `401` bad token, `400` missing `X-Tenant-ID`, `403` not-a-member.
