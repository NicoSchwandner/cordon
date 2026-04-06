package ports

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ErrWorkspaceNotFound is returned when a workspace does not exist in the store.
var ErrWorkspaceNotFound = errors.New("workspace not found")

// WorkspaceStore persists workspace records to a durable store.
type WorkspaceStore interface {
	Insert(ctx context.Context, ws domain.Workspace) error
	Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) // excludes destroyed
	UpdateStatus(ctx context.Context, workspaceID uuid.UUID, status domain.WorkspaceStatus) error
	UpdateExpiry(ctx context.Context, workspaceID uuid.UUID, expiresAt time.Time) error
	RecordActivity(ctx context.Context, workspaceID uuid.UUID, at time.Time) error
	UpdateConfig(ctx context.Context, workspaceID uuid.UUID, config domain.WorkspaceConfig) error
}
