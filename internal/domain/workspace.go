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

type WorkspaceConfig struct {
	DevcontainerPath string
	CPU              int
	MemoryMB         int
	IdleTimeout      time.Duration
	MaxLifetime      time.Duration
	Repo             string
	AllowSSH         bool
}
