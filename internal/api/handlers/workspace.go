package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/api/ws"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/application/progress"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// WorkspaceActivityRecorder records workspace activity for idle timeout tracking.
type WorkspaceActivityRecorder interface {
	RecordActivity(wsID uuid.UUID)
}

// WorkspaceTTLExtender manages TTL overrides for workspaces.
type WorkspaceTTLExtender interface {
	SetExpiry(wsID uuid.UUID, expiresAt time.Time)
	EffectiveExpiry(wsID uuid.UUID, labelExpiry time.Time) time.Time
}

// WorkspacePreparer persists a workspace record before async creation starts.
type WorkspacePreparer interface {
	InsertPending(ctx context.Context, ws domain.Workspace) error
}

// WorkspaceHandlerConfig holds configurable defaults for workspace operations.
type WorkspaceHandlerConfig struct {
	DefaultMaxLifetime time.Duration
	MaxExtension       time.Duration
	MinLifetime        time.Duration
	DefaultIdleTimeout time.Duration
	DefaultCPU         int
	DefaultMemoryMB    int
	AsyncTimeout       time.Duration
}

type WorkspaceHandler struct {
	service  ports.WorkspaceService
	progress *progress.Store
	activity WorkspaceActivityRecorder // optional, nil-safe
	ttl      WorkspaceTTLExtender      // optional, nil-safe
	preparer WorkspacePreparer         // optional, nil-safe
	agents   *ws.AgentRegistry         // optional, nil-safe
	cfg      WorkspaceHandlerConfig
}

