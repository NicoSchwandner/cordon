package sops

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// MemoryVault is an in-memory SecretVault for testing.
type MemoryVault struct {
	secrets map[string]map[string]string // tenantID -> placeholder -> real value
	refs    map[string][]domain.SecretRef
}

func NewMemoryVault() *MemoryVault {
	return &MemoryVault{
		secrets: make(map[string]map[string]string),
		refs:    make(map[string][]domain.SecretRef),
	}
}

func (v *MemoryVault) AddSecret(tenantID uuid.UUID, name, placeholder, realValue string) {
	tid := tenantID.String()
	if v.secrets[tid] == nil {
		v.secrets[tid] = make(map[string]string)
	}
	v.secrets[tid][placeholder] = realValue
	v.refs[tid] = append(v.refs[tid], domain.SecretRef{Name: name, Placeholder: placeholder})
}

func (v *MemoryVault) Resolve(_ context.Context, tenantID uuid.UUID, placeholder string) (string, error) {
	tid := tenantID.String()
	if secrets, ok := v.secrets[tid]; ok {
		if val, ok := secrets[placeholder]; ok {
			return val, nil
		}
	}
	return "", fmt.Errorf("secret not found for placeholder %q", placeholder)
}

func (v *MemoryVault) ListRefs(_ context.Context, tenantID uuid.UUID) ([]domain.SecretRef, error) {
	return v.refs[tenantID.String()], nil
}
