package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"github.com/nicobistolfi/cordon/internal/application/proxy"
	"github.com/nicobistolfi/cordon/internal/domain"
	"github.com/nicobistolfi/cordon/internal/infrastructure/sops"
)

// mockAuditStore for handler tests
type mockAuditStore struct {
	entries []domain.AuditEntry
}

func (m *mockAuditStore) Write(_ interface{ Deadline() (time.Time, bool) }, entry domain.AuditEntry) error {
	m.entries = append(m.entries, entry)
	return nil
}

func newTestServer() (*ProxyHandler, *middleware.StaticAuth) {
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	vault := sops.NewMemoryVault()
	vault.AddSecret(tenantID, "DB", "cordon-placeholder-db", "real-db")

	pipeline := proxy.NewPipeline(proxy.PipelineConfig{
		SQLClassifier:  proxy.NewSQLClassifier(),
		HTTPClassifier: proxy.NewHTTPClassifier(),
		Swapper:        proxy.NewSecretSwapper(vault),
		Egress:         proxy.NewEgressChecker([]string{"github.com"}),
		ApprovalTimeout: 100 * time.Millisecond,
	})

	auth := &middleware.StaticAuth{TenantID: tenantID, UserID: "test-user"}
	return NewProxyHandler(pipeline), auth
}

func TestSQLProxyTier1(t *testing.T) {
	handler, auth := newTestServer()

	body, _ := json.Marshal(SQLRequest{
		Query:  "SELECT * FROM users WHERE id = 1",
		Target: "staging-db",
		Caller: "test-agent",
	})

	req := httptest.NewRequest("POST", "/api/proxy/sql", bytes.NewReader(body))
	w := httptest.NewRecorder()

	// Wrap with auth middleware
	authMW := middleware.Auth(auth)
	authMW(http.HandlerFunc(handler.SQL)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var result ProxyResult
	json.Unmarshal(w.Body.Bytes(), &result)

	if !result.Allowed {
		t.Error("SELECT should be allowed")
	}
	if result.Tier != 1 {
		t.Errorf("tier = %d, want 1", result.Tier)
	}
}

func TestSQLProxyTier4Blocked(t *testing.T) {
	handler, auth := newTestServer()

	body, _ := json.Marshal(SQLRequest{
		Query:  "TRUNCATE TABLE users",
		Caller: "test-agent",
	})

	req := httptest.NewRequest("POST", "/api/proxy/sql", bytes.NewReader(body))
	w := httptest.NewRecorder()

	authMW := middleware.Auth(auth)
	authMW(http.HandlerFunc(handler.SQL)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403. Body: %s", w.Code, w.Body.String())
	}

	var problem domain.ProblemDetails
	json.Unmarshal(w.Body.Bytes(), &problem)

	if problem.Code != "tier_blocked" {
		t.Errorf("code = %s, want tier_blocked", problem.Code)
	}
}

func TestSQLProxyEgressBlocked(t *testing.T) {
	handler, auth := newTestServer()

	body, _ := json.Marshal(HTTPProxyRequest{
		Method: "POST",
		Host:   "evil-exfil.com",
		URL:    "/steal",
		Caller: "malicious",
	})

	req := httptest.NewRequest("POST", "/api/proxy/http", bytes.NewReader(body))
	w := httptest.NewRecorder()

	authMW := middleware.Auth(auth)
	authMW(http.HandlerFunc(handler.HTTP)).ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403. Body: %s", w.Code, w.Body.String())
	}

	var problem domain.ProblemDetails
	json.Unmarshal(w.Body.Bytes(), &problem)

	if problem.Code != "egress_denied" {
		t.Errorf("code = %s, want egress_denied", problem.Code)
	}
}

func TestUnauthenticatedRequest(t *testing.T) {
	handler, _ := newTestServer()

	body, _ := json.Marshal(SQLRequest{Query: "SELECT 1"})
	req := httptest.NewRequest("POST", "/api/proxy/sql", bytes.NewReader(body))
	w := httptest.NewRecorder()

	// Use token auth with no valid token
	tokenAuth := &middleware.TokenAuth{Tokens: map[string]middleware.TokenInfo{
		"valid-token": {TenantID: uuid.New(), UserID: "user"},
	}}
	authMW := middleware.Auth(tokenAuth)
	authMW(http.HandlerFunc(handler.SQL)).ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}
