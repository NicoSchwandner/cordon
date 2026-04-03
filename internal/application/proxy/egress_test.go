package proxy

import "testing"

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
