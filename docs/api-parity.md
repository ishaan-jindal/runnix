# API Parity: legacy → v2

Legacy (`runnix-legacy` v0.1.0, Express) was flat and single-tenant-implicit.
v2 is tenant-first and versioned. No route is copied verbatim.

| Legacy | v2 | Fate |
|---|---|---|
| `POST /auth/register|login|refresh|logout|logout-all` | `POST /v1/auth/register|login|refresh` | Rebuilt; access token carries `tenants[]`, refresh re-resolves memberships. Logout = client discards + server revocation list (Phase 1). |
| `GET/PATCH /auth/me`, password, api-keys CRUD | `GET /v1/users/me` + `POST /v1/tenants/:id/api-keys` (Phase 1) | Scoped per tenant; user-level keys optional. |
| `POST /admin/users/*` | Tenant `owner/admin` roles via `memberships` | No global admin path in scaffold; bootstrap owner via seed. |
| `POST /submit {language, code}` | `POST /v1/executions {tenant_id, language, source, ...}` | `202 {id, status: queued}`; requires `X-Tenant-ID`. |
| `GET /result/:id`, `GET /jobs` | `GET /v1/executions/:id`, `GET /v1/executions?tenant_id=` | Always tenant-filtered (`AND tenant_id = $2`). |
| `GET /languages` | `GET /v1/languages` | Same shape `{languages: [...]}`. |
| Webhooks (HMAC, SSRF guard) | `webhook_url` on execution + per-tenant secret (Phase 1) | `internal/webhooks` keeps http(s)-only now; private/loopback/metadata block ported in Phase 1. |
| `GET /health\|/status\|/metrics` | `GET /healthz`, `/readyz`, `/metrics` | Cloud-native naming; tenant labels reserved. |

Auth errors: `401` bad token, `400` missing `X-Tenant-ID`, `403` not-a-member.
