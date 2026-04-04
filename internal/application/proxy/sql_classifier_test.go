package proxy

import (
	"testing"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

func TestSQLClassifier(t *testing.T) {
	c := NewSQLClassifier()

	tests := []struct {
		name      string
		query     string
		overrides []ports.TierOverride
		wantTier  domain.Tier
	}{
		// Tier 1 — Read
		{"simple select", "SELECT * FROM users WHERE id = 1", nil, domain.TierRead},
		{"select with limit", "SELECT * FROM users LIMIT 10", nil, domain.TierRead},
		{"select count", "SELECT count(*) FROM users", nil, domain.TierRead},
		{"select sum", "SELECT SUM(amount) FROM orders", nil, domain.TierRead},
		{"select avg", "SELECT AVG(price) FROM products", nil, domain.TierRead},
		{"explain", "EXPLAIN SELECT * FROM users", nil, domain.TierRead},
		{"explain analyze", "EXPLAIN ANALYZE SELECT * FROM users", nil, domain.TierRead},
		{"show", "SHOW tables", nil, domain.TierRead},
		{"describe", "DESCRIBE users", nil, domain.TierRead},
		{"with cte select", "WITH cte AS (SELECT 1) SELECT * FROM cte", nil, domain.TierRead},
		{"select literal", "SELECT 1", nil, domain.TierRead},
		{"select now", "SELECT now()", nil, domain.TierRead},
		{"select version", "SELECT version()", nil, domain.TierRead},
		{"begin", "BEGIN", nil, domain.TierRead},
		{"commit", "COMMIT", nil, domain.TierRead},
		{"rollback", "ROLLBACK", nil, domain.TierRead},

		// Tier 2 — Safe Write
		{"insert", "INSERT INTO users (name) VALUES ('alice')", nil, domain.TierSafeWrite},
		{"insert select", "INSERT INTO archive SELECT * FROM users WHERE deleted = true", nil, domain.TierSafeWrite},

		// Tier 3 — Destructive
		{"update", "UPDATE users SET name = 'bob' WHERE id = 1", nil, domain.TierDestructive},
		{"delete", "DELETE FROM test_sessions WHERE created_at < '2025-01-01'", nil, domain.TierDestructive},
		{"drop table", "DROP TABLE temp_data", nil, domain.TierDestructive},
		{"unbounded select", "SELECT * FROM users", nil, domain.TierDestructive},
		{"unknown statement", "MERGE INTO users USING ...", nil, domain.TierDestructive},
		{"empty query", "", nil, domain.TierDestructive},

		// Tier 4 — Forbidden
		{"truncate", "TRUNCATE TABLE users", nil, domain.TierForbidden},
		{"drop database", "DROP DATABASE production", nil, domain.TierForbidden},
		{"grant", "GRANT ALL ON users TO admin", nil, domain.TierForbidden},
		{"revoke", "REVOKE SELECT ON users FROM readonly", nil, domain.TierForbidden},

		// Overrides
		{"override escalates delete to forbidden", "DELETE FROM audit_log WHERE id = 1", []ports.TierOverride{
			{Pattern: "DELETE FROM audit_log", Tier: domain.TierForbidden},
		}, domain.TierForbidden},
		{"override de-escalates", "DROP TABLE temp_cache", []ports.TierOverride{
			{Pattern: "DROP TABLE temp_cache", Tier: domain.TierSafeWrite},
		}, domain.TierSafeWrite},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := c.ClassifySQL(tt.query, tt.overrides)
			if result.Tier != tt.wantTier {
				t.Errorf("ClassifySQL(%q) = tier %v (%s), want %v\n  reason: %s\n  rule: %s",
					tt.query, result.Tier, result.Tier, tt.wantTier, result.Reason, result.RuleMatched)
			}
		})
	}
}

func TestSQLClassifierCaseInsensitive(t *testing.T) {
	c := NewSQLClassifier()

	result := c.ClassifySQL("select * from users where id = 1", nil)
	if result.Tier != domain.TierRead {
		t.Errorf("lowercase select should be Tier 1, got %v", result.Tier)
	}

	result = c.ClassifySQL("  SELECT * FROM users WHERE id = 1  ", nil)
	if result.Tier != domain.TierRead {
		t.Errorf("whitespace-padded select should be Tier 1, got %v", result.Tier)
	}
}
