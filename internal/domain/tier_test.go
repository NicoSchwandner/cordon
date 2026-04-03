package domain

import "testing"

func TestTierString(t *testing.T) {
	tests := []struct {
		tier Tier
		want string
	}{
		{TierRead, "read"},
		{TierSafeWrite, "safe_write"},
		{TierDestructive, "destructive"},
		{TierForbidden, "forbidden"},
		{Tier(99), "unknown(99)"},
	}
	for _, tt := range tests {
		if got := tt.tier.String(); got != tt.want {
			t.Errorf("Tier(%d).String() = %q, want %q", int(tt.tier), got, tt.want)
		}
	}
}

func TestTierIsBlocked(t *testing.T) {
	tests := []struct {
		tier Tier
		want bool
	}{
		{TierRead, false},
		{TierSafeWrite, false},
		{TierDestructive, false},
		{TierForbidden, true},
	}
	for _, tt := range tests {
		if got := tt.tier.IsBlocked(); got != tt.want {
			t.Errorf("Tier(%d).IsBlocked() = %v, want %v", int(tt.tier), got, tt.want)
		}
	}
}
