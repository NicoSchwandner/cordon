package sops

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// MemoryVault is an in-memory SecretVault for testing.
type MemoryVault struct {
	mu      sync.RWMutex
	secrets map[string]map[string]string   // tenantID -> placeholder -> real value
	refs    map[string][]domain.SecretRef  // tenantID -> refs
	byName  map[string]map[string]string   // tenantID -> name -> placeholder
}

func NewMemoryVault() *MemoryVault {
	return &MemoryVault{
		secrets: make(map[string]map[string]string),
		refs:    make(map[string][]domain.SecretRef),
		byName:  make(map[string]map[string]string),
	}
}

func (v *MemoryVault) AddSecret(tenantID uuid.UUID, name, placeholder, realValue string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.addSecretLocked(tenantID.String(), name, placeholder, realValue)
}

func (v *MemoryVault) addSecretLocked(tid, name, placeholder, realValue string) {
	if v.secrets[tid] == nil {
		v.secrets[tid] = make(map[string]string)
	}
	if v.byName[tid] == nil {
		v.byName[tid] = make(map[string]string)
	}
	v.secrets[tid][placeholder] = realValue
	v.byName[tid][name] = placeholder

	// Replace existing ref or append
	found := false
	for i, ref := range v.refs[tid] {
		if ref.Name == name {
			v.refs[tid][i].Placeholder = placeholder
			found = true
			break
		}
	}
	if !found {
		v.refs[tid] = append(v.refs[tid], domain.SecretRef{Name: name, Placeholder: placeholder})
	}
}

func (v *MemoryVault) SetSecret(_ context.Context, tenantID uuid.UUID, name, placeholder, realValue string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	tid := tenantID.String()

	// If updating an existing secret, remove the old placeholder mapping
	if oldPlaceholder, ok := v.byName[tid][name]; ok && oldPlaceholder != placeholder {
		delete(v.secrets[tid], oldPlaceholder)
	}

	v.addSecretLocked(tid, name, placeholder, realValue)
	return nil
}

func (v *MemoryVault) DeleteSecret(_ context.Context, tenantID uuid.UUID, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	tid := tenantID.String()

	placeholder, ok := v.byName[tid][name]
	if !ok {
		return fmt.Errorf("secret %q not found", name)
	}

	delete(v.secrets[tid], placeholder)
	delete(v.byName[tid], name)

	refs := v.refs[tid]
	for i, ref := range refs {
		if ref.Name == name {
			v.refs[tid] = append(refs[:i], refs[i+1:]...)
			break
		}
	}
	return nil
}

func (v *MemoryVault) Resolve(_ context.Context, tenantID uuid.UUID, placeholder string) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	tid := tenantID.String()
	if secrets, ok := v.secrets[tid]; ok {
		if val, ok := secrets[placeholder]; ok {
			return val, nil
		}
	}
	return "", fmt.Errorf("secret not found for placeholder %q", placeholder)
}

func (v *MemoryVault) RevealValue(_ context.Context, tenantID uuid.UUID, name string) (string, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	tid := tenantID.String()
	placeholder, ok := v.byName[tid][name]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	val, ok := v.secrets[tid][placeholder]
	if !ok {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return val, nil
}

func (v *MemoryVault) ListRefs(_ context.Context, tenantID uuid.UUID) ([]domain.SecretRef, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()
	return v.refs[tenantID.String()], nil
}
