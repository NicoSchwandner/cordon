package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// Provider manages Docker-based workspaces.
type Provider struct {
	client     *client.Client
	mu         sync.Mutex
	workspaces map[uuid.UUID]*workspaceState
}

type workspaceState struct {
	workspace   domain.Workspace
	containerID string
	networkID   string
	lastActive  time.Time
}

func NewProvider() (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &Provider{
		client:     cli,
		workspaces: make(map[uuid.UUID]*workspaceState),
	}, nil
}

func (p *Provider) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := uuid.New()
	wsName := fmt.Sprintf("cordon-%s-%s", config.Name, wsID.String()[:8])

	log.Printf("[workspace] creating %s (tenant=%s)", wsName, tenantID.String()[:8])

	// Create a dedicated network for the workspace
	networkResp, err := p.client.NetworkCreate(ctx, wsName+"-net", network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"cordon.tenant":    tenantID.String(),
			"cordon.workspace": wsID.String(),
		},
	})
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("creating network: %w", err)
	}

	// Use a base dev image — in production this would be built from devcontainer.json
	image := "mcr.microsoft.com/devcontainers/base:ubuntu"
	if config.DevcontainerPath != "" {
		image = "mcr.microsoft.com/devcontainers/base:ubuntu" // TODO: build from devcontainer.json
	}

	// Pull image if not already available
	log.Printf("[workspace] ensuring image %s is available...", image)
	reader, err := p.client.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err == nil {
		io.Copy(io.Discard, reader)
		reader.Close()
		log.Printf("[workspace] image ready")
	} else {
		log.Printf("[workspace] image pull failed (may already exist locally): %v", err)
	}

	// Create workspace container
	cpuQuota := int64(config.CPU) * 100000 // 1 CPU = 100000
	memLimit := int64(config.MemoryMB) * 1024 * 1024

	containerConfig := &container.Config{
		Image: image,
		Labels: map[string]string{
			"cordon.tenant":    tenantID.String(),
			"cordon.workspace": wsID.String(),
			"cordon.name":      wsName,
		},
		Env: []string{
			"ZT_WORKSPACE_ID=" + wsID.String(),
			"ZT_TENANT_ID=" + tenantID.String(),
			// Placeholder secrets — real values injected by proxy
			"DATABASE_URL=cordon-placeholder-database-url",
			"API_KEY=cordon-placeholder-api-key",
		},
		Cmd: []string{"sleep", "infinity"}, // Keep container alive
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			CPUQuota: cpuQuota,
			Memory:   memLimit,
		},
		NetworkMode: container.NetworkMode(wsName + "-net"),
	}

	resp, err := p.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, wsName)
	if err != nil {
		p.client.NetworkRemove(ctx, networkResp.ID)
		return domain.Workspace{}, fmt.Errorf("creating container: %w", err)
	}

	log.Printf("[workspace] starting container %s", resp.ID[:12])
	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		p.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		p.client.NetworkRemove(ctx, networkResp.ID)
		return domain.Workspace{}, fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[workspace] %s is running (container=%s)", wsName, resp.ID[:12])

	now := time.Now().UTC()
	ws := domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      config.Name,
		Status:    domain.WorkspaceRunning,
		CreatedAt: now,
		ExpiresAt: now.Add(config.MaxLifetime),
		Config:    config,
	}

	p.mu.Lock()
	p.workspaces[wsID] = &workspaceState{
		workspace:   ws,
		containerID: resp.ID,
		networkID:   networkResp.ID,
		lastActive:  now,
	}
	p.mu.Unlock()

	return ws, nil
}

func (p *Provider) Get(_ context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	state, ok := p.workspaces[workspaceID]
	if !ok || state.workspace.TenantID != tenantID {
		return domain.Workspace{}, fmt.Errorf("workspace not found")
	}
	return state.workspace, nil
}

func (p *Provider) List(_ context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var result []domain.Workspace
	for _, state := range p.workspaces {
		if state.workspace.TenantID == tenantID {
			result = append(result, state.workspace)
		}
	}
	return result, nil
}

func (p *Provider) Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	p.mu.Lock()
	state, ok := p.workspaces[workspaceID]
	if !ok || state.workspace.TenantID != tenantID {
		p.mu.Unlock()
		return fmt.Errorf("workspace not found")
	}
	p.mu.Unlock()

	if err := p.client.ContainerPause(ctx, state.containerID); err != nil {
		return fmt.Errorf("pausing container: %w", err)
	}

	p.mu.Lock()
	now := time.Now().UTC()
	state.workspace.Status = domain.WorkspaceSuspended
	state.workspace.SuspendedAt = &now
	p.mu.Unlock()

	return nil
}

func (p *Provider) Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	p.mu.Lock()
	state, ok := p.workspaces[workspaceID]
	if !ok || state.workspace.TenantID != tenantID {
		p.mu.Unlock()
		return fmt.Errorf("workspace not found")
	}
	p.mu.Unlock()

	if err := p.client.ContainerUnpause(ctx, state.containerID); err != nil {
		return fmt.Errorf("unpausing container: %w", err)
	}

	p.mu.Lock()
	state.workspace.Status = domain.WorkspaceRunning
	state.workspace.SuspendedAt = nil
	state.lastActive = time.Now().UTC()
	p.mu.Unlock()

	return nil
}

func (p *Provider) Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	p.mu.Lock()
	state, ok := p.workspaces[workspaceID]
	if !ok || state.workspace.TenantID != tenantID {
		p.mu.Unlock()
		return fmt.Errorf("workspace not found")
	}
	delete(p.workspaces, workspaceID)
	p.mu.Unlock()

	// Remove container (force kill)
	log.Printf("[workspace] destroying %s (container=%s)", state.workspace.Name, state.containerID[:12])
	if err := p.client.ContainerRemove(ctx, state.containerID, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("[workspace] warning: container remove failed: %v", err)
	}

	// Remove network
	if err := p.client.NetworkRemove(ctx, state.networkID); err != nil {
		log.Printf("[workspace] warning: network remove failed: %v", err)
	}

	log.Printf("[workspace] destroyed %s", state.workspace.Name)
	return nil
}

func (p *Provider) Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error) {
	p.mu.Lock()
	state, ok := p.workspaces[workspaceID]
	if !ok || state.workspace.TenantID != tenantID {
		p.mu.Unlock()
		return -1, fmt.Errorf("workspace not found")
	}
	state.lastActive = time.Now().UTC()
	containerID := state.containerID
	p.mu.Unlock()

	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := p.client.ContainerExecCreate(ctx, containerID, execConfig)
	if err != nil {
		return -1, fmt.Errorf("creating exec: %w", err)
	}

	if err := p.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return -1, fmt.Errorf("starting exec: %w", err)
	}

	// Wait for completion
	for {
		inspect, err := p.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return -1, err
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ContainerID returns the Docker container ID for a workspace (for terminal relay).
func (p *Provider) ContainerID(workspaceID uuid.UUID) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	state, ok := p.workspaces[workspaceID]
	if !ok {
		return "", fmt.Errorf("workspace not found")
	}
	return state.containerID, nil
}
