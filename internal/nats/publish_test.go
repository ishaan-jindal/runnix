package nats

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSubmitMessageRoundTrip(t *testing.T) {
	in := SubmitMessage{
		ExecutionID: "exec-1",
		TenantID:    "tenant-1",
		Language:    "python",
		Source:      "print('hi')",
		Stdin:       "in",
		TimeoutS:    5,
		WebhookURL:  "https://example.com/hook",
	}
	raw, err := json.Marshal(in)
	if err != nil {
		t.Fatal(err)
	}
	var out SubmitMessage
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(in, out) {
		t.Fatalf("round trip = %+v, want %+v", out, in)
	}
}

func TestSubmitMessageOmitsEmptyWebhook(t *testing.T) {
	raw, err := json.Marshal(SubmitMessage{ExecutionID: "e"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["webhook_url"]; ok {
		t.Fatalf("webhook_url should be omitted when empty: %s", raw)
	}
}
