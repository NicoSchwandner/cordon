package websocket

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// ApprovalStore is an in-memory approval service with channel-based decision notification.
type ApprovalStore struct {
	mu       sync.Mutex
	requests map[uuid.UUID]domain.ApprovalRequest
	channels map[uuid.UUID]chan domain.ApprovalGrant
	grants   map[string]*domain.ApprovalGrant // key: tenantID:workspaceID:operation:target
}

func NewApprovalStore() *ApprovalStore {
	return &ApprovalStore{
		requests: make(map[uuid.UUID]domain.ApprovalRequest),
		channels: make(map[uuid.UUID]chan domain.ApprovalGrant),
		grants:   make(map[string]*domain.ApprovalGrant),
	}
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
		// If this is a session grant, store it for future lookups
		if grant.Scope == domain.GrantSession || grant.Scope == domain.GrantPattern {
			s.mu.Lock()
			req := s.requests[requestID]
			key := grantKey(req.TenantID, req.WorkspaceID, req.Operation, req.Target)
			s.grants[key] = &grant
			s.mu.Unlock()
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

func (s *ApprovalStore) CheckExistingGrant(_ context.Context, tenantID, workspaceID uuid.UUID, operation, target string) (*domain.ApprovalGrant, error) {
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
