package store

import (
	"testing"

	"github.com/google/uuid"
)

func TestUUIDRoundTrip(t *testing.T) {
	id := uuid.New()
	pg := UUIDToPg(id)
	if !pg.Valid {
		t.Fatal("expected valid pgtype UUID")
	}
	if got := PgToUUID(pg); got != id {
		t.Fatalf("round trip = %s, want %s", got, id)
	}
	if got := PgToString(pg); got != id.String() {
		t.Fatalf("string = %s, want %s", got, id)
	}
	if _, err := ParsePg("not-a-uuid"); err == nil {
		t.Fatal("expected parse error")
	}
}
