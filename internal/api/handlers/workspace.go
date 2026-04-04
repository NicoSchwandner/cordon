package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/application/progress"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

type WorkspaceHandler struct {
	service  ports.WorkspaceService
	progress *progress.Store
}

func NewWorkspaceHandler(service ports.WorkspaceService, ps *progress.Store) *WorkspaceHandler {
	return &WorkspaceHandler{service: service, progress: ps}
}

type RepoConfigRequest struct {
	URL              string `json:"url"`
	Branch           string `json:"branch,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	DevcontainerPath string `json:"devcontainer_path,omitempty"`
	Primary          bool   `json:"primary,omitempty"`
	ServiceContainer bool   `json:"service_container,omitempty"`
}

type CreateWorkspaceRequest struct {
	Name             string              `json:"name"`
	Mode             string              `json:"mode,omitempty"`
	Org              string              `json:"org,omitempty"`
	Repos            []RepoConfigRequest `json:"repos"`
	DevcontainerPath string              `json:"devcontainer_path"`
	CPU              int                 `json:"cpu"`
	MemoryMB         int                 `json:"memory_mb"`
}

type RepoConfigResponse struct {
	URL              string `json:"url"`
	Branch           string `json:"branch,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	Primary          bool   `json:"primary,omitempty"`
	ServiceContainer bool   `json:"service_container,omitempty"`
}

type InvestigationStateResponse struct {
	CatalogOrg     string   `json:"catalog_org"`
	ShallowRepos   []string `json:"shallow_repos"`
	ActivatedRepos []string `json:"activated_repos"`
}

type WorkspaceResponse struct {
	ID            string                        `json:"id"`
	TenantID      string                        `json:"tenant_id"`
	Name          string                        `json:"name"`
	Status        string                        `json:"status"`
	Mode          string                        `json:"mode,omitempty"`
	SpawnedFrom   string                        `json:"spawned_from,omitempty"`
	Repos         []RepoConfigResponse          `json:"repos"`
	Investigation *InvestigationStateResponse    `json:"investigation,omitempty"`
	CreatedAt     string                        `json:"created_at"`
	ExpiresAt     string                        `json:"expires_at"`
}

func toWorkspaceResponse(ws domain.Workspace) WorkspaceResponse {
	repos := make([]RepoConfigResponse, len(ws.Config.Repos))
	for i, r := range ws.Config.Repos {
		repos[i] = RepoConfigResponse{
			URL:              r.URL,
			Branch:           r.Branch,
			BaseBranch:       r.BaseBranch,
			Primary:          r.Primary,
			ServiceContainer: r.ServiceContainer,
		}
	}
	resp := WorkspaceResponse{
		ID:        ws.ID.String(),
		TenantID:  ws.TenantID.String(),
		Name:      ws.Name,
		Status:    string(ws.Status),
		Mode:      string(ws.Config.Mode),
		Repos:     repos,
		CreatedAt: ws.CreatedAt.Format(time.RFC3339),
		ExpiresAt: ws.ExpiresAt.Format(time.RFC3339),
	}
	if ws.SpawnedFrom != nil {
		resp.SpawnedFrom = ws.SpawnedFrom.String()
	}
	if ws.Config.Investigation != nil {
		resp.Investigation = &InvestigationStateResponse{
			CatalogOrg:     ws.Config.Investigation.CatalogOrg,
			ShallowRepos:   ws.Config.Investigation.ShallowRepos,
			ActivatedRepos: ws.Config.Investigation.ActivatedRepos,
		}
	}
	return resp
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

	// Convert request repos to domain repos
	repos := make([]domain.RepoConfig, len(req.Repos))
	for i, r := range req.Repos {
		repos[i] = domain.RepoConfig{
			URL:              r.URL,
			Branch:           r.Branch,
			BaseBranch:       r.BaseBranch,
			DevcontainerPath: r.DevcontainerPath,
			Primary:          r.Primary,
			ServiceContainer: r.ServiceContainer,
		}
	}

	config := domain.WorkspaceConfig{
		Name:             name,
		Repos:            repos,
		DevcontainerPath: req.DevcontainerPath,
		CPU:              max(req.CPU, 1),
		MemoryMB:         max(req.MemoryMB, 512),
		IdleTimeout:      15 * time.Minute,
		MaxLifetime:      24 * time.Hour,
	}

	// Investigation mode
	if req.Mode == "investigation" {
		if req.Org == "" {
			http.Error(w, "org is required for investigation mode", http.StatusBadRequest)
			return
		}
		config.Mode = domain.WorkspaceModeInvestigation
		config.Investigation = &domain.InvestigationState{CatalogOrg: req.Org}
	}

	// Async creation: investigation workspaces or repo-based workspaces
	if (config.IsInvestigation() || config.HasRepos()) && h.progress != nil {
		wsID := uuid.New()
		config.ID = wsID
		now := time.Now().UTC()

		h.progress.Create(wsID)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
			defer cancel()

			ws, err := h.service.Create(ctx, tenantID, config)
			if err != nil {
				log.Printf("[workspace] create failed: %v", err)
			}
			_ = ws
			h.progress.Complete(wsID, err)
		}()

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		json.NewEncoder(w).Encode(pendingWorkspaceResponse(wsID, tenantID, config, now))
		return
	}

	// Bare workspaces: create synchronously (fast)
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

	wsID, suffix, err := extractWorkspaceIDAndSuffix(r.URL.Path)
	if err != nil || len(suffix) == 0 {
		http.Error(w, "invalid workspace path", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	action := suffix[0]

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

// CreationLogs streams workspace creation progress as Server-Sent Events.
func (h *WorkspaceHandler) CreationLogs(w http.ResponseWriter, r *http.Request) {
	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	ch, ok := h.progress.Subscribe(wsID)
	if !ok {
		// No in-flight creation — send a single done event
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		evt := progress.Event{Step: "done", Message: "Workspace ready!", Done: true}
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	for {
		select {
		case evt, open := <-ch:
			if !open {
				return
			}
			data, _ := json.Marshal(evt)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
			if evt.Done {
				return
			}
		case <-ctx.Done():
			return
		}
	}
}

// ExecRequest represents a command execution request in a workspace.
type ExecRequest struct {
	Repo   string   `json:"repo"`
	Cmd    []string `json:"cmd"`
	Stream bool     `json:"stream"`
}

// ExecResponse contains the result of a command execution.
type ExecResponse struct {
	ExitCode int    `json:"exit_code"`
	Output   string `json:"output"`
}

// ExecInWorkspace executes a command in a workspace, optionally targeting a specific repo's container.
func (h *WorkspaceHandler) ExecInWorkspace(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.NotFound(w, r)
		return
	}

	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	var req ExecRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if len(req.Cmd) == 0 {
		http.Error(w, "cmd is required", http.StatusBadRequest)
		return
	}

	exitCode, output, err := h.service.ExecInRepo(r.Context(), wsID, req.Repo, req.Cmd)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/exec-failed", Title: "Exec Failed",
			Status: 500, Detail: err.Error(), Code: "exec_failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(ExecResponse{
		ExitCode: exitCode,
		Output:   output,
	})
}

