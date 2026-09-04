-- Execution submit columns (promised by the API but missing from 0001).
-- timeout_s is validated at the gateway (default 2, clamp 1-60).
-- webhook_url is optional; gateway validates http(s) now, full SSRF
-- blocklist deferred: ssrf-guard.

ALTER TABLE executions ADD COLUMN IF NOT EXISTS timeout_s INT NOT NULL DEFAULT 2;
ALTER TABLE executions ADD COLUMN IF NOT EXISTS webhook_url TEXT;
