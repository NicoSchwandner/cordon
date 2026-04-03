package handlers

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
)

type WorkspaceHandler struct {
	service ports.WorkspaceService
}

func NewWorkspaceHandler(service ports.WorkspaceService) *WorkspaceHandler {
	return &WorkspaceHandler{service: service}
}

type CreateWorkspaceRequest struct {
	Name             string `json:"name"`
	Repo             string `json:"repo"`
	BaseBranch       string `json:"base_branch"`
	Branch           string `json:"branch"`
	DevcontainerPath string `json:"devcontainer_path"`
	CPU              int    `json:"cpu"`
	MemoryMB         int    `json:"memory_mb"`
}

type WorkspaceResponse struct {
	ID        string `json:"id"`
	TenantID  string `json:"tenant_id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Repo      string `json:"repo,omitempty"`
	Branch    string `json:"branch,omitempty"`
	CreatedAt string `json:"created_at"`
	ExpiresAt string `json:"expires_at"`
}

func toWorkspaceResponse(ws domain.Workspace) WorkspaceResponse {
	return WorkspaceResponse{
		ID:        ws.ID.String(),
		TenantID:  ws.TenantID.String(),
		Name:      ws.Name,
		Status:    string(ws.Status),
		Repo:      ws.Config.Repo,
		Branch:    ws.Config.Branch,
		CreatedAt: ws.CreatedAt.Format(time.RFC3339),
		ExpiresAt: ws.ExpiresAt.Format(time.RFC3339),
	}
}

func (h *WorkspaceHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/not-implemented", Title: "Not Implemented",
			Status: 501, Detail: "Workspace orchestrator not configured", Code: "not_implemented",
		})
		return
	}

	var req CreateWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	name := req.Name
	if name == "" {
		name = "workspace"
	}
	config := domain.WorkspaceConfig{
		Name:             name,
		DevcontainerPath: req.DevcontainerPath,
		CPU:              max(req.CPU, 1),
		MemoryMB:         max(req.MemoryMB, 512),
		IdleTimeout:      15 * time.Minute,
		MaxLifetime:      24 * time.Hour,
		Repo:             req.Repo,
		BaseBranch:       req.BaseBranch,
		Branch:           req.Branch,
	}

	ws, err := h.service.Create(r.Context(), tenantID, config)
	if err != nil {
		log.Printf("[workspace] create failed: %v", err)
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/workspace-create-failed", Title: "Create Failed",
			Status: 500, Detail: err.Error(), Code: "workspace_create_failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(toWorkspaceResponse(ws))
}

func (h *WorkspaceHandler) List(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]WorkspaceResponse{})
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	workspaces, err := h.service.List(r.Context(), tenantID)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/workspace-list-failed", Title: "List Failed",
			Status: 500, Detail: err.Error(), Code: "workspace_list_failed",
		})
		return
	}

	resp := make([]WorkspaceResponse, len(workspaces))
	for i, ws := range workspaces {
		resp[i] = toWorkspaceResponse(ws)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *WorkspaceHandler) Get(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.NotFound(w, r)
		return
	}

	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	ws, err := h.service.Get(r.Context(), tenantID, wsID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(toWorkspaceResponse(ws))
}

func (h *WorkspaceHandler) Delete(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.NotFound(w, r)
		return
	}

	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	if err := h.service.Destroy(r.Context(), tenantID, wsID); err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/workspace-destroy-failed", Title: "Destroy Failed",
			Status: 500, Detail: err.Error(), Code: "workspace_destroy_failed",
		})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *WorkspaceHandler) Action(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.NotFound(w, r)
		return
	}

	// Parse: /api/workspaces/{id}/{action}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/workspaces/"), "/")
	if len(parts) < 2 {
		http.Error(w, "invalid path", http.StatusBadRequest)
		return
	}

	wsID, err := uuid.Parse(parts[0])
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	action := parts[1]

	switch action {
	case "suspend":
		err = h.service.Suspend(r.Context(), tenantID, wsID)
	case "resume":
		err = h.service.Resume(r.Context(), tenantID, wsID)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
		return
	}

	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/workspace-action-failed", Title: "Action Failed",
			Status: 500, Detail: err.Error(), Code: "workspace_action_failed",
		})
		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"ok"}`))
}

func extractWorkspaceID(path string) (uuid.UUID, error) {
	// /api/workspaces/{id}
	parts := strings.Split(strings.TrimPrefix(path, "/api/workspaces/"), "/")
	if len(parts) == 0 {
		return uuid.Nil, fmt.Errorf("missing workspace ID")
	}
	return uuid.Parse(parts[0])
}