type ActivateRepoRequest struct {
	RepoURLs []string `json:"repo_urls"`
	// Backwards compat: single repo
	RepoURL string `json:"repo_url"`
}

// Activate triggers async repo activation in an investigation workspace.
func (h *WorkspaceHandler) Activate(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		http.NotFound(w, r)
		return
	}

	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	var req ActivateRepoRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	// Normalize: support both single repo_url and batch repo_urls
	urls := req.RepoURLs
	if len(urls) == 0 && req.RepoURL != "" {
		urls = []string{req.RepoURL}
	}
	if len(urls) == 0 {
		http.Error(w, "repo_urls or repo_url is required", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())

	// Register progress channel for activation SSE
	if h.progress != nil {
		h.progress.CreateIfAbsent(wsID)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()

		var lastErr error
		for _, repoURL := range urls {
			if err := h.service.ActivateRepo(ctx, tenantID, wsID, repoURL); err != nil {
				log.Printf("[workspace] activate %s failed: %v", repoURL, err)
				lastErr = err
			}
		}
		if h.progress != nil {
			h.progress.Complete(wsID, lastErr)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]any{
		"status":    "activating",
		"repo_urls": urls,
	})
}

func pendingWorkspaceResponse(wsID, tenantID uuid.UUID, config domain.WorkspaceConfig, now time.Time) WorkspaceResponse {
	repos := make([]RepoConfigResponse, len(config.Repos))
	for i, r := range config.Repos {
		repos[i] = RepoConfigResponse{
			URL:     r.URL,
			Branch:  r.Branch,
			Primary: r.Primary,
		}
	}
	resp := WorkspaceResponse{
		ID:        wsID.String(),
		TenantID:  tenantID.String(),
		Name:      config.Name,
		Status:    string(domain.WorkspaceCreating),
		Mode:      string(config.Mode),
		Repos:     repos,
		CreatedAt: now.Format(time.RFC3339),
		ExpiresAt: now.Add(config.MaxLifetime).Format(time.RFC3339),
	}
	if config.Investigation != nil {
		resp.Investigation = &InvestigationStateResponse{
			CatalogOrg: config.Investigation.CatalogOrg,
		}
	}
	return resp
}

func extractWorkspaceIDAndSuffix(path string) (uuid.UUID, []string, error) {
	parts := strings.Split(strings.TrimPrefix(path, "/api/workspaces/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return uuid.Nil, nil, fmt.Errorf("missing workspace ID")
	}
	id, err := uuid.Parse(parts[0])
	if err != nil {
		return uuid.Nil, nil, err
	}
	return id, parts[1:], nil
}

func extractWorkspaceID(path string) (uuid.UUID, error) {
	id, _, err := extractWorkspaceIDAndSuffix(path)
	return id, err
}
