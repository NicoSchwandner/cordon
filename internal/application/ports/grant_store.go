package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// GrantStore persists approval grants so they survive server restarts.
type GrantStore interface {
	SaveGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string, grant domain.ApprovalGrant) error
	CheckGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string) (*domain.ApprovalGrant, error)
	CleanExpired(ctx context.Context) error
}
