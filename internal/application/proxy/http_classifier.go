package proxy

import (
	"strings"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// HTTPClassifier assigns tiers to HTTP requests based on method.
type HTTPClassifier struct{}

func NewHTTPClassifier() *HTTPClassifier {
	return &HTTPClassifier{}
}

func (c *HTTPClassifier) ClassifyHTTP(method, path string, overrides []ports.TierOverride) domain.TierResult {
	method = strings.ToUpper(strings.TrimSpace(method))

	// Check overrides
	for _, o := range overrides {
		pattern := strings.ToUpper(o.Pattern)
		check := method + " " + path
		if strings.Contains(strings.ToUpper(check), pattern) {
			return domain.TierResult{
				Tier:        o.Tier,
				Reason:      "matched override pattern",
				RuleMatched: "override:" + o.Pattern,
			}
		}
	}

	switch method {
	case "GET", "HEAD", "OPTIONS":
		return domain.TierResult{Tier: domain.TierRead, Reason: method + " is read-only", RuleMatched: "method:" + method}
	case "POST", "PUT", "PATCH":
		return domain.TierResult{Tier: domain.TierSafeWrite, Reason: method + " is a safe write", RuleMatched: "method:" + method}
	case "DELETE":
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "DELETE is destructive", RuleMatched: "method:DELETE"}
	default:
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "unknown HTTP method", RuleMatched: "default"}
	}
}
