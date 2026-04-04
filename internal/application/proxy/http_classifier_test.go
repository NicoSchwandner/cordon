package proxy

import (
	"testing"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

func TestHTTPClassifier(t *testing.T) {
	c := NewHTTPClassifier()

	tests := []struct {
		name     string
		method   string
		path     string
		wantTier domain.Tier
	}{
		// Tier 1 — Read
		{"GET", "GET", "/api/users", domain.TierRead},
		{"HEAD", "HEAD", "/api/users", domain.TierRead},
		{"OPTIONS", "OPTIONS", "/api/users", domain.TierRead},

		// Tier 2 — Safe Write
		{"POST", "POST", "/api/users", domain.TierSafeWrite},
		{"PUT", "PUT", "/api/users/1", domain.TierSafeWrite},
		{"PATCH", "PATCH", "/api/users/1", domain.TierSafeWrite},

		// Tier 3 — Destructive
		{"DELETE", "DELETE", "/api/users/1", domain.TierDestructive},
		{"unknown method", "PURGE", "/api/cache", domain.TierDestructive},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.ClassifyHTTP(tt.method, tt.path, nil)
			if result.Tier != tt.wantTier {
				t.Errorf("ClassifyHTTP(%s, %s) = tier %v, want %v", tt.method, tt.path, result.Tier, tt.wantTier)
			}
		})
	}
}

func TestHTTPClassifierOverrides(t *testing.T) {
	c := NewHTTPClassifier()

	overrides := []ports.TierOverride{
		{Pattern: "DELETE /api/cache", Tier: domain.TierSafeWrite},
	}

	result := c.ClassifyHTTP("DELETE", "/api/cache", overrides)
	if result.Tier != domain.TierSafeWrite {
		t.Errorf("override should de-escalate DELETE to safe_write, got %v", result.Tier)
	}
}

func TestHTTPClassifierCaseInsensitive(t *testing.T) {
	c := NewHTTPClassifier()

	result := c.ClassifyHTTP("get", "/api/users", nil)
	if result.Tier != domain.TierRead {
		t.Errorf("lowercase get should be Tier 1, got %v", result.Tier)
	}
}
