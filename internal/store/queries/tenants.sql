-- name: CreateTenant :one
INSERT INTO tenants (slug, namespace, tier) VALUES ($1, $2, $3)
RETURNING id, slug, namespace, tier, created_at;

-- name: AddMembership :exec
INSERT INTO memberships (user_id, tenant_id, role) VALUES ($1, $2, $3)
ON CONFLICT (user_id, tenant_id) DO UPDATE SET role = EXCLUDED.role;

-- name: CheckMembership :one
SELECT role FROM memberships WHERE user_id = $1 AND tenant_id = $2;
