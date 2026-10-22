package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/jackc/pgx/v5/pgtype"
)

// TestExecutionActorAttribution proves submits record the authenticated actor
// in executions: the user for both auth paths, plus the key id only when an
// API key authenticated the request.
func TestExecutionActorAttribution(t *testing.T) {
	pool := testPool(t)

	authH := &AuthHandler{Pool: pool, JWTSecret: testJWTSecret}
	apiH := NewAPIKeysHandler(pool)
	execH := &ExecutionsHandler{Store: storedb.New(pool), Publisher: &fakePublisher{}}
	keyAuth := middleware.RequireAuthWithAPIKeys(testJWTSecret, middleware.DBChecker(storedb.New(pool)), apiH.CheckAPIKey)

	m := http.NewServeMux()
	m.HandleFunc("POST /auth/register", authH.Register)
	m.Handle("POST /tenants/{id}/api-keys", keyAuth(http.HandlerFunc(apiH.Create)))
	m.Handle("POST /executions", keyAuth(http.HandlerFunc(execH.Create)))

	sess := registerSession(t, m, "ActorUser", "actor@example.com")
	tenant := sess.Tenants[0].ID

	// Mint a key with the JWT so the API-key case has something to present.
	code, raw := doJSON(t, m, http.MethodPost, "/tenants/"+tenant+"/api-keys",
		authHeaders(sess, tenant), map[string]string{"name": "actor key"})
	if code != http.StatusCreated {
		t.Fatalf("create key = %d (%s), want 201", code, raw)
	}
	var key struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	decodeRaw(t, raw, &key)

	cases := []struct {
		name       string
		headers    map[string]string
		wantAPIKey string
	}{
		{
			name:       "jwt records user only",
			headers:    authHeaders(sess, tenant),
			wantAPIKey: "",
		},
		{
			name:       "api key records user and key",
			headers:    map[string]string{"Authorization": "Bearer " + key.Key, "X-Tenant-ID": tenant},
			wantAPIKey: key.ID,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, raw := doJSON(t, m, http.MethodPost, "/executions", tc.headers, map[string]any{
				"language": "python",
				"source":   "print(1)",
			})
			if code != http.StatusAccepted {
				t.Fatalf("submit = %d (%s), want 202", code, raw)
			}
			var created struct {
				ID string `json:"id"`
			}
			decodeRaw(t, raw, &created)
			execID, err := store.ParsePg(created.ID)
			if err != nil {
				t.Fatalf("execution id %q: %v", created.ID, err)
			}

			var userID pgtype.UUID
			var keyID pgtype.Text
			if err := pool.QueryRow(context.Background(),
				`SELECT submitted_by_user_id, submitted_by_api_key_id FROM executions WHERE id = $1`,
				execID).Scan(&userID, &keyID); err != nil {
				t.Fatalf("read actor: %v", err)
			}
			if got := store.PgToString(userID); got != sess.User.ID {
				t.Fatalf("submitted_by_user_id = %q, want %q", got, sess.User.ID)
			}
			gotKey := ""
			if keyID.Valid {
				gotKey = keyID.String
			}
			if gotKey != tc.wantAPIKey {
				t.Fatalf("submitted_by_api_key_id = %q, want %q", gotKey, tc.wantAPIKey)
			}
		})
	}
}
