package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

func TestAuditStoreWriteAndQuery(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewAuditStore(pool)
	ctx := context.Background()
	tenantID := uuid.New()
	workspaceID := uuid.New()

	entry := domain.AuditEntry{
		ID:          uuid.New(),
		TenantID:    tenantID,
		WorkspaceID: workspaceID,
		Timestamp:   time.Now().UTC().Truncate(time.Microsecond),
		Tier:        domain.TierRead,
		Operation:   "SQL:SELECT",
		Target:      "staging-db.users",
		Caller:      "claude-agent",
		Decision:    domain.DecisionAllowed,
		DurationMs:  5,
		Detail:      "SELECT * FROM users WHERE id = 1",
	}

	if err := store.Write(ctx, entry); err != nil {
		t.Fatalf("Write: %v", err)
	}

	entries, err := store.Query(ctx, ports.AuditFilter{
		TenantID: tenantID,
		Limit:    10,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	got := entries[0]
	if got.ID != entry.ID {
		t.Errorf("ID = %v, want %v", got.ID, entry.ID)
	}
	if got.Tier != entry.Tier {
		t.Errorf("Tier = %v, want %v", got.Tier, entry.Tier)
	}
	if got.Decision != entry.Decision {
		t.Errorf("Decision = %v, want %v", got.Decision, entry.Decision)
	}
	if got.Operation != entry.Operation {
		t.Errorf("Operation = %v, want %v", got.Operation, entry.Operation)
	}
	if got.Detail != entry.Detail {
		t.Errorf("Detail = %v, want %v", got.Detail, entry.Detail)
	}
}

func TestAuditStoreFilterByTier(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewAuditStore(pool)
	ctx := context.Background()
	tenantID := uuid.New()
	workspaceID := uuid.New()

	// Write entries at different tiers
	tiers := []domain.Tier{domain.TierRead, domain.TierRead, domain.TierSafeWrite, domain.TierDestructive, domain.TierForbidden}
	for _, tier := range tiers {
		entry := domain.AuditEntry{
			ID:          uuid.New(),
			TenantID:    tenantID,
			WorkspaceID: workspaceID,
			Timestamp:   time.Now().UTC(),
			Tier:        tier,
			Operation:   "SQL:TEST",
			Target:      "test",
			Caller:      "test",
			Decision:    domain.DecisionAllowed,
		}
		if err := store.Write(ctx, entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Query tier >= 3
	tierMin := domain.TierDestructive
	entries, err := store.Query(ctx, ports.AuditFilter{
		TenantID: tenantID,
		TierMin:  &tierMin,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (tier 3 + 4), got %d", len(entries))
	}
}

func TestAuditStoreFilterByDecision(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewAuditStore(pool)
	ctx := context.Background()
	tenantID := uuid.New()
	workspaceID := uuid.New()

	decisions := []domain.Decision{domain.DecisionAllowed, domain.DecisionAllowed, domain.DecisionBlocked}
	for _, dec := range decisions {
		entry := domain.AuditEntry{
			ID:          uuid.New(),
			TenantID:    tenantID,
			WorkspaceID: workspaceID,
			Timestamp:   time.Now().UTC(),
			Tier:        domain.TierRead,
			Operation:   "SQL:TEST",
			Target:      "test",
			Caller:      "test",
			Decision:    dec,
		}
		if err := store.Write(ctx, entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	blocked := domain.DecisionBlocked
	entries, err := store.Query(ctx, ports.AuditFilter{
		TenantID: tenantID,
		Decision: &blocked,
	})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 blocked entry, got %d", len(entries))
	}
}

func TestAuditStoreTenantIsolation(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}

	pool, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewAuditStore(pool)
	ctx := context.Background()
	tenant1 := uuid.New()
	tenant2 := uuid.New()

	// Write 1 entry per tenant
	for _, tid := range []uuid.UUID{tenant1, tenant2} {
		entry := domain.AuditEntry{
			ID:          uuid.New(),
			TenantID:    tid,
			WorkspaceID: uuid.New(),
			Timestamp:   time.Now().UTC(),
			Tier:        domain.TierRead,
			Operation:   "SQL:TEST",
			Target:      "test",
			Caller:      "test",
			Decision:    domain.DecisionAllowed,
		}
		if err := store.Write(ctx, entry); err != nil {
			t.Fatalf("Write: %v", err)
		}
	}

	// Tenant 1 should only see their entry
	entries, err := store.Query(ctx, ports.AuditFilter{TenantID: tenant1})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("tenant isolation: expected 1, got %d", len(entries))
	}
}
