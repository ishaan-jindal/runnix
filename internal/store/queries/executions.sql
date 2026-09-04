-- name: CreateExecution :one
INSERT INTO executions (tenant_id, language, source, stdin, timeout_s, webhook_url)
VALUES ($1, $2, $3, $4, $5, $6)
RETURNING id, tenant_id, language, status, created_at;

-- name: GetExecution :one
SELECT id, tenant_id, language, status, source, stdin, stdout, stderr, exit_code, created_at, updated_at
FROM executions WHERE id = $1 AND tenant_id = $2;

-- name: ListExecutions :many
SELECT id, language, status, created_at, updated_at
FROM executions WHERE tenant_id = $1 ORDER BY created_at DESC LIMIT $2 OFFSET $3;

-- name: SetExecutionFailed :exec
-- Marks a queued execution failed (e.g. the submit queue was unreachable).
UPDATE executions
SET status = 'failed', stderr = $2, updated_at = now()
WHERE id = $1 AND status = 'queued';

-- name: MarkExecutionRunning :execrows
-- Claims a queued execution for the dispatcher. Only the claim that
-- transitions queued->running runs the code; duplicate JetStream deliveries
-- (at-least-once) see 0 rows and are acked and skipped.
UPDATE executions
SET status = 'running', updated_at = now()
WHERE id = $1 AND status = 'queued';

-- name: FinishExecution :exec
-- Writes a result for a running execution. status is one of
-- succeeded/failed/timeout (validated by the status CHECK constraint).
UPDATE executions
SET status = $2, stdout = $3, stderr = $4, exit_code = $5, updated_at = now()
WHERE id = $1 AND status = 'running';

-- name: ReapStaleExecutions :execrows
-- Fails executions stuck "running" because their dispatcher died between
-- claim and finish. Guarded by status + staleness, so multiple replicas and
-- late JetStream redeliveries are harmless.
UPDATE executions
SET status = 'failed', stderr = sqlc.arg(stderr), updated_at = now()
WHERE status = 'running' AND updated_at < now() - make_interval(secs => sqlc.arg(stale_secs)::int);
