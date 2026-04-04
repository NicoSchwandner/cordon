package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ApprovalService manages the approval flow for Tier 3 operations.
type ApprovalService interface {
	RequestApproval(ctx context.Context, req domain.ApprovalRequest) error
	AwaitDecision(ctx context.Context, requestID uuid.UUID, timeout time.Duration) (domain.ApprovalGrant, error)
	Decide(ctx context.Context, requestID uuid.UUID, grant domain.ApprovalGrant) error
	CheckExistingGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string) (*domain.ApprovalGrant, error)
}
