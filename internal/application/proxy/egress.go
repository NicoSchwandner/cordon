package proxy

import "strings"

// EgressChecker validates whether a host is in the egress allowlist.
type EgressChecker struct {
	Allowlist []string
}

func NewEgressChecker(allowlist []string) *EgressChecker {
	return &EgressChecker{Allowlist: allowlist}
}

func (e *EgressChecker) IsAllowed(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	// Strip port if present
	if idx := strings.LastIndex(host, ":"); idx != -1 {
		host = host[:idx]
	}

	for _, pattern := range e.Allowlist {
		pattern = strings.ToLower(strings.TrimSpace(pattern))
		if pattern == host {
			return true
		}
		// Wildcard: *.example.com matches sub.example.com
		if strings.HasPrefix(pattern, "*.") {
			suffix := pattern[1:] // ".example.com"
			if strings.HasSuffix(host, suffix) {
				return true
			}
			// Also match the bare domain: *.example.com should match example.com
			if host == pattern[2:] {
				return true
			}
		}
	}
	return false
}
