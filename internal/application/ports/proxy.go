package ports

import "github.com/NicoSchwandner/cordon/internal/domain"

// TierOverride allows per-project tier escalation/de-escalation.
type TierOverride struct {
	Pattern string
	Tier    domain.Tier
}

// TierClassifier assigns a risk tier to an operation.
type TierClassifier interface {
	ClassifySQL(query string, overrides []TierOverride) domain.TierResult
	ClassifyHTTP(method, path string, overrides []TierOverride) domain.TierResult
}
