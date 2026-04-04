package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ApprovalHandler handles approval decisions via REST.
type ApprovalHandler struct {
	service ports.ApprovalService
}

func NewApprovalHandler(service ports.ApprovalService) *ApprovalHandler {
	return &ApprovalHandler{service: service}
}

type ApprovalDecisionRequest struct {
	Scope   string `json:"scope"`
	Pattern string `json:"pattern,omitempty"`
}

// Decide handles POST /api/approvals/{id}/decide
func (h *ApprovalHandler) Decide(w http.ResponseWriter, r *http.Request) {
	// Extract approval ID from path: /api/approvals/{id}/decide
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) < 4 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}
	idStr := parts[len(parts)-2] // second to last segment
	requestID, err := uuid.Parse(idStr)
	if err != nil {
		http.Error(w, "invalid approval ID", http.StatusBadRequest)
		return
	}

	var req ApprovalDecisionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	grant := domain.ApprovalGrant{
		Scope:   domain.GrantScope(req.Scope),
		Pattern: req.Pattern,
	}

	if err := h.service.Decide(r.Context(), requestID, grant); err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type:   "https://cordon.dev/problems/approval-failed",
			Title:  "Decision Failed",
			Status: 500,
			Detail: err.Error(),
			Code:   "approval_failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"status":"accepted"}`))
}
