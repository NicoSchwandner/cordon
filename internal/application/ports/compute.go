package ports

import (
	"context"
	"io"
)

// ContainerHandle is an opaque reference to a running container.
// ID is the backend-specific identifier (Docker container ID, K8s pod name, etc.).
// Meta carries backend-specific metadata (labels, annotations, state).
type ContainerHandle struct {
	ID              string
	Name            string
	State           string // "running", "paused", "exited", etc.
	WorkspaceFolder string
	Labels          map[string]string
	Meta            map[string]string
}

// CreateContainerOpts captures what's needed to create a container on any backend.
type CreateContainerOpts struct {
	Name     string
	Image    string
	Labels   map[string]string
	Env      []string
	Mounts   []MountSpec
	Network  string // network name or ID
	CPU      int
	MemoryMB int
}

// MountSpec describes a volume mount.
type MountSpec struct {
	Source string // volume name or host path
	Target string // container path
}

// TerminalOpts configures an interactive terminal session.
type TerminalOpts struct {
	Cmd        []string
	Env        []string
	WorkingDir string
}

// TerminalSession provides interactive PTY access to a container.
type TerminalSession interface {
	io.ReadWriteCloser
	Resize(rows, cols uint) error
}

// ComputeBackend abstracts the container runtime.
// Docker, Kubernetes, ACI, and other backends implement this interface.
type ComputeBackend interface {
	// Lifecycle
	CreateContainer(ctx context.Context, opts CreateContainerOpts) (ContainerHandle, error)
	RemoveContainer(ctx context.Context, id string) error
	PauseContainer(ctx context.Context, id string) error
	UnpauseContainer(ctx context.Context, id string) error

	// Execution
	Exec(ctx context.Context, id string, cmd string) (exitCode int, err error)
	ExecWithOutput(ctx context.Context, id string, cmd string) (output string, err error)
	ExecRaw(ctx context.Context, id string, cmd []string) (exitCode int, output string, err error)
	OpenTerminal(ctx context.Context, id string, opts TerminalOpts) (TerminalSession, error)

	// Image
	PullImage(ctx context.Context, image string) error

	// Networking
	CreateNetwork(ctx context.Context, name string, labels map[string]string, internal bool) (networkID string, err error)
	RemoveNetwork(ctx context.Context, id string) error

	// Egress enforcement — network-level isolation managed outside the container.
	// EnsureProxyAccess makes the Cordon proxy reachable from an isolated network.
	// The gateway should use PROXY protocol so the server sees real client IPs.
	EnsureProxyAccess(ctx context.Context, networkName string, proxyAddr string, labels map[string]string) (internalProxyAddr string, err error)
	// RemoveProxyAccess cleans up resources created by EnsureProxyAccess.
	RemoveProxyAccess(ctx context.Context, networkName string) error

	// Storage
	CreateVolume(ctx context.Context, name string, labels map[string]string) error
	RemoveVolume(ctx context.Context, name string) error

	// Discovery — find containers by label selectors
	FindContainer(ctx context.Context, labels map[string]string) (ContainerHandle, error)
	ListContainers(ctx context.Context, labels map[string]string) ([]ContainerHandle, error)
}
