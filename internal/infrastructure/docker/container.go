package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

func (p *Provider) containerName(name string, wsID uuid.UUID) string {
	return fmt.Sprintf("cordon-%s-%s", sanitizeContainerName(name), wsID.String()[:8])
}

func (p *Provider) createNetwork(ctx context.Context, containerName string, tenantID, wsID uuid.UUID) (string, error) {
	networkResp, err := p.client.NetworkCreate(ctx, containerName+"-net", network.CreateOptions{
		Driver: "bridge",
		Labels: map[string]string{
			"cordon.tenant":    tenantID.String(),
			"cordon.workspace": wsID.String(),
		},
	})
	if err != nil {
		return "", fmt.Errorf("creating network: %w", err)
	}
	return networkResp.ID, nil
}

func (p *Provider) baseLabels(tenantID, wsID uuid.UUID, name string, now time.Time, maxLifetime time.Duration) map[string]string {
	return map[string]string{
		"cordon.tenant":    tenantID.String(),
		"cordon.workspace": wsID.String(),
		"cordon.name":      name,
		"cordon.created":   now.Format(time.RFC3339),
		"cordon.expires":   now.Add(maxLifetime).Format(time.RFC3339),
	}
}

func (p *Provider) pullImage(ctx context.Context, image string) {
	log.Printf("[workspace] ensuring image %s is available...", image)
	reader, err := p.client.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err == nil {
		_, _ = io.Copy(io.Discard, reader)
		reader.Close()
		log.Printf("[workspace] image ready")
	} else {
		log.Printf("[workspace] image pull skipped (may already exist): %v", err)
	}
}

type startContainerOpts struct {
	name        string
	image       string
	labels      map[string]string
	env         []string
	mounts      []mount.Mount
	networkName string // Docker network name to join
	cpu         int
	memoryMB    int
}

func (p *Provider) startContainer(ctx context.Context, containerName, image string, labels map[string]string, env []string, config domain.WorkspaceConfig, networkID string, tenantID, wsID uuid.UUID, now time.Time) (domain.Workspace, error) {
	networkName := containerName + "-net"
	if err := p.startContainerRaw(ctx, startContainerOpts{
		name:        containerName,
		image:       image,
		labels:      labels,
		env:         env,
		networkName: networkName,
		cpu:         config.CPU,
		memoryMB:    config.MemoryMB,
	}); err != nil {
		return domain.Workspace{}, err
	}

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

func (p *Provider) startContainerRaw(ctx context.Context, opts startContainerOpts) error {
	cpuQuota := int64(opts.cpu) * 100000
	memLimit := int64(opts.memoryMB) * 1024 * 1024

	containerConfig := &container.Config{
		Image:  opts.image,
		Labels: opts.labels,
		Env:    opts.env,
		Cmd:    []string{"sleep", "infinity"},
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			CPUQuota: cpuQuota,
			Memory:   memLimit,
		},
		NetworkMode: container.NetworkMode(opts.networkName),
		Mounts:      opts.mounts,
	}

	resp, err := p.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, opts.name)
	if err != nil {
		return fmt.Errorf("creating container: %w", err)
	}

	log.Printf("[workspace] starting container %s", resp.ID[:12])
	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		p.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return fmt.Errorf("starting container: %w", err)
	}
	log.Printf("[workspace] %s is running (container=%s)", opts.name, resp.ID[:12])
	return nil
}

// ContainerInfo holds container metadata needed by the terminal handler.
type ContainerInfo struct {
	ID              string
	WorkspaceFolder string // from cordon.workspace-folder label
}

func (p *Provider) ContainerID(workspaceID uuid.UUID) (string, error) {
	info, err := p.ContainerInfo(workspaceID)
	if err != nil {
		return "", err
	}
	return info.ID, nil
}

func (p *Provider) ContainerInfo(workspaceID uuid.UUID) (ContainerInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
		),
	})
	if err != nil || len(containers) == 0 {
		return ContainerInfo{}, fmt.Errorf("workspace not found")
	}
	return ContainerInfo{
		ID:              containers[0].ID,
		WorkspaceFolder: containers[0].Labels["cordon.workspace-folder"],
	}, nil
}

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
	// Return the primary container (skip service containers)
	for _, c := range containers {
		if _, isSvc := c.Labels["cordon.service-for"]; !isSvc {
			return c, nil
		}
	}
	if len(containers) == 0 {
		return types.Container{}, fmt.Errorf("workspace not found")
	}
	return containers[0], nil
}

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

	var repos []domain.RepoConfig
	if reposJSON := c.Labels["cordon.repos"]; reposJSON != "" {
		json.Unmarshal([]byte(reposJSON), &repos)
	}

	config := domain.WorkspaceConfig{
		Repos: repos,
	}

	// Populate investigation state from labels
	if mode := c.Labels["cordon.mode"]; mode == string(domain.WorkspaceModeInvestigation) {
		config.Mode = domain.WorkspaceModeInvestigation
		var shallowRepos []string
		if sr := c.Labels["cordon.shallow-repos"]; sr != "" {
			json.Unmarshal([]byte(sr), &shallowRepos)
		}
		config.Investigation = &domain.InvestigationState{
			CatalogOrg:   c.Labels["cordon.org"],
			ShallowRepos: shallowRepos,
			// ActivatedRepos populated by Get() via exec (labels are immutable)
		}
	}

	ws := domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      c.Labels["cordon.name"],
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
		Config:    config,
	}
	if sf := c.Labels["cordon.spawned-from"]; sf != "" {
		if id, err := uuid.Parse(sf); err == nil {
			ws.SpawnedFrom = &id
		}
	}
	return ws
}

func (p *Provider) cloneURL(repo string) string {
	if p.token == "" {
		if !strings.Contains(repo, "://") {
			return "https://" + repo
		}
		return repo
	}
	repoURL := repo
	if !strings.Contains(repoURL, "://") {
		repoURL = "https://" + repoURL
	}
	u, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}
	if !strings.HasSuffix(u.Path, ".git") {
		u.Path += ".git"
	}
	u.User = url.UserPassword("x-access-token", p.token)
	return u.String()
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
