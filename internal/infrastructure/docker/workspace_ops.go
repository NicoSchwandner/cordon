package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/volume"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/domain"
)

func (p *Provider) Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	ws := containerToWorkspace(c)

	// Enrich investigation state from in-container file (labels are immutable)
	if ws.Config.Mode == domain.WorkspaceModeInvestigation && ws.Status == domain.WorkspaceRunning {
		output, err := p.execInContainerWithOutput(ctx, c.ID[:12], `cat /workspace/.cordon/activated-repos.json 2>/dev/null || echo '[]'`)
		if err == nil && ws.Config.Investigation != nil {
			var activated []string
			json.Unmarshal([]byte(strings.TrimSpace(output)), &activated)
			ws.Config.Investigation.ActivatedRepos = activated
		}
	}
	return ws, nil
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
		// Skip service containers — only list primary workspaces
		if c.Labels["cordon.service-for"] != "" {
			continue
		}
		result = append(result, containerToWorkspace(c))
	}
	return result, nil
}

func (p *Provider) Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	return p.client.ContainerPause(ctx, c.ID)
}

func (p *Provider) Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	return p.client.ContainerUnpause(ctx, c.ID)
}

func (p *Provider) Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	// Find ALL containers for this workspace (primary + service containers)
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
		),
	})
	if err != nil {
		return fmt.Errorf("querying docker: %w", err)
	}
	if len(containers) == 0 {
		return fmt.Errorf("workspace not found")
	}

	log.Printf("[workspace] destroying workspace %s (%d containers)", workspaceID.String()[:8], len(containers))

	// Remove all containers
	var networkName string
	for _, c := range containers {
		svcFor := c.Labels["cordon.service-for"]
		if svcFor != "" {
			log.Printf("[workspace] removing service container for %s (%s)", svcFor, c.ID[:12])
		} else {
			log.Printf("[workspace] removing primary container (%s)", c.ID[:12])
			if len(c.Names) > 0 {
				networkName = strings.TrimPrefix(c.Names[0], "/") + "-net"
			}
		}
		if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			log.Printf("[workspace] warning: container remove failed: %v", err)
		}
	}

	// Remove workspace volumes
	volumes, _ := p.client.VolumeList(ctx, volume.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
		),
	})
	for _, v := range volumes.Volumes {
		log.Printf("[workspace] removing volume %s", v.Name)
		if err := p.client.VolumeRemove(ctx, v.Name, true); err != nil {
			log.Printf("[workspace] warning: volume remove failed: %v", err)
		}
	}

	// Remove network
	if networkName != "" {
		if err := p.client.NetworkRemove(ctx, networkName); err != nil {
			log.Printf("[workspace] warning: network remove failed: %v", err)
		}
	}

	log.Printf("[workspace] workspace destroyed")
	return nil
}

// ResolveDefaultBranch returns the default branch for a repo, delegating to the shared GitHub client.
func (p *Provider) ResolveDefaultBranch(repoURL string) string {
	if p.github != nil {
		return p.github.ResolveDefaultBranch(repoURL)
	}
	return "main"
}
