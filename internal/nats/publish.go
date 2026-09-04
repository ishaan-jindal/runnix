// Publish support for the runnix JetStream streams.
//
// The gateway publishes one message per execution to exec.submit.<lang>.
// The dispatcher (execution loop deferred to a later slice) consumes them;
// each publish is at-least-once, so the consumer dedupes by execution_id.
package nats

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/nats-io/nats.go/jetstream"
)

// SubmitMessage is the payload published to exec.submit.<lang> when an
// execution is created. Both gateway and dispatcher must agree on this shape.
type SubmitMessage struct {
	ExecutionID string `json:"execution_id"`
	TenantID    string `json:"tenant_id"`
	Language    string `json:"language"`
	Source      string `json:"source"`
	Stdin       string `json:"stdin"`
	TimeoutS    int    `json:"timeout_s"`
	WebhookURL  string `json:"webhook_url,omitempty"`
}

// Publisher is the JetStream surface the gateway needs. *Client satisfies it;
// tests supply fakes.
type Publisher interface {
	// EnsureStreams creates EXEC_SUBMIT/EXEC_RESULT when missing.
	EnsureStreams(ctx context.Context) error
	// PublishSubmit publishes msg to exec.submit.<lang>.
	PublishSubmit(ctx context.Context, msg SubmitMessage) error
}

// JS returns the JetStream handle for the connection.
func (c *Client) JS() (jetstream.JetStream, error) {
	return jetstream.New(c.Conn)
}

// EnsureStreams creates the submit/result streams idempotently.
// Limits retention (server default); per-message TTL and consumers
// are the dispatcher's concern.
func (c *Client) EnsureStreams(ctx context.Context) error {
	js, err := c.JS()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	for name, subjects := range map[string][]string{
		StreamSubmit: {"exec.submit.>"},
		StreamResult: {"exec.result.>"},
	} {
		if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
			Name:        name,
			Subjects:    subjects,
			Retention:   jetstream.LimitsPolicy,
			MaxMsgs:     100_000,
			Storage:     jetstream.FileStorage,
			AllowDirect: true,
		}); err != nil {
			return fmt.Errorf("stream %s: %w", name, err)
		}
	}
	return nil
}

// PublishSubmit publishes a SubmitMessage to exec.submit.<lang>.
func (c *Client) PublishSubmit(ctx context.Context, msg SubmitMessage) error {
	js, err := c.JS()
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("encode submit message: %w", err)
	}
	if _, err := js.Publish(ctx, SubjectForSubmit(msg.Language), raw); err != nil {
		return fmt.Errorf("publish submit: %w", err)
	}
	return nil
}
