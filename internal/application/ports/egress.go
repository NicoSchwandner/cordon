package ports

import "github.com/google/uuid"

// EgressPolicyManager allows workspace creation/destruction to register
// per-workspace egress rules. Implemented by the proxy's EgressChecker.
type EgressPolicyManager interface {
	SetWorkspacePolicy(wsID uuid.UUID, additionalHosts []string)
	RemoveWorkspacePolicy(wsID uuid.UUID)
}
