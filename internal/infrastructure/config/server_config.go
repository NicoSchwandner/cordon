package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// ServerConfig holds all server-level configuration loaded from environment
// variables at startup. Values that were previously hardcoded across handlers,
// services, and main.go are centralized here.
type ServerConfig struct {
	Server    ServerSettings
	Auth      AuthSettings
	Database  DatabaseSettings
	Secrets   SecretSettings
	Egress    EgressSettings
	Workspace WorkspaceSettings
	Proxy     ProxySettings
	GitHub    GitHubSettings
	HTTP      HTTPSettings
}

type ServerSettings struct {
	Port            string
	ShutdownTimeout time.Duration
}

type AuthSettings struct {
	Mode            string // "static" or "token"
	DefaultTenantID string
	APIToken        string // used when Mode == "token"
}

type DatabaseSettings struct {
	URL string
}

// SecretSettings holds initial vault secrets loaded from env vars.
// These move to a real vault backend in Phase 1.2.
type SecretSettings struct {
	RealDatabaseURL string
	RealAPIKey      string
	GitHubToken     string
	MasterKey       string // 32-byte hex-encoded key for AES-256-GCM encryption at rest
}

type EgressSettings struct {
	Enforce   bool
	ProxyAddr string   // auto-computed from port if empty
	Allowlist []string // hosts the proxy allows through
}

type WorkspaceSettings struct {
	DefaultIdleTimeout time.Duration
	DefaultMaxLifetime time.Duration
	MaxExtension       time.Duration // hard ceiling for TTL extensions
	MinLifetime        time.Duration // minimum allowed lifetime in create requests
	AsyncTimeout       time.Duration // context timeout for async creation goroutines
	DefaultCPU         int
	DefaultMemoryMB    int
	CloneConcurrency   int // parallel repo clone semaphore size
	ReaperInterval     time.Duration
}

type ProxySettings struct {
	ApprovalTimeout  time.Duration
	HTTPTimeout      time.Duration // timeout for outbound HTTP proxy requests
	MaxResponseBody  int64         // max response body size in bytes
}

type GitHubSettings struct {
	RepoListTTL   time.Duration
	BranchListTTL time.Duration
	HTTPTimeout   time.Duration
}

type HTTPSettings struct {
	ReadTimeout time.Duration
	IdleTimeout time.Duration
}

// LoadServer reads server configuration from environment variables with sensible defaults.
// It validates required fields and returns an error if configuration is invalid.
func LoadServer() (*ServerConfig, error) {
	port := envOr("PORT", "8443")

	cfg := &ServerConfig{
		Server: ServerSettings{
			Port:            port,
			ShutdownTimeout: durationOr("CORDON_SHUTDOWN_TIMEOUT", 10*time.Second),
		},
		Auth: AuthSettings{
			Mode:            envOr("AUTH_MODE", "static"),
			DefaultTenantID: envOr("DEFAULT_TENANT_ID", "00000000-0000-0000-0000-000000000001"),
			APIToken:        envOr("API_TOKEN", "dev-token"),
		},
		Database: DatabaseSettings{
			URL: envOr("DATABASE_URL", "postgres://postgres:postgres@localhost:5432/cordon?sslmode=disable"),
		},
		Secrets: SecretSettings{
			RealDatabaseURL: envOr("REAL_DATABASE_URL", "postgresql://real:secret@db:5432/prod"),
			RealAPIKey:      envOr("REAL_API_KEY", "sk-real-key-12345"),
			GitHubToken:     os.Getenv("GITHUB_TOKEN"),
			MasterKey:       os.Getenv("CORDON_MASTER_KEY"),
		},
		Egress: EgressSettings{
			Enforce:   os.Getenv("CORDON_EGRESS_ENFORCE") != "false",
			ProxyAddr: envOr("CORDON_PROXY_ADDR", "host.docker.internal:"+port),
			Allowlist: stringsOr("CORDON_EGRESS_ALLOWLIST", []string{
				"github.com", "*.github.com",
				"api.anthropic.com",
				"registry.npmjs.org",
				"nuget.org", "*.nuget.org",
			}),
		},
		Workspace: WorkspaceSettings{
			DefaultIdleTimeout: durationOr("CORDON_IDLE_TIMEOUT", 15*time.Minute),
			DefaultMaxLifetime: durationOr("CORDON_MAX_LIFETIME", 8*time.Hour),
			MaxExtension:       durationOr("CORDON_MAX_EXTENSION", 24*time.Hour),
			MinLifetime:        1 * time.Minute,
			AsyncTimeout:       durationOr("CORDON_ASYNC_TIMEOUT", 30*time.Minute),
			DefaultCPU:         intOr("CORDON_DEFAULT_CPU", 1),
			DefaultMemoryMB:    intOr("CORDON_DEFAULT_MEMORY_MB", 512),
			CloneConcurrency:   intOr("CORDON_CLONE_CONCURRENCY", 12),
			ReaperInterval:     durationOr("CORDON_REAPER_INTERVAL", 30*time.Second),
		},
		Proxy: ProxySettings{
			ApprovalTimeout:  durationOr("CORDON_APPROVAL_TIMEOUT", 5*time.Minute),
			HTTPTimeout:      durationOr("CORDON_PROXY_HTTP_TIMEOUT", 30*time.Second),
			MaxResponseBody:  int64(intOr("CORDON_MAX_RESPONSE_BODY", 1<<20)),
		},
		GitHub: GitHubSettings{
			RepoListTTL:   durationOr("CORDON_GITHUB_REPO_TTL", 5*time.Minute),
			BranchListTTL: durationOr("CORDON_GITHUB_BRANCH_TTL", 2*time.Minute),
			HTTPTimeout:   durationOr("CORDON_GITHUB_HTTP_TIMEOUT", 15*time.Second),
		},
		HTTP: HTTPSettings{
			ReadTimeout: durationOr("CORDON_HTTP_READ_TIMEOUT", 30*time.Second),
			IdleTimeout: durationOr("CORDON_HTTP_IDLE_TIMEOUT", 120*time.Second),
		},
	}

	if err := cfg.validate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *ServerConfig) validate() error {
	if c.Server.Port == "" {
		return fmt.Errorf("PORT must not be empty")
	}
	if c.Database.URL == "" {
		return fmt.Errorf("DATABASE_URL must not be empty")
	}
	if c.Auth.Mode != "static" && c.Auth.Mode != "token" {
		return fmt.Errorf("AUTH_MODE must be 'static' or 'token', got %q", c.Auth.Mode)
	}
	if c.Workspace.DefaultMaxLifetime < c.Workspace.MinLifetime {
		return fmt.Errorf("CORDON_MAX_LIFETIME (%s) must be >= MinLifetime (%s)", c.Workspace.DefaultMaxLifetime, c.Workspace.MinLifetime)
	}
	return nil
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func durationOr(key string, fallback time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return fallback
	}
	return d
}

func intOr(key string, fallback int) int {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return fallback
	}
	return n
}

func stringsOr(key string, fallback []string) []string {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	parts := strings.Split(v, ",")
	result := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	if len(result) == 0 {
		return fallback
	}
	return result
}