func NewWorkspaceHandler(service ports.WorkspaceService, ps *progress.Store, ar WorkspaceActivityRecorder, ttl WorkspaceTTLExtender, agents *ws.AgentRegistry, cfg WorkspaceHandlerConfig) *WorkspaceHandler {
	h := &WorkspaceHandler{service: service, progress: ps, activity: ar, ttl: ttl, agents: agents, cfg: cfg}
	// If the service implements WorkspacePreparer (e.g., PersistentService), use it.
	if p, ok := service.(WorkspacePreparer); ok {
		h.preparer = p
	}
	return h
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
	MaxLifetime      string              `json:"max_lifetime,omitempty"` // e.g. "8h", "4h30m"
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
	IdleTimeout   string                        `json:"idle_timeout,omitempty"`
	MaxLifetime   string                        `json:"max_lifetime,omitempty"`
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
		ID:          ws.ID.String(),
		TenantID:    ws.TenantID.String(),
		Name:        ws.Name,
		Status:      string(ws.Status),
		Mode:        string(ws.Config.Mode),
		Repos:       repos,
		CreatedAt:   ws.CreatedAt.Format(time.RFC3339),
		ExpiresAt:   ws.ExpiresAt.Format(time.RFC3339),
		IdleTimeout: ws.Config.IdleTimeout.String(),
		MaxLifetime: ws.Config.MaxLifetime.String(),
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

	maxLifetime := h.cfg.DefaultMaxLifetime
	if req.MaxLifetime != "" {
		parsed, err := time.ParseDuration(req.MaxLifetime)
		if err != nil || parsed < h.cfg.MinLifetime || parsed > h.cfg.MaxExtension {
			http.Error(w, fmt.Sprintf("invalid max_lifetime (must be between %s and %s)", h.cfg.MinLifetime, h.cfg.MaxExtension), http.StatusBadRequest)
			return
		}
		maxLifetime = parsed
	}

	config := domain.WorkspaceConfig{
		Name:             name,
		Repos:            repos,
		DevcontainerPath: req.DevcontainerPath,
		CPU:              max(req.CPU, h.cfg.DefaultCPU),
		MemoryMB:         max(req.MemoryMB, h.cfg.DefaultMemoryMB),
		IdleTimeout:      h.cfg.DefaultIdleTimeout,
		MaxLifetime:      maxLifetime,
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

		// Persist the workspace record before spawning the goroutine so it
		// appears in dashboard listings immediately.
		if h.preparer != nil {
			pending := domain.Workspace{
				ID:        wsID,
				TenantID:  tenantID,
				Name:      name,
				Status:    domain.WorkspaceCreating,
				CreatedAt: now,
				ExpiresAt: now.Add(config.MaxLifetime),
				Config:    config,
			}
			if err := h.preparer.InsertPending(r.Context(), pending); err != nil {
				slog.Error("failed to persist pending workspace", "component", "workspace", "error", err)
			}
		}

		h.progress.Create(wsID)

		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), h.cfg.AsyncTimeout)
			defer cancel()

			ws, err := h.service.Create(ctx, tenantID, config)
			if err != nil {
				slog.Error("create failed", "component", "workspace", "error", err)
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
	wsID := uuid.New()
	config.ID = wsID
	now := time.Now().UTC()

	if h.preparer != nil {
		pending := domain.Workspace{
			ID: wsID, TenantID: tenantID, Name: name,
			Status: domain.WorkspaceCreating, CreatedAt: now,
			ExpiresAt: now.Add(config.MaxLifetime), Config: config,
		}
		if err := h.preparer.InsertPending(r.Context(), pending); err != nil {
			slog.Error("failed to persist pending workspace", "component", "workspace", "error", err)
		}
	}

	ws, err := h.service.Create(r.Context(), tenantID, config)
	if err != nil {
		slog.Error("create failed", "component", "workspace", "error", err)
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
		resp[i] = h.withEffectiveExpiry(ws)
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
	json.NewEncoder(w).Encode(h.withEffectiveExpiry(ws))
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

	history, ch, unsub, ok := h.progress.Subscribe(wsID)
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
	defer unsub()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Replay events that happened before this subscriber connected
	for _, evt := range history {
		data, _ := json.Marshal(evt)
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	// If creation already completed, the last history event is "done"
	if len(history) > 0 && history[len(history)-1].Done {
		return
	}

	// Stream live events
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

	if h.activity != nil {
		h.activity.RecordActivity(wsID)
	}

	// Streaming path: if stream requested and an agent is connected, relay via WebSocket.
	if req.Stream && h.agents != nil && req.Repo != "" && h.agents.IsConnected(wsID, req.Repo) {
		h.execStreaming(w, r, wsID, req)
		return
	}

	// Fallback: synchronous docker exec
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

// execStreaming relays an exec request to a connected agent and streams JSONL output.
func (h *WorkspaceHandler) execStreaming(w http.ResponseWriter, r *http.Request, wsID uuid.UUID, req ExecRequest) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	ch, err := h.agents.Exec(r.Context(), wsID, req.Repo, req.Cmd, "")
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/exec-failed", Title: "Exec Failed",
			Status: 500, Detail: err.Error(), Code: "exec_failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	enc := json.NewEncoder(w)
	for msg := range ch {
		enc.Encode(msg)
		flusher.Flush()
	}
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
				slog.Error("activate failed", "component", "workspace", "repo_url", repoURL, "error", err)
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

// withEffectiveExpiry patches the ExpiresAt field with the TTL override if one exists.
func (h *WorkspaceHandler) withEffectiveExpiry(ws domain.Workspace) WorkspaceResponse {
	resp := toWorkspaceResponse(ws)
	if h.ttl != nil {
		effective := h.ttl.EffectiveExpiry(ws.ID, ws.ExpiresAt)
		resp.ExpiresAt = effective.Format(time.RFC3339)
	}
	return resp
}

type ExtendRequest struct {
	Duration string `json:"duration"` // e.g. "2h", "1h30m"
}

// Extend extends the TTL of a running workspace.
func (h *WorkspaceHandler) Extend(w http.ResponseWriter, r *http.Request) {
	if h.service == nil || h.ttl == nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/not-implemented", Title: "Not Implemented",
			Status: 501, Detail: "Lifecycle management not configured", Code: "not_implemented",
		})
		return
	}

	wsID, err := extractWorkspaceID(r.URL.Path)
	if err != nil {
		http.Error(w, "invalid workspace ID", http.StatusBadRequest)
		return
	}

	var req ExtendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	duration, err := time.ParseDuration(req.Duration)
	if err != nil || duration <= 0 || duration > h.cfg.MaxExtension {
		http.Error(w, fmt.Sprintf("invalid duration (must be between 1m and %s)", h.cfg.MaxExtension), http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	ws, err := h.service.Get(r.Context(), tenantID, wsID)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	// Compute new expiry: extend from current effective expiry or now, whichever is later
	currentExpiry := h.ttl.EffectiveExpiry(ws.ID, ws.ExpiresAt)
	now := time.Now().UTC()
	base := currentExpiry
	if now.After(base) {
		base = now
	}
	newExpiry := base.Add(duration)

	maxExpiry := now.Add(h.cfg.MaxExtension)
	if newExpiry.After(maxExpiry) {
		newExpiry = maxExpiry
	}

	h.ttl.SetExpiry(ws.ID, newExpiry)
	slog.Info("TTL extended", "component", "workspace", "workspace_id", wsID.String()[:8], "new_expiry", newExpiry.Format(time.RFC3339))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"expires_at": newExpiry.Format(time.RFC3339),
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
		ID:          wsID.String(),
		TenantID:    tenantID.String(),
		Name:        config.Name,
		Status:      string(domain.WorkspaceCreating),
		Mode:        string(config.Mode),
		Repos:       repos,
		CreatedAt:   now.Format(time.RFC3339),
		ExpiresAt:   now.Add(config.MaxLifetime).Format(time.RFC3339),
		IdleTimeout: config.IdleTimeout.String(),
		MaxLifetime: config.MaxLifetime.String(),
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
