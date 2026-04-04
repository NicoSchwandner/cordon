package proxy

import (
	"strings"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// SQLClassifier assigns tiers to SQL queries using keyword analysis.
type SQLClassifier struct{}

func NewSQLClassifier() *SQLClassifier {
	return &SQLClassifier{}
}

func (c *SQLClassifier) ClassifySQL(query string, overrides []ports.TierOverride) domain.TierResult {
	normalized := strings.TrimSpace(strings.ToUpper(query))
	if normalized == "" {
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "empty query", RuleMatched: "default"}
	}

	// Check overrides first — they take highest precedence
	for _, o := range overrides {
		if strings.Contains(strings.ToUpper(query), strings.ToUpper(o.Pattern)) {
			return domain.TierResult{
				Tier:        o.Tier,
				Reason:      "matched override pattern",
				RuleMatched: "override:" + o.Pattern,
			}
		}
	}

	keyword := firstKeyword(normalized)

	switch keyword {
	case "TRUNCATE":
		return tier4("TRUNCATE is forbidden")
	case "GRANT", "REVOKE":
		return tier4(keyword + " is forbidden")
	case "DROP":
		if containsWord(normalized, "DATABASE") {
			return tier4("DROP DATABASE is forbidden")
		}
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "DROP statement", RuleMatched: "keyword:DROP"}
	case "DELETE":
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "DELETE statement", RuleMatched: "keyword:DELETE"}
	case "UPDATE":
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "UPDATE statement", RuleMatched: "keyword:UPDATE"}
	case "INSERT":
		return domain.TierResult{Tier: domain.TierSafeWrite, Reason: "INSERT statement", RuleMatched: "keyword:INSERT"}
	case "SELECT":
		return classifySelect(normalized)
	case "EXPLAIN", "SHOW", "DESCRIBE", "DESC":
		return domain.TierResult{Tier: domain.TierRead, Reason: keyword + " is read-only", RuleMatched: "keyword:" + keyword}
	case "WITH":
		return classifyWith(normalized)
	case "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT":
		return domain.TierResult{Tier: domain.TierRead, Reason: "transaction control", RuleMatched: "keyword:" + keyword}
	default:
		return domain.TierResult{Tier: domain.TierDestructive, Reason: "unknown statement type", RuleMatched: "default"}
	}
}

func classifySelect(normalized string) domain.TierResult {
	if isUnboundedRead(normalized) {
		return domain.TierResult{
			Tier:        domain.TierDestructive,
			Reason:      "unbounded SELECT without WHERE or LIMIT",
			RuleMatched: "bulk_read",
		}
	}
	return domain.TierResult{Tier: domain.TierRead, Reason: "SELECT statement", RuleMatched: "keyword:SELECT"}
}

func classifyWith(normalized string) domain.TierResult {
	// CTEs: find the final statement after the CTE definition
	// Look for the main query after the last closing paren
	idx := strings.LastIndex(normalized, ")")
	if idx >= 0 && idx < len(normalized)-1 {
		remainder := strings.TrimSpace(normalized[idx+1:])
		kw := firstKeyword(remainder)
		switch kw {
		case "SELECT":
			// CTE SELECTs read from a finite CTE result — don't flag as unbounded
			return domain.TierResult{Tier: domain.TierRead, Reason: "CTE with SELECT", RuleMatched: "keyword:WITH+SELECT"}
		case "INSERT":
			return domain.TierResult{Tier: domain.TierSafeWrite, Reason: "CTE with INSERT", RuleMatched: "keyword:WITH+INSERT"}
		case "UPDATE":
			return domain.TierResult{Tier: domain.TierDestructive, Reason: "CTE with UPDATE", RuleMatched: "keyword:WITH+UPDATE"}
		case "DELETE":
			return domain.TierResult{Tier: domain.TierDestructive, Reason: "CTE with DELETE", RuleMatched: "keyword:WITH+DELETE"}
		}
	}
	// Default: treat WITH as read if we can't determine the final statement
	return domain.TierResult{Tier: domain.TierRead, Reason: "CTE query", RuleMatched: "keyword:WITH"}
}

func isUnboundedRead(normalized string) bool {
	// No FROM clause = not a table query (e.g., "SELECT 1", "SELECT now()")
	if !containsWord(normalized, "FROM") {
		return false
	}
	// Aggregates are always bounded
	if strings.Contains(normalized, "COUNT(") || strings.Contains(normalized, "SUM(") ||
		strings.Contains(normalized, "AVG(") || strings.Contains(normalized, "MAX(") ||
		strings.Contains(normalized, "MIN(") {
		return false
	}
	hasWhere := containsWord(normalized, "WHERE")
	hasLimit := containsWord(normalized, "LIMIT")
	return !hasWhere && !hasLimit
}

func firstKeyword(s string) string {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

func containsWord(s, word string) bool {
	return strings.Contains(" "+s+" ", " "+word+" ")
}

func tier4(reason string) domain.TierResult {
	return domain.TierResult{Tier: domain.TierForbidden, Reason: reason, RuleMatched: "keyword:forbidden"}
}
