package websocket

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// ApprovalStore is an in-memory approval service with channel-based decision notification.
// When a GrantStore is configured, grants are persisted to PostgreSQL and survive restarts.
// Without a GrantStore, grants are kept in memory only (graceful degradation for development).
type ApprovalStore struct {
	mu         sync.Mutex
	requests   map[uuid.UUID]domain.ApprovalRequest
	channels   map[uuid.UUID]chan domain.ApprovalGrant
	grants     map[string]*domain.ApprovalGrant // key: tenantID:workspaceID:operation:target
	grantStore ports.GrantStore                 // optional — nil means in-memory only
}

func NewApprovalStore() *ApprovalStore {
	return &ApprovalStore{
		requests: make(map[uuid.UUID]domain.ApprovalRequest),
		channels: make(map[uuid.UUID]chan domain.ApprovalGrant),
		grants:   make(map[string]*domain.ApprovalGrant),
	}
}

// SetGrantStore configures a persistent grant store. When set, grants are saved to
// and checked from the persistent store instead of in-memory maps.
func (s *ApprovalStore) SetGrantStore(gs ports.GrantStore) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grantStore = gs
}

func (s *ApprovalStore) RequestApproval(_ context.Context, req domain.ApprovalRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests[req.ID] = req
	s.channels[req.ID] = make(chan domain.ApprovalGrant, 1)
	return nil
}

func (s *ApprovalStore) AwaitDecision(ctx context.Context, requestID uuid.UUID, timeout time.Duration) (domain.ApprovalGrant, error) {
	s.mu.Lock()
	ch, ok := s.channels[requestID]
	s.mu.Unlock()
	if !ok {
		return domain.ApprovalGrant{}, errors.New("no pending approval request")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case grant := <-ch:
		// If this is a reusable grant, persist it for future lookups.
		if grant.Scope == domain.GrantSession || grant.Scope == domain.GrantPattern || grant.Scope == domain.GrantOneTime {
			s.mu.Lock()
			req := s.requests[requestID]
			gs := s.grantStore
			s.mu.Unlock()

			if gs != nil {
				if err := gs.SaveGrant(ctx, req.TenantID, req.WorkspaceID, req.Operation, req.Target, grant); err != nil {
					slog.Warn("failed to persist approval grant, falling back to in-memory", "error", err)
					s.mu.Lock()
					key := grantKey(req.TenantID, req.WorkspaceID, req.Operation, req.Target)
					s.grants[key] = &grant
					s.mu.Unlock()
				}
			} else {
				s.mu.Lock()
				key := grantKey(req.TenantID, req.WorkspaceID, req.Operation, req.Target)
				s.grants[key] = &grant
				s.mu.Unlock()
			}
		}
		return grant, nil
	case <-timer.C:
		return domain.ApprovalGrant{}, errors.New("approval timeout")
	case <-ctx.Done():
		return domain.ApprovalGrant{}, ctx.Err()
	}
}

func (s *ApprovalStore) Decide(_ context.Context, requestID uuid.UUID, grant domain.ApprovalGrant) error {
	s.mu.Lock()
	ch, ok := s.channels[requestID]
	s.mu.Unlock()
	if !ok {
		return errors.New("no pending approval request")
	}
	ch <- grant
	return nil
}

func (s *ApprovalStore) CheckExistingGrant(ctx context.Context, tenantID, workspaceID uuid.UUID, operation, target string) (*domain.ApprovalGrant, error) {
	s.mu.Lock()
	gs := s.grantStore
	s.mu.Unlock()

	// If a persistent store is configured, check it first.
	if gs != nil {
		grant, err := gs.CheckGrant(ctx, tenantID, workspaceID, operation, target)
		if err != nil {
			slog.Warn("failed to check persistent grant store, falling back to in-memory", "error", err)
		} else if grant != nil {
			return grant, nil
		}
	}

	// Fall back to in-memory grants.
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tenantID, workspaceID, operation, target)
	grant, ok := s.grants[key]
	if !ok {
		return nil, nil
	}
	if !grant.ExpiresAt.IsZero() && time.Now().After(grant.ExpiresAt) {
		delete(s.grants, key)
		return nil, nil
	}
	return grant, nil
}

// PendingRequests returns all pending approval requests (for broadcasting to UI).
func (s *ApprovalStore) PendingRequests() []domain.ApprovalRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	var pending []domain.ApprovalRequest
	for _, req := range s.requests {
		if req.Status == domain.ApprovalPending {
			pending = append(pending, req)
		}
	}
	return pending
}

func grantKey(tenantID, workspaceID uuid.UUID, operation, target string) string {
	return tenantID.String() + ":" + workspaceID.String() + ":" + operation + ":" + target
}
