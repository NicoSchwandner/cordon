package domain

import "fmt"

// Tier represents the risk level of an operation.
type Tier int

const (
	TierRead        Tier = 1
	TierSafeWrite   Tier = 2
	TierDestructive Tier = 3
	TierForbidden   Tier = 4
)

func (t Tier) String() string {
	switch t {
	case TierRead:
		return "read"
	case TierSafeWrite:
		return "safe_write"
	case TierDestructive:
		return "destructive"
	case TierForbidden:
		return "forbidden"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

func (t Tier) IsBlocked() bool {
	return t == TierForbidden
}

// TierResult is the outcome of classifying an operation.
type TierResult struct {
	Tier        Tier
	Reason      string
	RuleMatched string
	Err         error
}
