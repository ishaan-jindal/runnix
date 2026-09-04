package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"time"
)

// Event carried on the X-Runnix-Event header.
const EventExecutionCompleted = "execution.completed"

// SignatureHeader / TimestampHeader / EventHeader name the delivery headers
// receivers verify (see docs/webhooks.md).
const (
	SignatureHeader = "X-Runnix-Signature"
	TimestampHeader = "X-Runnix-Timestamp"
	EventHeader     = "X-Runnix-Event"
)

// ExecutionEvent is the JSON payload POSTed to a submission's webhook_url
// when the run reaches a terminal state.
type ExecutionEvent struct {
	Event       string `json:"event"`
	ExecutionID string `json:"execution_id"`
	TenantID    string `json:"tenant_id"`
	Status      string `json:"status"` // succeeded | failed | timeout
	ExitCode    *int   `json:"exit_code"`
	DurationMS  int64  `json:"duration_ms"`
	FinishedAt  string `json:"finished_at"` // RFC3339Nano
}

// retryDelays are the waits between webhook attempts. A var so tests can
// shrink them.
var retryDelays = []time.Duration{time.Second, 5 * time.Second}

// Deliverer POSTs signed ExecutionEvents to submission webhook URLs.
// Delivery is best-effort: Postgres holds the full result, so exhausted
// retries are logged, not retried by the execution pipeline.
type Deliverer struct {
	// Secret keys the HMAC-SHA256 payload signatures. Required.
	Secret string
	// AllowPrivate disables the SSRF blocklist (development/tests only).
	AllowPrivate bool
	// HTTPClient overrides the default client (used by tests).
	HTTPClient *http.Client
	Logger     *log.Logger
}

// Deliver POSTs evt to rawURL with a signature over "<timestamp>.<body>",
// retrying transport errors, 5xx, and 429 with backoff. Other statuses fail
// immediately.
func (d *Deliverer) Deliver(ctx context.Context, rawURL string, evt ExecutionEvent) error {
	if d.Secret == "" {
		return fmt.Errorf("webhook signing secret not configured")
	}
	if err := ValidateCallbackURL(ctx, rawURL, Options{AllowPrivate: d.AllowPrivate}); err != nil {
		return err
	}
	body, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("encode webhook payload: %w", err)
	}

	client := d.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			// Never follow redirects: the target of a redirect would skip
			// URL validation entirely.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		}
	}

	var lastErr error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(retryDelays[attempt-1]):
			}
		}
		retryable, err := d.deliverOnce(ctx, client, rawURL, body)
		if err == nil {
			return nil
		}
		lastErr = err
		if !retryable {
			break
		}
	}
	return lastErr
}

// deliverOnce performs one signed POST. It reports whether the failure is
// worth retrying.
func (d *Deliverer) deliverOnce(ctx context.Context, client *http.Client, rawURL string, body []byte) (bool, error) {
	ts := time.Now().Unix()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(EventHeader, EventExecutionCompleted)
	req.Header.Set(TimestampHeader, strconv.FormatInt(ts, 10))
	req.Header.Set(SignatureHeader, Sign(d.Secret, ts, body))

	resp, err := client.Do(req)
	if err != nil {
		return true, err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return false, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return true, fmt.Errorf("webhook receiver: %s", resp.Status)
	default:
		return false, fmt.Errorf("webhook receiver: %s (not retrying)", resp.Status)
	}
}

// Sign returns the X-Runnix-Signature value for a delivery:
// "sha256=" + hex(HMAC-SHA256(secret, "<timestamp>.<body>")). Receivers
// recompute it over the raw request body (docs/webhooks.md).
func Sign(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = fmt.Fprintf(mac, "%d.", timestamp)
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
