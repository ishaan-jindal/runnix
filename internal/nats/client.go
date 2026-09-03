// Package nats defines JetStream subject conventions and a stub client.
// Real publish/subscribe is deferred: nats-publish; the dispatcher
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

// Client is a thin wrapper reserved for publish/subscribe logic (deferred: nats-publish).
type Client struct {
	Conn *natsgo.Conn
}

// Connect dials NATS (used by dispatcher stub health check; gateway defers use).
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
