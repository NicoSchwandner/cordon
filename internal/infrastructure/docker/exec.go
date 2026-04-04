package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/google/uuid"
)

func (p *Provider) execInContainer(ctx context.Context, containerIDPrefix, cmd string) (int, error) {
	// Find full container ID
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "cordon.workspace")),
	})
	if err != nil {
		return -1, err
	}
	var containerID string
	for _, c := range containers {
		if strings.HasPrefix(c.ID, containerIDPrefix) || strings.HasPrefix(c.Labels["cordon.workspace"], containerIDPrefix) {
			containerID = c.ID
			break
		}
	}
	if containerID == "" {
		return -1, fmt.Errorf("container not found")
	}

	execResp, err := p.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
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
			if inspect.ExitCode != 0 {
				return inspect.ExitCode, fmt.Errorf("command exited with code %d", inspect.ExitCode)
			}
			return 0, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// execInContainerWithOutput runs a shell command and returns its stdout.
func (p *Provider) execInContainerWithOutput(ctx context.Context, containerIDPrefix, cmd string) (string, error) {
	// Find full container ID
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "cordon.workspace")),
	})
	if err != nil {
		return "", err
	}
	var containerID string
	for _, c := range containers {
		if strings.HasPrefix(c.ID, containerIDPrefix) || strings.HasPrefix(c.Labels["cordon.workspace"], containerIDPrefix) {
			containerID = c.ID
			break
		}
	}
	if containerID == "" {
		return "", fmt.Errorf("container not found")
	}

	execResp, err := p.client.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("creating exec: %w", err)
	}

	attach, err := p.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attach.Close()

	var buf bytes.Buffer
	// Docker multiplexes stdout/stderr with an 8-byte header per frame.
	// stdcopy.StdCopy demuxes it, but for simple cases we can read raw
	// since we only care about combined output.
	_, _ = io.Copy(&buf, attach.Reader)

	// Wait for exec to finish
	for {
		inspect, err := p.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return buf.String(), err
		}
		if !inspect.Running {
			if inspect.ExitCode != 0 {
				return buf.String(), fmt.Errorf("command exited with code %d", inspect.ExitCode)
			}
			return buf.String(), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// execInContainerByName runs a shell command in a container identified by name.
func (p *Provider) execInContainerByName(ctx context.Context, containerName, cmd string) (int, error) {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("name", containerName)),
	})
	if err != nil || len(containers) == 0 {
		return -1, fmt.Errorf("container %s not found", containerName)
	}

	execResp, err := p.client.ContainerExecCreate(ctx, containers[0].ID, container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
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
			if inspect.ExitCode != 0 {
				return inspect.ExitCode, fmt.Errorf("command exited with code %d", inspect.ExitCode)
			}
			return 0, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ExecInRepo executes a command in the container that owns a specific repo.
// If repoName is empty or matches the primary repo, exec in the primary container.
func (p *Provider) ExecInRepo(ctx context.Context, workspaceID uuid.UUID, repoName string, cmd []string) (int, string, error) {
	containers, err := p.client.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(
			filters.Arg("label", "cordon.workspace="+workspaceID.String()),
		),
	})
	if err != nil {
		return -1, "", fmt.Errorf("querying docker: %w", err)
	}

	// Find the right container
	var targetID string
	for _, c := range containers {
		svcFor := c.Labels["cordon.service-for"]
		if repoName != "" && svcFor == repoName {
			targetID = c.ID
			break
		}
		if svcFor == "" {
			// Primary container — use as default
			if repoName == "" {
				targetID = c.ID
				break
			}
			// Check if this repo is a non-service repo (cloned in primary)
			if targetID == "" {
				targetID = c.ID // fallback to primary
			}
		}
	}

	if targetID == "" {
		return -1, "", fmt.Errorf("no container found for repo %q", repoName)
	}

	// Execute with output capture
	execResp, err := p.client.ContainerExecCreate(ctx, targetID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, "", fmt.Errorf("creating exec: %w", err)
	}

	attach, err := p.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attach.Close()

	var buf bytes.Buffer
	io.Copy(&buf, attach.Reader)

	// Wait for completion
	for {
		inspect, err := p.client.ContainerExecInspect(ctx, execResp.ID)
		if err != nil {
			return -1, buf.String(), err
		}
		if !inspect.Running {
			return inspect.ExitCode, buf.String(), nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *Provider) Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error) {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return -1, err
	}

	execResp, err := p.client.ContainerExecCreate(ctx, c.ID, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
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
