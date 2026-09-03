-- name: CreateExecution :one
INSERT INTO executions (tenant_id, language, source, stdin)
VALUES ($1, $2, $3, $4)
RETURNING id, tenant_id, language, status, created_at;

-- name: GetExecution :one
SELECT id, tenant_id, language, status, source, stdin, stdout, stderr, exit_code, created_at, updated_at
FROM executions WHERE id = $1 AND tenant_id = $2;

-- name: ListExecutions :many
SELECT id, language, status, created_at, updated_at
FROM executions WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;
