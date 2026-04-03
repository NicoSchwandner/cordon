package domain

import (
	"time"

	"github.com/google/uuid"
)

type ApprovalStatus string

const (
	ApprovalPending ApprovalStatus = "pending"
	ApprovalGranted ApprovalStatus = "granted"
	ApprovalDenied  ApprovalStatus = "denied"
	ApprovalExpired ApprovalStatus = "expired"
)

type GrantScope string

const (
	GrantOneTime GrantScope = "one_time"
	GrantSession GrantScope = "session"
	GrantPattern GrantScope = "pattern"
)

// ApprovalRequest is created when a Tier 3 operation needs human approval.
type ApprovalRequest struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	Tier        Tier
	Operation   string
	Target      string
	Caller      string
	RequestedAt time.Time
	ExpiresAt   time.Time
	Status      ApprovalStatus
}

// ApprovalGrant records an approval decision with its scope.
type ApprovalGrant struct {
	Scope     GrantScope
	Pattern   string
	ExpiresAt time.Time
}
