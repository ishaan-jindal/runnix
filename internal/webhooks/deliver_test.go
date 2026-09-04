package webhooks

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func shrinkRetries(t *testing.T) {
	t.Helper()
	orig := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond}
	t.Cleanup(func() { retryDelays = orig })
}

func testEvent() ExecutionEvent {
	return ExecutionEvent{
		Event:       EventExecutionCompleted,
		ExecutionID: "22222222-2222-2222-2222-222222222222",
		TenantID:    "11111111-1111-1111-1111-111111111111",
		Status:      "succeeded",
		DurationMS:  42,
		FinishedAt:  time.Now().UTC().Format(time.RFC3339Nano),
	}
}

func TestSignContract(t *testing.T) {
	// The signature must be hex(HMAC-SHA256(secret, "<timestamp>.<body>")).
	secret := "hook-secret"
	ts := int64(1700000000)
	body := []byte(`{"a":1}`)
	got := Sign(secret, ts, body)
	if !strings.HasPrefix(got, "sha256=") {
		t.Fatalf("Sign = %q, want sha256= prefix", got)
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(ts, 10) + "."))
	mac.Write(body)
	if want := "sha256=" + hex.EncodeToString(mac.Sum(nil)); got != want {
		t.Fatalf("Sign = %q, want %q", got, want)
	}
	// Different timestamp, secret, or body must change the signature.
	if Sign(secret, ts+1, body) == got || Sign("other", ts, body) == got || Sign(secret, ts, []byte(`x`)) == got {
		t.Fatal("signature does not depend on timestamp/secret/body")
	}
}

func TestDelivererSuccess(t *testing.T) {
	shrinkRetries(t)
	var gotReq struct {
		method    string
		event     string
		timestamp string
		signature string
		body      []byte
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotReq.method = r.Method
		gotReq.event = r.Header.Get(EventHeader)
		gotReq.timestamp = r.Header.Get(TimestampHeader)
		gotReq.signature = r.Header.Get(SignatureHeader)
		gotReq.body, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "hook-secret", AllowPrivate: true}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err != nil {
		t.Fatalf("Deliver = %v", err)
	}
	if gotReq.method != http.MethodPost {
		t.Fatalf("method = %q, want POST", gotReq.method)
	}
	if gotReq.event != EventExecutionCompleted {
		t.Fatalf("event header = %q", gotReq.event)
	}
	ts, err := strconv.ParseInt(gotReq.timestamp, 10, 64)
	if err != nil {
		t.Fatalf("timestamp header %q: %v", gotReq.timestamp, err)
	}
	if want := Sign("hook-secret", ts, gotReq.body); gotReq.signature != want {
		t.Fatalf("signature = %q, want %q", gotReq.signature, want)
	}
	var evt ExecutionEvent
	if err := json.Unmarshal(gotReq.body, &evt); err != nil {
		t.Fatalf("payload not valid JSON: %v", err)
	}
	if evt.Status != "succeeded" || evt.ExecutionID != "22222222-2222-2222-2222-222222222222" {
		t.Fatalf("payload = %+v", evt)
	}
}

func TestDelivererRetries5xx(t *testing.T) {
	shrinkRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "s", AllowPrivate: true}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err != nil {
		t.Fatalf("Deliver = %v, want success after retries", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestDelivererRetries429(t *testing.T) {
	shrinkRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "s", AllowPrivate: true}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err == nil {
		t.Fatal("Deliver = nil, want exhaustion error")
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3 (429 must retry)", calls)
	}
}

func TestDelivererNoRetry4xx(t *testing.T) {
	shrinkRetries(t)
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "s", AllowPrivate: true}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err == nil {
		t.Fatal("Deliver = nil, want error for 400")
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (4xx must not retry)", calls)
	}
}

func TestDelivererDoesNotFollowRedirects(t *testing.T) {
	shrinkRetries(t)
	redirected := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusFound)
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "s", AllowPrivate: true}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err == nil {
		t.Fatal("Deliver = nil, want error for 302")
	}
	if redirected != 0 {
		t.Fatalf("redirect target hit %d times, want 0", redirected)
	}
}

func TestDelivererRejectsPrivateURL(t *testing.T) {
	// AllowPrivate is false: the loopback receiver must be refused before
	// any HTTP happens.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Error("receiver should never be called")
	}))
	defer srv.Close()

	d := &Deliverer{Secret: "s"}
	if err := d.Deliver(context.Background(), srv.URL, testEvent()); err == nil {
		t.Fatal("Deliver = nil, want SSRF rejection")
	}
}

func TestDelivererRequiresSecret(t *testing.T) {
	d := &Deliverer{}
	if err := d.Deliver(context.Background(), "https://example.com/hook", testEvent()); err == nil {
		t.Fatal("Deliver = nil, want secret error")
	}
}
