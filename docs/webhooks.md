# Webhooks

When an execution reaches a terminal state (`succeeded`, `failed`, `timeout`),
the dispatcher POSTs a signed JSON event to the submission's `webhook_url`.
Set `WEBHOOK_SIGNING_SECRET` (same value for gateway and dispatcher) to enable
webhooks; without it, submissions carrying `webhook_url` are rejected with
`503` and the dispatcher signs nothing.

## Payload

`Content-Type: application/json`:

```json
{
  "event": "execution.completed",
  "execution_id": "…",
  "tenant_id": "…",
  "status": "succeeded",
  "exit_code": 0,
  "duration_ms": 1234,
  "finished_at": "2026-09-04T12:00:00.123456789Z"
}
```

## Verifying signatures

Every delivery carries:

- `X-Runnix-Event: execution.completed`
- `X-Runnix-Timestamp: <unix seconds>`
- `X-Runnix-Signature: sha256=<hex>`

The signature is HMAC-SHA256 over `"<timestamp>.<body>"` keyed with the
signing secret, hex-encoded. Recompute it over the **raw request body** and
compare with a constant-time comparison; reject deliveries whose timestamp is
older than a few minutes to bound replays.

Python example:

```python
import hashlib, hmac

def verify(secret: bytes, timestamp: str, body: bytes, signature: str) -> bool:
    expected = hmac.new(secret, timestamp.encode() + b"." + body, hashlib.sha256)
    return hmac.compare_digest("sha256=" + expected.hexdigest(), signature)
```

## Delivery guarantees

- 3 attempts with 1s/5s backoff. Retried on transport errors, `5xx`, and
  `429`; any `2xx` counts as delivered. Other statuses abort immediately and
  redirects are never followed.
- Delivery is best-effort: a webhook that never succeeds is logged by the
  dispatcher and the authoritative result remains `GET /executions/:id`.
- URLs must be http(s) and resolve to public addresses — loopback, private
  ranges, and link-local (cloud metadata) are refused at submit and delivery
  time. `WEBHOOK_ALLOW_PRIVATE=true` lifts this for local development.
