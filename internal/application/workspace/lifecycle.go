package workspace

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
)

// LifecycleManager tracks workspace activity and TTL overrides in memory.
// Docker container labels are immutable, so TTL extensions are stored here.
type LifecycleManager struct {
	mu          sync.RWMutex
	activity    map[uuid.UUID]time.Time // last activity per workspace
	expiry      map[uuid.UUID]time.Time // TTL overrides (extends beyond label-based expiry)
	idleTimeout time.Duration
}

func NewLifecycleManager(idleTimeout time.Duration) *LifecycleManager {
	return &LifecycleManager{
		activity:    make(map[uuid.UUID]time.Time),
		expiry:      make(map[uuid.UUID]time.Time),
		idleTimeout: idleTimeout,
	}
}

// RecordActivity marks a workspace as active right now.
func (lm *LifecycleManager) RecordActivity(wsID uuid.UUID) {
	lm.mu.Lock()
	lm.activity[wsID] = time.Now().UTC()
	lm.mu.Unlock()
}

// LastActivity returns the last recorded activity time for a workspace.
func (lm *LifecycleManager) LastActivity(wsID uuid.UUID) (time.Time, bool) {
	lm.mu.RLock()
	t, ok := lm.activity[wsID]
	lm.mu.RUnlock()
	return t, ok
}

// SetExpiry overrides the TTL for a workspace.
func (lm *LifecycleManager) SetExpiry(wsID uuid.UUID, expiresAt time.Time) {
	lm.mu.Lock()
	lm.expiry[wsID] = expiresAt
	lm.mu.Unlock()
}

// GetExpiry returns the overridden expiry if one exists.
func (lm *LifecycleManager) GetExpiry(wsID uuid.UUID) (time.Time, bool) {
	lm.mu.RLock()
	t, ok := lm.expiry[wsID]
	lm.mu.RUnlock()
	return t, ok
}

// EffectiveExpiry returns the TTL override if set, otherwise the label-based expiry.
func (lm *LifecycleManager) EffectiveExpiry(wsID uuid.UUID, labelExpiry time.Time) time.Time {
	if t, ok := lm.GetExpiry(wsID); ok {
		return t
	}
	return labelExpiry
}

// Remove cleans up all tracked state for a workspace.
func (lm *LifecycleManager) Remove(wsID uuid.UUID) {
	lm.mu.Lock()
	delete(lm.activity, wsID)
	delete(lm.expiry, wsID)
	lm.mu.Unlock()
}

// WorkspaceLifecycleService is the subset of WorkspaceService the reaper needs.
type WorkspaceLifecycleService interface {
	Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error
	Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error
}

// Reaper periodically checks workspaces and enforces idle timeout (suspend) and max lifetime (destroy).
type Reaper struct {
	backend  ports.ComputeBackend
	svc      WorkspaceLifecycleService
	lm       *LifecycleManager
	interval time.Duration
}

func NewReaper(backend ports.ComputeBackend, svc WorkspaceLifecycleService, lm *LifecycleManager, interval time.Duration) *Reaper {
	return &Reaper{
		backend:  backend,
		svc:      svc,
		lm:       lm,
		interval: interval,
	}
}

// Start runs the reaper loop until the context is cancelled.
func (r *Reaper) Start(ctx context.Context) {
	slog.Info("reaper started", "component", "reaper", "interval", r.interval, "idle_timeout", r.lm.idleTimeout)
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("reaper stopped", "component", "reaper")
			return
		case <-ticker.C:
			r.tick(ctx)
		}
	}
}

func (r *Reaper) tick(ctx context.Context) {
	handles, err := r.backend.ListContainers(ctx, map[string]string{"cordon.workspace": ""})
	if err != nil {
		slog.Warn("error listing containers", "component", "reaper", "error", err)
		return
	}

	if len(handles) == 0 {
		return
	}

	now := time.Now().UTC()
	for _, h := range handles {
		if h.Labels["cordon.service-for"] != "" {
			continue // skip service containers, lifecycle tied to primary
		}

		ws := handleToWorkspace(h)
		if ws.Status != "running" && ws.Status != "suspended" {
			continue
		}

		effectiveExpiry := r.lm.EffectiveExpiry(ws.ID, ws.ExpiresAt)

		// Check max lifetime → destroy
		if now.After(effectiveExpiry) {
			slog.Info("workspace expired, destroying", "component", "reaper", "workspace", ws.ID.String()[:8], "expires", effectiveExpiry.Format(time.RFC3339))
			if err := r.svc.Destroy(ctx, ws.TenantID, ws.ID); err != nil {
				slog.Warn("destroy failed", "component", "reaper", "workspace", ws.ID.String()[:8], "error", err)
			}
			r.lm.Remove(ws.ID)
			continue
		}

		// Check idle timeout → suspend (only for running workspaces)
		if ws.Status != "running" || r.lm.idleTimeout <= 0 {
			continue
		}

		// Skip workspaces still within their initial setup window — creation
		// (image build + clone + post-create) can take longer than the idle timeout.
		if now.Sub(ws.CreatedAt) < r.lm.idleTimeout {
			continue
		}

		lastActivity, hasActivity := r.lm.LastActivity(ws.ID)
		if !hasActivity {
			// No activity ever recorded — use creation time as baseline
			lastActivity = ws.CreatedAt
		}

		if now.Sub(lastActivity) > r.lm.idleTimeout {
			slog.Info("workspace idle, suspending", "component", "reaper", "workspace", ws.ID.String()[:8], "last_activity", lastActivity.Format(time.RFC3339))
			if err := r.svc.Suspend(ctx, ws.TenantID, ws.ID); err != nil {
				slog.Warn("suspend failed", "component", "reaper", "workspace", ws.ID.String()[:8], "error", err)
			}
		}
	}
}
