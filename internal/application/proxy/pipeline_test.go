package proxy

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
	"github.com/nicobistolfi/cordon/internal/infrastructure/sops"
)

// mockAuditStore records Write calls for verification.
type mockAuditStore struct {
	entries []domain.AuditEntry
}

func (m *mockAuditStore) Write(_ context.Context, entry domain.AuditEntry) error {
	m.entries = append(m.entries, entry)
	return nil
}

func (m *mockAuditStore) Query(_ context.Context, _ ports.AuditFilter) ([]domain.AuditEntry, error) {
	return m.entries, nil
}

// mockApprovalService for testing approval flows.
type mockApprovalService struct {
	grants    map[string]*domain.ApprovalGrant // key: operation+target
	decisions map[uuid.UUID]domain.ApprovalGrant
	requests  []domain.ApprovalRequest
}

func newMockApproval() *mockApprovalService {
	return &mockApprovalService{
		grants:    make(map[string]*domain.ApprovalGrant),
		decisions: make(map[uuid.UUID]domain.ApprovalGrant),
	}
}

func (m *mockApprovalService) RequestApproval(_ context.Context, req domain.ApprovalRequest) error {
	m.requests = append(m.requests, req)
	return nil
}

func (m *mockApprovalService) AwaitDecision(_ context.Context, requestID uuid.UUID, _ time.Duration) (domain.ApprovalGrant, error) {
	if grant, ok := m.decisions[requestID]; ok {
		return grant, nil
	}
	// Check if there's a pending decision for any request
	for _, req := range m.requests {
		if req.ID == requestID {
			if grant, ok := m.decisions[requestID]; ok {
				return grant, nil
			}
		}
	}
	return domain.ApprovalGrant{}, errors.New("timeout")
}

func (m *mockApprovalService) Decide(_ context.Context, requestID uuid.UUID, grant domain.ApprovalGrant) error {
	m.decisions[requestID] = grant
	return nil
}

func (m *mockApprovalService) CheckExistingGrant(_ context.Context, _, _ uuid.UUID, operation, target string) (*domain.ApprovalGrant, error) {
	key := operation + ":" + target
	if grant, ok := m.grants[key]; ok {
		return grant, nil
	}
	return nil, nil
}

func (m *mockApprovalService) AddGrant(operation, target string, grant domain.ApprovalGrant) {
	m.grants[operation+":"+target] = &grant
}

func newTestPipeline(audit *mockAuditStore, approval *mockApprovalService, egress *EgressChecker) *Pipeline {
	vault := sops.NewMemoryVault()
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	vault.AddSecret(tenantID, "DB_URL", "cordon-placeholder-db", "real-db-url")

	return NewPipeline(PipelineConfig{
		SQLClassifier:  NewSQLClassifier(),
		HTTPClassifier: NewHTTPClassifier(),
		Swapper:        NewSecretSwapper(vault),
		Audit:          audit,
		Approver:       approval,
		Egress:         egress,
		ApprovalTimeout: 100 * time.Millisecond,
	})
}

func TestPipelineTier1Allowed(t *testing.T) {
	audit := &mockAuditStore{}
	pipeline := newTestPipeline(audit, nil, nil)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeSQL,
		SQLQuery:    "SELECT * FROM users WHERE id = 1",
		TenantID:    uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if !resp.Allowed {
		t.Error("Tier 1 SELECT should be allowed")
	}
	if resp.Tier != domain.TierRead {
		t.Errorf("tier = %v, want Read", resp.Tier)
	}
	if len(audit.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(audit.entries))
	}
	if audit.entries[0].Decision != domain.DecisionAllowed {
		t.Errorf("audit decision = %v, want allowed", audit.entries[0].Decision)
	}
}

