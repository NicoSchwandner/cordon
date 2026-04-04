package ports

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// AuditFilter specifies criteria for querying audit entries.
type AuditFilter struct {
	TenantID    uuid.UUID
	WorkspaceID *uuid.UUID
	TierMin     *domain.Tier
	Decision    *domain.Decision
	Since       *time.Time
	Until       *time.Time
	Limit       int
	Offset      int
}

// AuditStore is the persistence port for audit entries.
type AuditStore interface {
	Write(ctx context.Context, entry domain.AuditEntry) error
	Query(ctx context.Context, filter AuditFilter) ([]domain.AuditEntry, error)
}
