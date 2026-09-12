-- name: CreateRefreshToken :one
INSERT INTO refresh_tokens (jti, user_id, expires_at) VALUES ($1, $2, $3)
RETURNING jti, user_id, expires_at, revoked_at, created_at;

-- name: GetRefreshToken :one
SELECT jti, user_id, expires_at, revoked_at, created_at FROM refresh_tokens WHERE jti = $1;

-- name: RevokeRefreshToken :exec
UPDATE refresh_tokens SET revoked_at = now() WHERE jti = $1 AND revoked_at IS NULL;

-- name: RevokeAllRefreshTokens :execrows
UPDATE refresh_tokens SET revoked_at = now() WHERE user_id = $1 AND revoked_at IS NULL;

-- name: CreateAPIKey :one
INSERT INTO api_keys (id, user_id, tenant_id, name, secret_hash) VALUES ($1, $2, $3, $4, $5)
RETURNING id, user_id, tenant_id, name, created_at, revoked_at;

-- name: GetAPIKey :one
SELECT id, user_id, tenant_id, name, secret_hash, created_at, revoked_at FROM api_keys WHERE id = $1;

-- name: ListAPIKeys :many
SELECT id, name, created_at, revoked_at FROM api_keys WHERE tenant_id = $1 ORDER BY created_at DESC;

-- name: RevokeAPIKey :exec
UPDATE api_keys SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL;
