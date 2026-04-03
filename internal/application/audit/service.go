package audit

import (
	"context"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// Service provides audit query operations with tenant authorization.
type Service struct {
	store ports.AuditStore
}

func NewService(store ports.AuditStore) *Service {
	return &Service{store: store}
}

func (s *Service) Write(ctx context.Context, entry domain.AuditEntry) error {
	return s.store.Write(ctx, entry)
}

func (s *Service) Query(ctx context.Context, tenantID uuid.UUID, filter ports.AuditFilter) ([]domain.AuditEntry, error) {
	// Enforce tenant isolation: override filter's tenant ID with the authenticated one
	filter.TenantID = tenantID
	return s.store.Query(ctx, filter)
}
