-- name: CreateTenant :one
-- id is app-assigned so the namespace (runnix-tenant-<id>) is known before insert.
INSERT INTO tenants (id, slug, namespace, tier) VALUES ($1, $2, $3, $4)
RETURNING id, slug, namespace, tier, created_at;

-- name: GetTenant :one
SELECT id, slug, namespace, tier, created_at FROM tenants WHERE id = $1;

-- name: AddMembership :exec
INSERT INTO memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)
ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = EXCLUDED.role;

-- name: CheckMembership :one
SELECT role FROM memberships WHERE user_id = $1 AND tenant_id = $2;
