package proxy

import (
	"strings"
	"sync"

	"github.com/google/uuid"
)

// EgressChecker validates whether a host is in the egress allowlist.
// Supports both global and per-workspace allowlists. The effective allowlist
// for a workspace is the union of global + workspace-specific entries.
type EgressChecker struct {
	globalAllowlist   []string
	mu                sync.RWMutex
	workspacePolicies map[uuid.UUID][]string
}

func NewEgressChecker(globalAllowlist []string) *EgressChecker {
	return &EgressChecker{
		globalAllowlist:   globalAllowlist,
		workspacePolicies: make(map[uuid.UUID][]string),
	}
}

// SetWorkspacePolicy registers additional allowed hosts for a workspace.
func (e *EgressChecker) SetWorkspacePolicy(wsID uuid.UUID, additionalHosts []string) {
	e.mu.Lock()
	e.workspacePolicies[wsID] = additionalHosts
	e.mu.Unlock()
}

// RemoveWorkspacePolicy removes the per-workspace egress policy.
func (e *EgressChecker) RemoveWorkspacePolicy(wsID uuid.UUID) {
	e.mu.Lock()
	delete(e.workspacePolicies, wsID)
	e.mu.Unlock()
}

// IsAllowed checks against the global allowlist only (backward compat).
func (e *EgressChecker) IsAllowed(host string) bool {
	return isAllowedAgainst(host, e.globalAllowlist)
}

// IsAllowedForWorkspace checks against the union of global + workspace allowlists.
func (e *EgressChecker) IsAllowedForWorkspace(host string, wsID uuid.UUID) bool {
	if isAllowedAgainst(host, e.globalAllowlist) {
		return true
	}
	if wsID == uuid.Nil {
		return false
	}
	e.mu.RLock()
	additional := e.workspacePolicies[wsID]
	e.mu.RUnlock()
	return isAllowedAgainst(host, additional)
}

func isAllowedAgainst(host string, patterns []string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	for _, pattern := range patterns {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == host {
			return true
		}
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
			if host == pattern[2:] {
				return true
			}
		}
	}
	return false
}
