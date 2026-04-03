package config

import (
	"testing"
)

func TestParseValidConfig(t *testing.T) {
	yaml := `
version: "1"
name: "my-project"
secrets:
  - name: DATABASE_URL
    placeholder: "cordon-placeholder-database-url"
  - name: API_KEY
    placeholder: "cordon-placeholder-api-key"
services:
  - name: database
    type: postgres
    target: "staging-db.example.com:5432"
    tier_overrides:
      - pattern: "DELETE FROM audit_log"
        tier: 4
egress:
  allowlist:
    - "*.example.com"
    - "registry.npmjs.org"
    - "github.com"
workspace:
  devcontainer: ".devcontainer/devcontainer.json"
  resources:
    cpu: 2
    memory: "4Gi"
  idle_timeout: "15m"
  max_lifetime: "24h"
  tools:
    - claude-code
  allow_ssh: true
approval:
  tier3_timeout_seconds: 300
  allow_session_grants: true
  allow_pattern_grants: true
`
	cfg, err := Parse([]byte(yaml))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if cfg.Version != "1" {
		t.Errorf("version = %s, want 1", cfg.Version)
	}
	if cfg.Name != "my-project" {
		t.Errorf("name = %s, want my-project", cfg.Name)
	}
	if len(cfg.Secrets) != 2 {
		t.Errorf("secrets count = %d, want 2", len(cfg.Secrets))
	}
	if len(cfg.Services) != 1 {
		t.Errorf("services count = %d, want 1", len(cfg.Services))
	}
	if len(cfg.Services[0].TierOverrides) != 1 {
		t.Errorf("tier overrides = %d, want 1", len(cfg.Services[0].TierOverrides))
	}
	if cfg.Services[0].TierOverrides[0].Tier != 4 {
		t.Errorf("override tier = %d, want 4", cfg.Services[0].TierOverrides[0].Tier)
	}
	if len(cfg.Egress.Allowlist) != 3 {
		t.Errorf("egress allowlist = %d, want 3", len(cfg.Egress.Allowlist))
	}
	if !cfg.Workspace.AllowSSH {
		t.Error("allow_ssh should be true")
	}
	if cfg.Approval.Tier3TimeoutSeconds != 300 {
		t.Errorf("timeout = %d, want 300", cfg.Approval.Tier3TimeoutSeconds)
	}

	overrides := ToTierOverrides(cfg)
	if len(overrides) != 1 {
		t.Errorf("tier overrides = %d, want 1", len(overrides))
	}

	refs := ToSecretRefs(cfg)
	if len(refs) != 2 {
		t.Errorf("secret refs = %d, want 2", len(refs))
	}
}

func TestParseMissingVersion(t *testing.T) {
	yaml := `name: "test"`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing version")
	}
}

func TestParseMissingName(t *testing.T) {
	yaml := `version: "1"`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for missing name")
	}
}

func TestParseInvalidTierOverride(t *testing.T) {
	yaml := `
version: "1"
name: "test"
services:
  - name: db
    tier_overrides:
      - pattern: "SELECT 1"
        tier: 5
`
	_, err := Parse([]byte(yaml))
	if err == nil {
		t.Error("expected error for invalid tier 5")
	}
}
