package proxy

import (
	"testing"

	"github.com/google/uuid"
)

func TestEgressChecker(t *testing.T) {
	checker := NewEgressChecker([]string{
		"*.example.com",
		"registry.npmjs.org",
		"nuget.org",
		"github.com",
		"api.anthropic.com",
	})

	tests := []struct {
		host    string
		allowed bool
	}{
		// Allowed
		{"github.com", true},
		{"registry.npmjs.org", true},
		{"nuget.org", true},
		{"api.anthropic.com", true},
		{"sub.example.com", true},
		{"deep.sub.example.com", true},
		{"example.com", true}, // bare domain matches *.example.com

		// Blocked
		{"evil-exfil.com", false},
		{"github.com.evil.com", false},
		{"notnuget.org", false},
		{"", false},

		// With port
		{"github.com:443", true},
		{"evil.com:80", false},
	}

	for _, tt := range tests {
		t.Run(tt.host, func(t *testing.T) {
			got := checker.IsAllowed(tt.host)
			if got != tt.allowed {
				t.Errorf("IsAllowed(%q) = %v, want %v", tt.host, got, tt.allowed)
			}
		})
	}
}

func TestEgressCheckerEmptyAllowlist(t *testing.T) {
	checker := NewEgressChecker(nil)
	if checker.IsAllowed("anything.com") {
		t.Error("empty allowlist should block everything")
	}
}

func TestEgressCheckerWorkspacePolicy(t *testing.T) {
	checker := NewEgressChecker([]string{"github.com"})
	wsID := uuid.New()

	// Before policy: only global hosts allowed
	if !checker.IsAllowedForWorkspace("github.com", wsID) {
		t.Error("global host should be allowed for any workspace")
	}
	if checker.IsAllowedForWorkspace("custom-api.internal.com", wsID) {
		t.Error("non-global host should be blocked without workspace policy")
	}

	// Set workspace policy
	checker.SetWorkspacePolicy(wsID, []string{"custom-api.internal.com", "*.corp.net"})

	if !checker.IsAllowedForWorkspace("custom-api.internal.com", wsID) {
		t.Error("workspace-specific host should be allowed after SetWorkspacePolicy")
	}
	if !checker.IsAllowedForWorkspace("svc.corp.net", wsID) {
		t.Error("workspace wildcard should match")
	}
	if !checker.IsAllowedForWorkspace("github.com", wsID) {
		t.Error("global host should still be allowed")
	}

	// Other workspaces don't get the policy
	otherWS := uuid.New()
	if checker.IsAllowedForWorkspace("custom-api.internal.com", otherWS) {
		t.Error("workspace policy should not apply to other workspaces")
	}

	// Remove policy
	checker.RemoveWorkspacePolicy(wsID)
	if checker.IsAllowedForWorkspace("custom-api.internal.com", wsID) {
		t.Error("workspace host should be blocked after RemoveWorkspacePolicy")
	}
}

func TestEgressCheckerNilWorkspaceID(t *testing.T) {
	checker := NewEgressChecker([]string{"github.com"})

	if !checker.IsAllowedForWorkspace("github.com", uuid.Nil) {
		t.Error("global host should be allowed even with nil workspace ID")
	}
	if checker.IsAllowedForWorkspace("evil.com", uuid.Nil) {
		t.Error("non-global host should be blocked with nil workspace ID")
	}
}
