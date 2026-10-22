-- Reaper sweeps. Both run on status + a timestamp, but the only index on
-- executions is (tenant_id, created_at DESC), so each sweep is a sequential
-- scan. Partial indexes match the predicates exactly and stay small: only
-- in-flight rows are "queued"/"running".
-- Non-CONCURRENT is deliberate: migrations run inside a transaction, where
-- CREATE INDEX CONCURRENTLY is not allowed.

CREATE INDEX IF NOT EXISTS idx_executions_running_updated
  ON executions (updated_at) WHERE status = 'running';

CREATE INDEX IF NOT EXISTS idx_executions_queued_created
  ON executions (created_at) WHERE status = 'queued';
