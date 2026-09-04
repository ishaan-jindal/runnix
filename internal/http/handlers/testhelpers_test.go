package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcnats "github.com/testcontainers/testcontainers-go/modules/nats"
	tcpg "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
)

const testJWTSecret = "integration-test-secret-min-32-bytes!!"

// testPool boots postgres:16-alpine and applies migrations via store.Migrate.
// It runs Migrate twice to prove idempotency.
// Skips when Docker is unavailable so unit runs stay green everywhere.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ctr, err := tcpg.Run(ctx, "postgres:16",
		tcpg.WithDatabase("runnix_test"),
		tcpg.WithUsername("runnix"),
		tcpg.WithPassword("runnix"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2),
			wait.ForListeningPort("5432/tcp"),
		),
	)
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = ctr.Terminate(ctx)
	})

	connStr, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(ctx, connStr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	// Migrations must be idempotent (gateway replicas all run them).
	if err := store.Migrate(ctx, pool); err != nil {
		t.Fatalf("second migrate failed: %v", err)
	}
	return pool
}

// testNATS boots nats:2-alpine with JetStream, ensures the runnix streams,
// and returns a connected client. Skips when Docker is unavailable.
func testNATS(t *testing.T) *rnats.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	ctr, err := tcnats.Run(ctx, "nats:2",
		testcontainers.WithCmd("-js"),
		testcontainers.WithWaitStrategy(wait.ForListeningPort("4222/tcp")),
	)
	if err != nil {
		t.Skipf("docker unavailable, skipping integration test: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_ = ctr.Terminate(ctx)
	})

	url, err := ctr.ConnectionString(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, err := rnats.Connect(url)
	if err != nil {
		t.Fatalf("connect nats: %v", err)
	}
	t.Cleanup(c.Close)
	if err := c.EnsureStreams(ctx); err != nil {
		t.Fatalf("ensure streams: %v", err)
	}
	return c
}

type sessionJSON struct {
	User struct {
		ID       string `json:"id"`
		Username string `json:"username"`
		Email    string `json:"email"`
	} `json:"user"`
	Tenants []struct {
		ID   string `json:"id"`
		Slug string `json:"slug"`
		Role string `json:"role"`
	} `json:"tenants"`
	AccessToken  string `json:"accessToken"`
	RefreshToken string `json:"refreshToken"`
}

// doJSON performs one request against h and returns status + raw body.
func doJSON(t *testing.T, h http.Handler, method, path string, headers map[string]string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// postJSON posts a body and decodes an auth session envelope.
func postJSON(t *testing.T, h http.Handler, path string, body any) (int, sessionJSON) {
	t.Helper()
	code, raw := doJSON(t, h, http.MethodPost, path, nil, body)
	var sess sessionJSON
	_ = json.Unmarshal(raw, &sess)
	return code, sess
}

// decodeRaw unmarshals a response body in tests.
func decodeRaw(t *testing.T, raw []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode response %s: %v", raw, err)
	}
}