func TestPipelineTier4Blocked(t *testing.T) {
	audit := &mockAuditStore{}
	pipeline := newTestPipeline(audit, nil, nil)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeSQL,
		SQLQuery:    "TRUNCATE TABLE users",
		TenantID:    uuid.New(),
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if resp.Allowed {
		t.Error("Tier 4 TRUNCATE should be blocked")
	}
	if resp.Tier != domain.TierForbidden {
		t.Errorf("tier = %v, want Forbidden", resp.Tier)
	}
	if resp.Decision != domain.DecisionBlocked {
		t.Errorf("decision = %v, want blocked", resp.Decision)
	}
	if resp.Problem == nil || resp.Problem.Code != "tier_blocked" {
		t.Error("expected tier_blocked problem")
	}
	if len(audit.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(audit.entries))
	}
	if audit.entries[0].Decision != domain.DecisionBlocked {
		t.Errorf("audit decision = %v, want blocked", audit.entries[0].Decision)
	}
}

func TestPipelineTier3WithExistingGrant(t *testing.T) {
	audit := &mockAuditStore{}
	approval := newMockApproval()
	approval.AddGrant("SQL", "", domain.ApprovalGrant{
		Scope:     domain.GrantSession,
		ExpiresAt: time.Now().Add(time.Hour),
	})

	pipeline := newTestPipeline(audit, approval, nil)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeSQL,
		SQLQuery:    "DELETE FROM test_sessions WHERE id = 1",
		TenantID:    uuid.MustParse("00000000-0000-0000-0000-000000000001"),
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if !resp.Allowed {
		t.Error("Tier 3 with existing grant should be allowed")
	}
}

func TestPipelineTier3WithoutGrant(t *testing.T) {
	audit := &mockAuditStore{}
	approval := newMockApproval()
	// No grants, no pre-set decisions → AwaitDecision will return error (timeout)

	pipeline := newTestPipeline(audit, approval, nil)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeSQL,
		SQLQuery:    "DELETE FROM users WHERE id = 1",
		TenantID:    uuid.New(),
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if resp.Allowed {
		t.Error("Tier 3 without grant should not be allowed")
	}
	if resp.Problem == nil || resp.Problem.Code != "approval_timeout" {
		t.Errorf("expected approval_timeout problem, got %v", resp.Problem)
	}
}

func TestPipelineEgressBlocked(t *testing.T) {
	audit := &mockAuditStore{}
	egress := NewEgressChecker([]string{"github.com", "nuget.org"})
	pipeline := newTestPipeline(audit, nil, egress)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeHTTP,
		Method:      "POST",
		Host:        "evil-exfil.com",
		Path:        "/steal",
		TenantID:    uuid.New(),
		WorkspaceID: uuid.New(),
		Caller:      "malicious-dep",
	})

	if resp.Allowed {
		t.Error("egress to non-allowlisted host should be blocked")
	}
	if resp.Problem == nil || resp.Problem.Code != "egress_denied" {
		t.Errorf("expected egress_denied problem, got %v", resp.Problem)
	}
}

func TestPipelineSecretSwapInAudit(t *testing.T) {
	audit := &mockAuditStore{}
	pipeline := newTestPipeline(audit, nil, nil)
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeSQL,
		SQLQuery:    "SELECT * FROM users WHERE conn = 'cordon-placeholder-db' AND id = 1",
		TenantID:    tenantID,
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if !resp.Allowed {
		t.Fatal("should be allowed")
	}

	// The audit detail must NOT contain the real credential
	if len(audit.entries) != 1 {
		t.Fatalf("expected 1 audit entry, got %d", len(audit.entries))
	}
	detail := audit.entries[0].Detail
	if containsString(detail, "real-db-url") {
		t.Errorf("audit detail contains real credential: %s", detail)
	}
}

func TestPipelineHTTPTier2(t *testing.T) {
	audit := &mockAuditStore{}
	pipeline := newTestPipeline(audit, nil, nil)

	resp := pipeline.Process(context.Background(), ProxyRequest{
		QueryType:   QueryTypeHTTP,
		Method:      "POST",
		Path:        "/api/users",
		TenantID:    uuid.New(),
		WorkspaceID: uuid.New(),
		Caller:      "test-agent",
	})

	if !resp.Allowed {
		t.Error("HTTP POST should be allowed (Tier 2)")
	}
	if resp.Tier != domain.TierSafeWrite {
		t.Errorf("tier = %v, want safe_write", resp.Tier)
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstr(s, substr))
}

func containsSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
