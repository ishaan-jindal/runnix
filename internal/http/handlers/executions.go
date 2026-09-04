package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/ishaan-jindal/runnix/internal/executions"
	"github.com/ishaan-jindal/runnix/internal/http/middleware"
	"github.com/ishaan-jindal/runnix/internal/store"
	"github.com/ishaan-jindal/runnix/internal/store/storedb"
	"github.com/ishaan-jindal/runnix/internal/webhooks"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	rnats "github.com/ishaan-jindal/runnix/internal/nats"
)

// Execution submit bounds (also documented in api/openapi.yaml).
const (
	DefaultTimeoutS = 2
	MinTimeoutS     = 1
	MaxTimeoutS     = 60
	// MaxSourceBytes caps source+stdin well under the 1 MiB body cap.
	MaxSourceBytes  = 100 * 1024
	DefaultListSize = 20
	MaxListSize     = 100
)

// ExecutionStore is the storedb surface execution handlers need.
// *storedb.Queries satisfies it; unit tests supply fakes.
type ExecutionStore interface {
	CreateExecution(ctx context.Context, arg storedb.CreateExecutionParams) (storedb.CreateExecutionRow, error)
	GetExecution(ctx context.Context, arg storedb.GetExecutionParams) (storedb.GetExecutionRow, error)
	ListExecutions(ctx context.Context, arg storedb.ListExecutionsParams) ([]storedb.ListExecutionsRow, error)
	SetExecutionFailed(ctx context.Context, arg storedb.SetExecutionFailedParams) error
}

// ExecutionsHandler serves create/list/get against Postgres and publishes
// new executions to JetStream.
type ExecutionsHandler struct {
	Store     ExecutionStore
	Publisher rnats.Publisher
}

type createExecutionRequest struct {
	TenantID   string `json:"tenant_id"`
	Language   string `json:"language"`
	Source     string `json:"source"`
	Stdin      string `json:"stdin"`
	TimeoutS   int    `json:"timeout_s"`
	WebhookURL string `json:"webhook_url"`
}

