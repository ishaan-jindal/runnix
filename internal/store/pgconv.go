package store

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// UUIDToPg converts a google UUID to a pgtype UUID for query params.
func UUIDToPg(id uuid.UUID) pgtype.UUID {
	return pgtype.UUID{Bytes: id, Valid: true}
}

// PgToUUID converts a pgtype UUID from query results to a google UUID.
func PgToUUID(u pgtype.UUID) uuid.UUID {
	return uuid.UUID(u.Bytes)
}

// PgToString renders a pgtype UUID for JWT claims and JSON responses.
func PgToString(u pgtype.UUID) string {
	return PgToUUID(u).String()
}

// ParsePg parses a UUID string into a pgtype UUID for query params.
func ParsePg(s string) (pgtype.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return pgtype.UUID{}, err
	}
	return UUIDToPg(id), nil
}
