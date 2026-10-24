-- Execution actor attribution (who submitted each run).
--
-- Both columns are nullable because executions created before this migration
-- have no actor to backfill, and development mode (no JWT_SECRET) submits
-- without an authenticated identity.
-- ON DELETE SET NULL keeps execution history when a user or API key is
-- removed: the run still belongs to its tenant and must stay readable, and a
-- deletion must never cascade into (or be blocked by) past executions. Losing
-- the actor link is the accepted trade-off; the columns are audit metadata,
-- not ownership.
ALTER TABLE executions
  ADD COLUMN IF NOT EXISTS submitted_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  ADD COLUMN IF NOT EXISTS submitted_by_api_key_id TEXT REFERENCES api_keys(id) ON DELETE SET NULL;

-- No index on submitted_by_user_id yet: no query reads it. Add one together
-- with the actor-scoped query that needs it, not before — an unused index is a
-- write cost on every submit.
