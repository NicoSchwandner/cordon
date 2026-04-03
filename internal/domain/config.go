package domain

import "time"

// ProjectConfig represents a .cordon.yaml file.
type ProjectConfig struct {
	Version  string          `yaml:"version"`
	Name     string          `yaml:"name"`
	Secrets  []SecretConfig  `yaml:"secrets"`
	Services []ServiceConfig `yaml:"services"`
	Egress   EgressConfig    `yaml:"egress"`
	Workspace WorkspaceConfigYAML `yaml:"workspace"`
	Approval ApprovalConfig  `yaml:"approval"`
}

type SecretConfig struct {
	Name        string `yaml:"name"`
	Placeholder string `yaml:"placeholder"`
}

type ServiceConfig struct {
	Name           string         `yaml:"name"`
	Type           string         `yaml:"type"`
	Target         string         `yaml:"target"`
	AllowedMethods []string       `yaml:"allowed_methods"`
	TierOverrides  []TierOverrideConfig `yaml:"tier_overrides"`
}

type TierOverrideConfig struct {
	Pattern string `yaml:"pattern"`
	Tier    int    `yaml:"tier"`
}

type EgressConfig struct {
	Allowlist []string `yaml:"allowlist"`
}

type WorkspaceConfigYAML struct {
	Devcontainer string        `yaml:"devcontainer"`
	Resources    ResourceConfig `yaml:"resources"`
	IdleTimeout  string        `yaml:"idle_timeout"`
	MaxLifetime  string        `yaml:"max_lifetime"`
	Tools        []string      `yaml:"tools"`
	AllowSSH     bool          `yaml:"allow_ssh"`
}

type ResourceConfig struct {
	CPU    int    `yaml:"cpu"`
	Memory string `yaml:"memory"`
}

type ApprovalConfig struct {
	Tier3TimeoutSeconds  int  `yaml:"tier3_timeout_seconds"`
	AllowSessionGrants   bool `yaml:"allow_session_grants"`
	AllowPatternGrants   bool `yaml:"allow_pattern_grants"`
}

// ParseDuration parses a human-readable duration like "15m" or "24h".
func ParseDuration(s string) time.Duration {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0
	}
	return d
}
