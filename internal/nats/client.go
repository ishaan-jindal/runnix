// Package nats defines JetStream subject conventions and the client.
// The gateway publishes submit messages (see publish.go); the dispatcher
// consumer groups per language are documented here so both sides agree.
package nats

import (
	"fmt"

	natsgo "github.com/nats-io/nats.go"
)

// Streams.
const (
	StreamSubmit = "EXEC_SUBMIT"
	StreamResult = "EXEC_RESULT"
)

// SubjectForSubmit returns exec.submit.<lang>.
func SubjectForSubmit(lang string) string {
	return fmt.Sprintf("exec.submit.%s", lang)
}

// SubjectForResult returns exec.result.<executionID>.
func SubjectForResult(executionID string) string {
	return fmt.Sprintf("exec.result.%s", executionID)
}

// Client wraps a NATS connection plus its JetStream streams.
type Client struct {
	Conn *natsgo.Conn
}

// Connect dials NATS (used by gateway and dispatcher; callers degrade when
// the dial fails).
func Connect(url string) (*Client, error) {
	nc, err := natsgo.Connect(url)
	if err != nil {
		return nil, err
	}
	return &Client{Conn: nc}, nil
}

// Close drains the connection.
func (c *Client) Close() {
	if c != nil && c.Conn != nil {
		_ = c.Conn.Drain()
	}
}
