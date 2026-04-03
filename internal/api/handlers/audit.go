package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"github.com/nicobistolfi/cordon/internal/application/audit"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// AuditHandler serves audit log queries.
type AuditHandler struct {
	service *audit.Service
}

func NewAuditHandler(service *audit.Service) *AuditHandler {
	return &AuditHandler{service: service}
}

// AuditEntryResponse is the JSON representation of an audit entry.
type AuditEntryResponse struct {
	ID          string `json:"id"`
	TenantID    string `json:"tenant_id"`
	WorkspaceID string `json:"workspace_id"`
	Timestamp   string `json:"timestamp"`
	Tier        int    `json:"tier"`
	TierName    string `json:"tier_name"`
	Operation   string `json:"operation"`
	Target      string `json:"target"`
	Caller      string `json:"caller"`
	Decision    string `json:"decision"`
	DurationMs  int64  `json:"duration_ms"`
	Detail      string `json:"detail"`
}

func (h *AuditHandler) Query(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())

	filter := ports.AuditFilter{
		TenantID: tenantID,
		Limit:    100,
	}

	// Parse query params
	if ws := r.URL.Query().Get("workspace_id"); ws != "" {
		id, err := uuid.Parse(ws)
		if err == nil {
			filter.WorkspaceID = &id
		}
	}
	if tm := r.URL.Query().Get("tier_min"); tm != "" {
		if v, err := strconv.Atoi(tm); err == nil {
			tier := domain.Tier(v)
			filter.TierMin = &tier
		}
	}
	if d := r.URL.Query().Get("decision"); d != "" {
		dec := domain.Decision(d)
		filter.Decision = &dec
	}
	if s := r.URL.Query().Get("since"); s != "" {
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			filter.Since = &t
		}
	}
	if l := r.URL.Query().Get("limit"); l != "" {
		if v, err := strconv.Atoi(l); err == nil && v > 0 && v <= 1000 {
			filter.Limit = v
		}
	}
	if o := r.URL.Query().Get("offset"); o != "" {
		if v, err := strconv.Atoi(o); err == nil && v >= 0 {
			filter.Offset = v
		}
	}

	entries, err := h.service.Query(r.Context(), tenantID, filter)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type:   "https://cordon.dev/problems/query-failed",
			Title:  "Query Failed",
			Status: 500,
			Detail: "Failed to query audit entries",
			Code:   "query_failed",
		})
		return
	}

	resp := make([]AuditEntryResponse, len(entries))
	for i, e := range entries {
		resp[i] = AuditEntryResponse{
			ID:          e.ID.String(),
			TenantID:    e.TenantID.String(),
			WorkspaceID: e.WorkspaceID.String(),
			Timestamp:   e.Timestamp.Format(time.RFC3339Nano),
			Tier:        int(e.Tier),
			TierName:    e.Tier.String(),
			Operation:   e.Operation,
			Target:      e.Target,
			Caller:      e.Caller,
			Decision:    string(e.Decision),
			DurationMs:  e.DurationMs,
			Detail:      e.Detail,
		}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}
