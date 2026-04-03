package domain

import (
	"time"

	"github.com/google/uuid"
)

type WorkspaceStatus string

const (
	WorkspaceCreating  WorkspaceStatus = "creating"
	WorkspaceRunning   WorkspaceStatus = "running"
	WorkspaceSuspended WorkspaceStatus = "suspended"
	WorkspaceDestroyed WorkspaceStatus = "destroyed"
)

type Workspace struct {
	ID          uuid.UUID
	TenantID    uuid.UUID
	Name        string
	Status      WorkspaceStatus
	CreatedAt   time.Time
	SuspendedAt *time.Time
	ExpiresAt   time.Time
	Config      WorkspaceConfig
}

// RepoConfig describes a single repository in a workspace.
type RepoConfig struct {
	URL              string `json:"url"`
	Branch           string `json:"branch,omitempty"`
	BaseBranch       string `json:"base_branch,omitempty"`
	DevcontainerPath string `json:"devcontainer_path,omitempty"`
	Primary          bool   `json:"primary,omitempty"`          // first repo is primary by default
	ServiceContainer bool   `json:"service_container,omitempty"` // gets its own devcontainer container
}

type WorkspaceConfig struct {
	ID               uuid.UUID     // pre-generated workspace ID (zero = auto-generate)
	Name             string
	Repos            []RepoConfig
	DevcontainerPath string
	CPU              int
	MemoryMB         int
	IdleTimeout      time.Duration
	MaxLifetime      time.Duration
	AllowSSH         bool
}

// PrimaryRepo returns the primary repository config.
// If none is explicitly marked, the first repo is primary.
// Returns nil if the workspace has no repos.
func (c WorkspaceConfig) PrimaryRepo() *RepoConfig {
	for i := range c.Repos {
		if c.Repos[i].Primary {
			return &c.Repos[i]
		}
	}
	if len(c.Repos) > 0 {
		return &c.Repos[0]
	}
	return nil
}

// HasRepos returns true if the workspace has at least one repository configured.
func (c WorkspaceConfig) HasRepos() bool {
	return len(c.Repos) > 0
}

// RepoShortName extracts the short name from a repo URL (e.g., "github.com/org/backend" → "backend").
func RepoShortName(repoURL string) string {
	url := repoURL
	// Strip .git suffix
	if len(url) > 4 && url[len(url)-4:] == ".git" {
		url = url[:len(url)-4]
	}
	// Find last slash
	for i := len(url) - 1; i >= 0; i-- {
		if url[i] == '/' {
			return url[i+1:]
		}
	}
	return url
}
