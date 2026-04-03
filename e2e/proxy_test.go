//go:build e2e

package e2e

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"
)

var serverURL string

func init() {
	serverURL = os.Getenv("CORDON_SERVER")
	if serverURL == "" {
		serverURL = "http://localhost:8443"
	}
}

type sqlRequest struct {
	Query  string `json:"query"`
	Target string `json:"target"`
	Caller string `json:"caller"`
}

type httpProxyRequest struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	Host   string `json:"host"`
	Caller string `json:"caller"`
}

type proxyResult struct {
	Allowed  bool   `json:"allowed"`
	Tier     int    `json:"tier"`
	Decision string `json:"decision"`
}

type problemDetails struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func postSQL(t *testing.T, query, caller string) (*http.Response, []byte) {
	t.Helper()
	body, _ := json.Marshal(sqlRequest{Query: query, Caller: caller})
	resp, err := http.Post(serverURL+"/api/proxy/sql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

func postHTTP(t *testing.T, method, host, url, caller string) (*http.Response, []byte) {
	t.Helper()
	body, _ := json.Marshal(httpProxyRequest{Method: method, Host: host, URL: url, Caller: caller})
	resp, err := http.Post(serverURL+"/api/proxy/http", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, data
}

// Acceptance Scenario 1: Tier 1 Read flows through
func TestE2E_Tier1ReadAllowed(t *testing.T) {
	resp, data := postSQL(t, "SELECT * FROM users WHERE id = 1 LIMIT 10", "claude-agent")
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, body = %s", resp.StatusCode, string(data))
	}
	var result proxyResult
	json.Unmarshal(data, &result)
	if !result.Allowed || result.Tier != 1 {
		t.Errorf("expected tier 1 allowed, got tier=%d allowed=%v", result.Tier, result.Allowed)
	}
}

// Acceptance Scenario 5: Tier 4 always blocked
func TestE2E_Tier4AlwaysBlocked(t *testing.T) {
	queries := []string{
		"TRUNCATE TABLE users",
		"DROP DATABASE production",
		"GRANT ALL ON users TO admin",
		"REVOKE SELECT ON users FROM readonly",
	}

	for _, q := range queries {
		resp, data := postSQL(t, q, "claude-agent")
		if resp.StatusCode != 403 {
			t.Errorf("query %q: expected 403, got %d", q, resp.StatusCode)
			continue
		}
		var problem problemDetails
		json.Unmarshal(data, &problem)
		if problem.Code != "tier_blocked" {
			t.Errorf("query %q: expected tier_blocked, got %s", q, problem.Code)
		}
	}
}

// Acceptance Scenario 6: Egress blocking
func TestE2E_EgressBlocking(t *testing.T) {
	// Blocked host
	resp, data := postHTTP(t, "POST", "evil-exfil.com", "/steal", "malicious-dep")
	if resp.StatusCode != 403 {
		t.Fatalf("expected 403, got %d", resp.StatusCode)
	}
	var problem problemDetails
	json.Unmarshal(data, &problem)
	if problem.Code != "egress_denied" {
		t.Errorf("expected egress_denied, got %s", problem.Code)
	}

	// Allowed host
	resp, data = postHTTP(t, "GET", "github.com", "/api/repos", "agent")
	if resp.StatusCode != 200 {
		t.Fatalf("github.com should be allowed, got %d: %s", resp.StatusCode, string(data))
	}
}

// Acceptance Scenario 7: Secret swap transparency
func TestE2E_SecretSwapTransparency(t *testing.T) {
	// Query with placeholder — should be allowed and audit should not contain real cred
	resp, data := postSQL(t,
		"SELECT * FROM users WHERE conn = 'cordon-placeholder-database-url' AND id = 1",
		"agent")
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d: %s", resp.StatusCode, string(data))
	}

	// Check audit log
	time.Sleep(100 * time.Millisecond) // brief delay for audit write
	auditResp, err := http.Get(serverURL + "/api/audit?limit=1")
	if err != nil {
		t.Fatalf("audit request failed: %v", err)
	}
	defer auditResp.Body.Close()
	auditData, _ := io.ReadAll(auditResp.Body)

	// Real credentials must never appear in audit
	auditStr := string(auditData)
	if bytes.Contains([]byte(auditStr), []byte("real:secret")) {
		t.Error("SECURITY VIOLATION: real database credential found in audit log")
	}
	if bytes.Contains([]byte(auditStr), []byte("sk-real-key")) {
		t.Error("SECURITY VIOLATION: real API key found in audit log")
	}
}

// Test audit log entries exist and have correct structure
func TestE2E_AuditLogStructure(t *testing.T) {
	// Generate some operations first
	postSQL(t, "SELECT 1", "test-agent")
	postSQL(t, "TRUNCATE TABLE users", "test-agent")
	time.Sleep(100 * time.Millisecond)

	resp, err := http.Get(serverURL + "/api/audit")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)

	var entries []struct {
		ID          string `json:"id"`
		TenantID    string `json:"tenant_id"`
		Timestamp   string `json:"timestamp"`
		Tier        int    `json:"tier"`
		TierName    string `json:"tier_name"`
		Operation   string `json:"operation"`
		Decision    string `json:"decision"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parsing audit: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("expected audit entries, got none")
	}

	for _, e := range entries {
		if e.ID == "" || e.TenantID == "" || e.Timestamp == "" {
			t.Errorf("audit entry missing required fields: %+v", e)
		}
		if e.Tier < 1 || e.Tier > 4 {
			t.Errorf("invalid tier %d", e.Tier)
		}
		if e.Decision != "allowed" && e.Decision != "blocked" && e.Decision != "denied" {
			t.Errorf("unexpected decision: %s", e.Decision)
		}
	}
}

// Health check
func TestE2E_HealthEndpoint(t *testing.T) {
	resp, err := http.Get(serverURL + "/health")
	if err != nil {
		t.Fatalf("health check failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("health status = %d", resp.StatusCode)
	}
}

func TestE2E_ReadyEndpoint(t *testing.T) {
	resp, err := http.Get(serverURL + "/ready")
	if err != nil {
		t.Fatalf("ready check failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("ready status = %d", resp.StatusCode)
	}
}
