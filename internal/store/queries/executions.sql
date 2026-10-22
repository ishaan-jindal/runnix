-- name: CreateExecution :one
-- submitted_by_* are NULL when the submit had no authenticated actor
-- (development without JWT_SECRET, or pre-attribution callers).
INSERT INTO executions (tenant_id, language, source, stdin, timeout_s, webhook_url,
                        submitted_by_user_id, submitted_by_api_key_id)
VALUES ($1, $2, $3, $4, $5, $6, sqlc.arg(submitted_by_user_id), sqlc.arg(submitted_by_api_key_id))
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

-- name: MarkExecutionRunning :one
-- Claims a queued execution for the dispatcher and returns the row it will
-- run. Only the claim that transitions queued->running runs the code;
-- duplicate JetStream deliveries (at-least-once) get pgx.ErrNoRows and are
-- acked and skipped. The queue carries references only, so the row is the
-- source of truth for source/stdin/timeout/webhook.
UPDATE executions
SET status = 'running', updated_at = now()
WHERE id = $1 AND status = 'queued'
RETURNING id, tenant_id, language, source, stdin, timeout_s, webhook_url;

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

-- name: ListStaleQueuedExecutions :many
-- Ids of executions stuck "queued" because their submit message never reached
-- a worker (e.g. JetStream gave up at MaxDeliver). Backs the stale-queued
-- reaper sweep; created_at is the moment the row entered "queued".
SELECT id FROM executions
WHERE status = 'queued' AND created_at < now() - make_interval(secs => sqlc.arg(stale_secs)::int);

-- name: MarkStaleQueuedFailed :execrows
-- Fails a stale queued execution found by ListStaleQueuedExecutions. The
-- status guard re-checks at UPDATE time, so a row claimed (queued->running)
-- between the SELECT and this UPDATE is left alone.
UPDATE executions
SET status = 'failed', stderr = sqlc.arg(stderr), updated_at = now()
WHERE id = sqlc.arg(id) AND status = 'queued';

-- name: CountExecutionsSince :one
-- Submits in the trailing window; backs the per-tenant hourly submit quota.
SELECT count(*)::bigint FROM executions WHERE tenant_id = $1 AND created_at >= $2;

-- name: CountActiveExecutions :one
-- Queued + running rows; backs the per-tenant concurrent submit quota.
SELECT count(*)::bigint FROM executions WHERE tenant_id = $1 AND status IN ('queued', 'running');
