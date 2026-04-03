package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// WorkspaceService manages ephemeral compute environments.
type WorkspaceService interface {
	Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error)
	Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error)
	List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error)
	Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error
	Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error
	Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error
	Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error)
	ExecInRepo(ctx context.Context, workspaceID uuid.UUID, repoName string, cmd []string) (int, string, error)
}
