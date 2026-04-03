package domain

import (
	"time"

	"github.com/google/uuid"
)

// Decision represents the outcome of an operation check.
type Decision string

const (
	DecisionAllowed         Decision = "allowed"
	DecisionDenied          Decision = "denied"
	DecisionBlocked         Decision = "blocked"
	DecisionPendingApproval Decision = "pending_approval"
)

// AuditEntry records a single proxied operation.
type AuditEntry struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	WorkspaceID uuid.UUID
	Timestamp   time.Time
	Tier        Tier
	Operation   string
	Target      string
	Caller      string
	Decision    Decision
	DurationMs  int64
	Detail      string
}
