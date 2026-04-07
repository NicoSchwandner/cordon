package workspace

import (
	"context"
	"errors"
	"log/slog"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// PersistentService wraps a WorkspaceService (Orchestrator) with database
// persistence. It keeps a workspace record in PostgreSQL so that workspaces
// are visible from the moment creation starts and survive server restarts.
type PersistentService struct {
	store ports.WorkspaceStore
	inner ports.WorkspaceService
}

var _ ports.WorkspaceService = (*PersistentService)(nil)

func NewPersistentService(store ports.WorkspaceStore, inner ports.WorkspaceService) *PersistentService {
	return &PersistentService{store: store, inner: inner}
}

// InsertPending saves a workspace record with status "creating" so it appears
// in listings immediately, before the container is built. Called by the handler
// before spawning the async creation goroutine.
func (ps *PersistentService) InsertPending(ctx context.Context, ws domain.Workspace) error {
	return ps.store.Insert(ctx, ws)
}

func (ps *PersistentService) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	ws, err := ps.inner.Create(ctx, tenantID, config)
	if err != nil {
		// Mark as destroyed in the DB so it doesn't linger as "creating" forever.
		if storeErr := ps.store.UpdateStatus(ctx, config.ID, domain.WorkspaceDestroyed); storeErr != nil {
			slog.Warn("failed to mark workspace as destroyed after create failure", "component", "persistent", "workspace", config.ID.String()[:8], "error", storeErr)
		}
		return ws, err
	}

	if storeErr := ps.store.UpdateStatus(ctx, config.ID, domain.WorkspaceRunning); storeErr != nil {
		slog.Warn("failed to update workspace to running", "component", "persistent", "workspace", config.ID.String()[:8], "error", storeErr)
	}
	return ws, nil
}

func (ps *PersistentService) Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	// Check DB first — if status is "creating", the container may already be
	// running but postCreateCommand/cloning is still in progress. Return the
	// DB status so the frontend shows the progress view, not the terminal.
	dbWS, dbErr := ps.store.Get(ctx, tenantID, workspaceID)
	if dbErr == nil && dbWS.Status == domain.WorkspaceCreating {
		return dbWS, nil
	}

	// Try Docker for live container state and enriched investigation data.
	ws, err := ps.inner.Get(ctx, tenantID, workspaceID)
	if err == nil {
		return ws, nil
	}

	// Fall back to DB for recently destroyed workspaces or other states.
	if dbErr != nil {
		if errors.Is(dbErr, ports.ErrWorkspaceNotFound) {
			return domain.Workspace{}, err // return original Docker error
		}
		return domain.Workspace{}, dbErr
	}
	return dbWS, nil
}

func (ps *PersistentService) List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) {
	// DB is the source of truth for which workspaces exist (includes "creating").
	dbWorkspaces, err := ps.store.List(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	// Get live Docker state for status enrichment.
	dockerWorkspaces, dockerErr := ps.inner.List(ctx, tenantID)

	// Index Docker workspaces by ID for fast lookup.
	dockerByID := make(map[uuid.UUID]domain.Workspace, len(dockerWorkspaces))
	if dockerErr == nil {
		for _, dws := range dockerWorkspaces {
			dockerByID[dws.ID] = dws
		}
	}

	result := make([]domain.Workspace, 0, len(dbWorkspaces))
	for _, dbWS := range dbWorkspaces {
		if dws, ok := dockerByID[dbWS.ID]; ok {
			// Docker has live data — use its status and enriched fields.
			result = append(result, dws)
			delete(dockerByID, dbWS.ID)
		} else if dbWS.Status == domain.WorkspaceCreating {
			// Still being built — no container yet, show from DB.
			result = append(result, dbWS)
		} else if dbWS.Status == domain.WorkspaceRunning || dbWS.Status == domain.WorkspaceSuspended {
			// DB says running/suspended but no Docker container — container was removed externally.
			slog.Warn("workspace has no container, marking destroyed", "component", "persistent", "workspace", dbWS.ID.String()[:8])
			ps.store.UpdateStatus(ctx, dbWS.ID, domain.WorkspaceDestroyed)
			// Don't include in results.
		}
		// Destroyed workspaces are already excluded by store.List.
	}

	// Include Docker workspaces not yet in DB (from before migration, or edge cases).
	for _, dws := range dockerByID {
		result = append(result, dws)
	}

	return result, nil
}

func (ps *PersistentService) Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	if err := ps.inner.Suspend(ctx, tenantID, workspaceID); err != nil {
		return err
	}
	if err := ps.store.UpdateStatus(ctx, workspaceID, domain.WorkspaceSuspended); err != nil {
		slog.Warn("failed to update workspace to suspended", "component", "persistent", "workspace", workspaceID.String()[:8], "error", err)
	}
	return nil
}

func (ps *PersistentService) Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	if err := ps.inner.Resume(ctx, tenantID, workspaceID); err != nil {
		return err
	}
	if err := ps.store.UpdateStatus(ctx, workspaceID, domain.WorkspaceRunning); err != nil {
		slog.Warn("failed to update workspace to running", "component", "persistent", "workspace", workspaceID.String()[:8], "error", err)
	}
	return nil
}

func (ps *PersistentService) Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	if err := ps.inner.Destroy(ctx, tenantID, workspaceID); err != nil {
		return err
	}
	if err := ps.store.UpdateStatus(ctx, workspaceID, domain.WorkspaceDestroyed); err != nil {
		slog.Warn("failed to update workspace to destroyed", "component", "persistent", "workspace", workspaceID.String()[:8], "error", err)
	}
	return nil
}

func (ps *PersistentService) Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error) {
	return ps.inner.Exec(ctx, tenantID, workspaceID, cmd)
}

func (ps *PersistentService) ExecInRepo(ctx context.Context, workspaceID uuid.UUID, repoName string, cmd []string) (int, string, error) {
	return ps.inner.ExecInRepo(ctx, workspaceID, repoName, cmd)
}

func (ps *PersistentService) ActivateRepo(ctx context.Context, tenantID, workspaceID uuid.UUID, repoURL string) error {
	return ps.inner.ActivateRepo(ctx, tenantID, workspaceID, repoURL)
}
