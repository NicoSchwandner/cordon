package workspace

import (
	"sync"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
)

// MemoryRegistry is an in-memory implementation of ports.WorkspaceRegistry.
// Suitable for single-node deployments. Will be replaced by a Postgres-backed
// implementation when the persistent workspace store is introduced.
type MemoryRegistry struct {
	mu   sync.RWMutex
	byIP map[string]uuid.UUID
}

var _ ports.WorkspaceRegistry = (*MemoryRegistry)(nil)

func NewMemoryRegistry() *MemoryRegistry {
	return &MemoryRegistry{
		byIP: make(map[string]uuid.UUID),
	}
}

func (r *MemoryRegistry) Register(addr string, workspaceID uuid.UUID) {
	r.mu.Lock()
	r.byIP[addr] = workspaceID
	r.mu.Unlock()
}

func (r *MemoryRegistry) Unregister(addr string) {
	r.mu.Lock()
	delete(r.byIP, addr)
	r.mu.Unlock()
}

func (r *MemoryRegistry) Lookup(remoteIP string) uuid.UUID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byIP[remoteIP]
}
