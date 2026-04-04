package ports

import (
	"context"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// SecretVault resolves placeholder tokens to real credential values.
type SecretVault interface {
	Resolve(ctx context.Context, tenantID uuid.UUID, placeholder string) (string, error)
	ListRefs(ctx context.Context, tenantID uuid.UUID) ([]domain.SecretRef, error)
	SetSecret(ctx context.Context, tenantID uuid.UUID, name, placeholder, realValue string) error
	DeleteSecret(ctx context.Context, tenantID uuid.UUID, name string) error
	RevealValue(ctx context.Context, tenantID uuid.UUID, name string) (string, error)
}