type executionJSON struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id,omitempty"`
	Language  string `json:"language"`
	Status    string `json:"status"`
	Source    string `json:"source,omitempty"`
	Stdin     string `json:"stdin,omitempty"`
	Stdout    string `json:"stdout,omitempty"`
	Stderr    string `json:"stderr,omitempty"`
	ExitCode  *int   `json:"exit_code"`
	TimeoutS  *int   `json:"timeout_s,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// requestTenant resolves the tenant scope: the validated context value when
// auth middleware is mounted, else the raw header (development without
// JWT_SECRET trusts it by design).
func requestTenant(r *http.Request) string {
	if t := middleware.TenantIDFrom(r.Context()); t != "" {
		return t
	}
	return r.Header.Get("X-Tenant-ID")
}

// Create persists an execution as queued and publishes it to the submit
// stream.
//
//	POST /executions -> 202 {id, status: queued}; 400, 403, 502.
func (h *ExecutionsHandler) Create(w http.ResponseWriter, r *http.Request) {
	var req createExecutionRequest
	if !decodeBody(w, r, &req) {
		return
	}
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	if req.TenantID != "" && req.TenantID != tenant {
		writeErr(w, http.StatusForbidden, "tenant_id does not match X-Tenant-ID")
		return
	}
	req.Language = strings.ToLower(strings.TrimSpace(req.Language))
	if !executions.ValidLanguage(req.Language) {
		writeErr(w, http.StatusBadRequest, "unsupported language")
		return
	}
	if strings.TrimSpace(req.Source) == "" {
		writeErr(w, http.StatusBadRequest, "source is required")
		return
	}
	if len(req.Source) > MaxSourceBytes || len(req.Stdin) > MaxSourceBytes {
		writeErr(w, http.StatusBadRequest, "source or stdin too large")
		return
	}
	if req.TimeoutS == 0 {
		req.TimeoutS = DefaultTimeoutS
	}
	if req.TimeoutS < MinTimeoutS || req.TimeoutS > MaxTimeoutS {
		writeErr(w, http.StatusBadRequest, "timeout_s must be 1-60")
		return
	}
	if req.WebhookURL != "" {
		if err := webhooks.ValidateCallbackURL(req.WebhookURL); err != nil {
			writeErr(w, http.StatusBadRequest, "invalid webhook_url")
			return
		}
	}
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid X-Tenant-ID")
		return
	}

	webhook := pgtype.Text{}
	if req.WebhookURL != "" {
		webhook = pgtype.Text{String: req.WebhookURL, Valid: true}
	}
	row, err := h.Store.CreateExecution(r.Context(), storedb.CreateExecutionParams{
		TenantID:   tenantUUID,
		Language:   req.Language,
		Source:     req.Source,
		Stdin:      req.Stdin,
		TimeoutS:   int32(req.TimeoutS),
		WebhookUrl: webhook,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not create execution")
		return
	}
	id := store.PgToString(row.ID)

	if err := h.Publisher.PublishSubmit(r.Context(), rnats.SubmitMessage{
		ExecutionID: id,
		TenantID:    tenant,
		Language:    req.Language,
		Source:      req.Source,
		Stdin:       req.Stdin,
		TimeoutS:    req.TimeoutS,
		WebhookURL:  req.WebhookURL,
	}); err != nil {
		// The row exists but nothing will ever run it: fail it loudly
		// instead of leaving a ghost-queued execution.
		_ = h.Store.SetExecutionFailed(r.Context(), storedb.SetExecutionFailedParams{
			ID:     row.ID,
			Stderr: "submit queue unavailable",
		})
		writeErr(w, http.StatusBadGateway, "submit queue unavailable, execution failed")
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"id":         id,
		"status":     row.Status,
		"created_at": row.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

// List returns a page of execution summaries for the tenant.
//
//	GET /executions?limit=&offset= -> 200
func (h *ExecutionsHandler) List(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid X-Tenant-ID")
		return
	}
	limit, offset, ok := pageParams(w, r)
	if !ok {
		return
	}

	rows, err := h.Store.ListExecutions(r.Context(), storedb.ListExecutionsParams{
		TenantID: tenantUUID,
		Limit:    limit,
		Offset:   offset,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not list executions")
		return
	}
	items := make([]executionJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, executionJSON{
			ID:        store.PgToString(row.ID),
			Language:  row.Language,
			Status:    row.Status,
			CreatedAt: row.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
			UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"executions": items,
		"limit":      limit,
		"offset":     offset,
	})
}

// Get returns one execution, always tenant-scoped.
//
//	GET /executions/{id} -> 200, 404 when missing or owned by another tenant.
func (h *ExecutionsHandler) Get(w http.ResponseWriter, r *http.Request) {
	tenant := requestTenant(r)
	if tenant == "" {
		writeErr(w, http.StatusBadRequest, "X-Tenant-ID is required")
		return
	}
	tenantUUID, err := store.ParsePg(tenant)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid X-Tenant-ID")
		return
	}
	id, err := store.ParsePg(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid execution id")
		return
	}

	row, err := h.Store.GetExecution(r.Context(), storedb.GetExecutionParams{ID: id, TenantID: tenantUUID})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeErr(w, http.StatusNotFound, "execution not found")
			return
		}
		writeErr(w, http.StatusInternalServerError, "could not get execution")
		return
	}

	var exitCode *int
	if row.ExitCode.Valid {
		v := int(row.ExitCode.Int32)
		exitCode = &v
	}
	writeJSON(w, http.StatusOK, executionJSON{
		ID:        store.PgToString(row.ID),
		TenantID:  store.PgToString(row.TenantID),
		Language:  row.Language,
		Status:    row.Status,
		Source:    row.Source,
		Stdin:     row.Stdin,
		Stdout:    row.Stdout,
		Stderr:    row.Stderr,
		ExitCode:  exitCode,
		CreatedAt: row.CreatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
		UpdatedAt: row.UpdatedAt.Time.Format("2006-01-02T15:04:05.999999999Z07:00"),
	})
}

// pageParams parses ?limit=&offset= with defaults and bounds.
func pageParams(w http.ResponseWriter, r *http.Request) (limit, offset int32, ok bool) {
	q := r.URL.Query()
	limit = DefaultListSize
	if s := q.Get("limit"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 {
			writeErr(w, http.StatusBadRequest, "invalid limit")
			return 0, 0, false
		}
		if n > MaxListSize {
			n = MaxListSize
		}
		limit = int32(n)
	}
	if s := q.Get("offset"); s != "" {
		n, err := strconv.Atoi(s)
		if err != nil || n < 0 {
			writeErr(w, http.StatusBadRequest, "invalid offset")
			return 0, 0, false
		}
		offset = int32(n)
	}
	return limit, offset, true
}
