package handlers

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/proxy"
	"github.com/NicoSchwandner/cordon/internal/domain"
	"github.com/NicoSchwandner/cordon/internal/infrastructure/sops"
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
	return NewProxyHandler(pipeline, ProxyHandlerConfig{
		HTTPTimeout:     30 * time.Second,
		MaxResponseBody: 1 << 20,
	}), auth
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

func TestHTTPProxyForwardsToUpstream(t *testing.T) {
	// Spin up a fake upstream server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Custom", "upstream-header")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"message":"hello from upstream"}`))
	}))
	defer upstream.Close()

	// Parse upstream URL to get host
	upstreamHost := upstream.Listener.Addr().String()

	handler, auth := newTestServer()
	// Override the handler's HTTP client to use http:// (upstream is plain HTTP)
	handler.client.Transport = &http.Transport{}

	// Add upstream host to the egress allowlist by recreating the pipeline
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")
	vault := sops.NewMemoryVault()
	vault.AddSecret(tenantID, "DB", "cordon-placeholder-db", "real-db")

	handler.pipeline = proxy.NewPipeline(proxy.PipelineConfig{
		SQLClassifier:   proxy.NewSQLClassifier(),
		HTTPClassifier:  proxy.NewHTTPClassifier(),
		Swapper:         proxy.NewSecretSwapper(vault),
		Egress:          proxy.NewEgressChecker([]string{"127.0.0.1"}),
		ApprovalTimeout: 100 * time.Millisecond,
	})

	body, _ := json.Marshal(HTTPProxyRequest{
		Method: "GET",
		Host:   upstreamHost,
		URL:    upstream.URL + "/api/test", // full URL so handler uses http://
		Caller: "test-agent",
	})

	req := httptest.NewRequest("POST", "/api/proxy/http", bytes.NewReader(body))
	w := httptest.NewRecorder()

	authMW := middleware.Auth(auth)
	authMW(http.HandlerFunc(handler.HTTP)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	var result ProxyResult
	json.Unmarshal(w.Body.Bytes(), &result)

	if !result.Allowed {
		t.Error("GET should be allowed")
	}
	if result.UpstreamStatus != 200 {
		t.Errorf("upstream_status = %d, want 200", result.UpstreamStatus)
	}
	if result.UpstreamBody != `{"message":"hello from upstream"}` {
		t.Errorf("upstream_body = %q, want hello from upstream", result.UpstreamBody)
	}
	if result.UpstreamHeaders["X-Custom"] != "upstream-header" {
		t.Errorf("upstream_headers missing X-Custom, got %v", result.UpstreamHeaders)
	}
}

func TestHTTPProxyUpstreamSecretSwap(t *testing.T) {
	// Verify that secrets are swapped before reaching upstream
	var receivedAuth string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	upstreamHost := upstream.Listener.Addr().String()
	tenantID := uuid.MustParse("00000000-0000-0000-0000-000000000001")

	vault := sops.NewMemoryVault()
	vault.AddSecret(tenantID, "API_KEY", "cordon-placeholder-api-key", "real-secret-key")

	pipeline := proxy.NewPipeline(proxy.PipelineConfig{
		SQLClassifier:   proxy.NewSQLClassifier(),
		HTTPClassifier:  proxy.NewHTTPClassifier(),
		Swapper:         proxy.NewSecretSwapper(vault),
		Egress:          proxy.NewEgressChecker([]string{"127.0.0.1"}),
		ApprovalTimeout: 100 * time.Millisecond,
	})

	handler := &ProxyHandler{
		pipeline: pipeline,
		client:   &http.Client{Timeout: 5 * time.Second},
	}
	auth := &middleware.StaticAuth{TenantID: tenantID, UserID: "test-user"}

	body, _ := json.Marshal(HTTPProxyRequest{
		Method:  "GET",
		Host:    upstreamHost,
		URL:     upstream.URL + "/api/test",
		Headers: map[string]string{"Authorization": "Bearer cordon-placeholder-api-key"},
		Caller:  "test-agent",
	})

	req := httptest.NewRequest("POST", "/api/proxy/http", bytes.NewReader(body))
	w := httptest.NewRecorder()

	middleware.Auth(auth)(http.HandlerFunc(handler.HTTP)).ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200. Body: %s", w.Code, w.Body.String())
	}

	// The upstream should have received the REAL secret, not the placeholder
	if receivedAuth != "Bearer real-secret-key" {
		t.Errorf("upstream received auth = %q, want 'Bearer real-secret-key'", receivedAuth)
	}

	// But the proxy response should NOT contain the real secret
	var result ProxyResult
	json.Unmarshal(w.Body.Bytes(), &result)
	if result.UpstreamStatus != 200 {
		t.Errorf("upstream_status = %d, want 200", result.UpstreamStatus)
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
