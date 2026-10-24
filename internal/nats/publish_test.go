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

func TestSubmitMessageCarriesReferencesOnly(t *testing.T) {
	raw, err := json.Marshal(SubmitMessage{ExecutionID: "e", TenantID: "t", Language: "python"})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"source", "stdin", "timeout_s", "webhook_url"} {
		if _, ok := m[key]; ok {
			t.Fatalf("%s must not travel on the queue: %s", key, raw)
		}
	}
	if len(m) != 3 {
		t.Fatalf("submit message = %s, want exactly execution_id/tenant_id/language", raw)
	}
}
