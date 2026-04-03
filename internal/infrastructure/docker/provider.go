package docker

import (
	"context"
	"fmt"
	"io"
	"log"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// Provider manages Docker-based workspaces.
// Container labels are the source of truth — no in-memory state survives restarts.
type Provider struct {
	client *client.Client
}

func NewProvider() (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	// Log how many cordon containers already exist (from previous runs)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	existing, _ := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "cordon.workspace")),
	})
	if len(existing) > 0 {
		log.Printf("[workspace] found %d existing cordon container(s) from previous run", len(existing))
	}

	return &Provider{client: cli}, nil
}

func (p *Provider) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := uuid.New()
	safeName := sanitizeContainerName(config.Name)
	containerName := fmt.Sprintf("cordon-%s-%s", safeName, wsID.String()[:8])

	log.Printf("[workspace] creating %s (tenant=%s)", containerName, tenantID.String()[:8])

	// Create a dedicated network for the workspace
	networkResp, err := p.client.NetworkCreate(ctx, containerName+"-net", network.CreateOptions{
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
	cpuQuota := int64(config.CPU) * 100000
	memLimit := int64(config.MemoryMB) * 1024 * 1024
	now := time.Now().UTC()

	containerConfig := &container.Config{
		Image: image,
		Labels: map[string]string{
			"cordon.tenant":    tenantID.String(),
			"cordon.workspace": wsID.String(),
			"cordon.name":      config.Name,
			"cordon.created":   now.Format(time.RFC3339),
			"cordon.expires":   now.Add(config.MaxLifetime).Format(time.RFC3339),
		},
		Env: []string{
			"ZT_WORKSPACE_ID=" + wsID.String(),
			"ZT_TENANT_ID=" + tenantID.String(),
			"DATABASE_URL=cordon-placeholder-database-url",
			"API_KEY=cordon-placeholder-api-key",
		},
		Cmd: []string{"sleep", "infinity"},
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			CPUQuota: cpuQuota,
			Memory:   memLimit,
		},
		NetworkMode: container.NetworkMode(containerName + "-net"),
	}

	resp, err := p.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, containerName)
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
	log.Printf("[workspace] %s is running (container=%s)", containerName, resp.ID[:12])

	return domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      config.Name,
		Status:    domain.WorkspaceRunning,
		CreatedAt: now,
		ExpiresAt: now.Add(config.MaxLifetime),
		Config:    config,
	}, nil
}

func (p *Provider) Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	return containerToWorkspace(c), nil
}

func (p *Provider) List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.tenant="+tenantID.String()),
		),
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	result := make([]domain.Workspace, 0, len(containers))
	for _, c := range containers {
		result = append(result, containerToWorkspace(c))
	}
	return result, nil
}

func (p *Provider) Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	if err := p.client.ContainerPause(ctx, c.ID); err != nil {
		return fmt.Errorf("pausing container: %w", err)
	}
	return nil
}

func (p *Provider) Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	if err := p.client.ContainerUnpause(ctx, c.ID); err != nil {
		return fmt.Errorf("unpausing container: %w", err)
	}
	return nil
}

func (p *Provider) Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}

	name := c.Labels["cordon.name"]
	log.Printf("[workspace] destroying %s (container=%s)", name, c.ID[:12])

	if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("[workspace] warning: container remove failed: %v", err)
	}

	// Find and remove associated network
	networkName := ""
	if len(c.Names) > 0 {
		networkName = strings.TrimPrefix(c.Names[0], "/") + "-net"
	}
	if networkName != "" {
		if err := p.client.NetworkRemove(ctx, networkName); err != nil {
			log.Printf("[workspace] warning: network remove failed: %v", err)
		}
	}

	log.Printf("[workspace] destroyed %s", name)
	return nil
}

func (p *Provider) Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error) {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return -1, err
	}

	execConfig := container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := p.client.ContainerExecCreate(ctx, c.ID, execConfig)
	if err != nil {
		return -1, fmt.Errorf("creating exec: %w", err)
	}

	if err := p.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return -1, fmt.Errorf("starting exec: %w", err)
	}

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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
		),
	})
	if err != nil || len(containers) == 0 {
		return "", fmt.Errorf("workspace not found")
	}
	return containers[0].ID, nil
}

// findContainer locates a running cordon container by workspace and tenant ID.
func (p *Provider) findContainer(ctx context.Context, tenantID, workspaceID uuid.UUID) (types.Container, error) {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
			filters.Arg("label", "cordon.tenant="+tenantID.String()),
		),
	})
	if err != nil {
		return types.Container{}, fmt.Errorf("querying docker: %w", err)
	}
	if len(containers) == 0 {
		return types.Container{}, fmt.Errorf("workspace not found")
	}
	return containers[0], nil
}

// containerToWorkspace maps a Docker container to a domain Workspace.
func containerToWorkspace(c types.Container) domain.Workspace {
	wsID, _ := uuid.Parse(c.Labels["cordon.workspace"])
	tenantID, _ := uuid.Parse(c.Labels["cordon.tenant"])
	created, _ := time.Parse(time.RFC3339, c.Labels["cordon.created"])
	expires, _ := time.Parse(time.RFC3339, c.Labels["cordon.expires"])

	if created.IsZero() {
		created = time.Unix(c.Created, 0).UTC()
	}
	if expires.IsZero() {
		expires = created.Add(24 * time.Hour)
	}

	status := domain.WorkspaceRunning
	switch c.State {
	case "paused":
		status = domain.WorkspaceSuspended
	case "exited", "dead", "removing":
		status = domain.WorkspaceDestroyed
	case "created", "restarting":
		status = domain.WorkspaceCreating
	}

	return domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      c.Labels["cordon.name"],
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
	}
}

func sanitizeContainerName(name string) string {
	var b []byte
	for _, c := range []byte(strings.ToLower(name)) {
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' {
			b = append(b, c)
		} else if c == ' ' {
			b = append(b, '-')
		}
	}
	if len(b) == 0 {
		return "workspace"
	}
	return string(b)
}
