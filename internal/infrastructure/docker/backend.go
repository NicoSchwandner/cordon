package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
)

// Backend implements ports.ComputeBackend using the Docker Engine API.
type Backend struct {
	client *client.Client
}

// Compile-time check that Backend satisfies ComputeBackend.
var _ ports.ComputeBackend = (*Backend)(nil)

// NewBackend creates a Docker compute backend.
func NewBackend() (*Backend, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}
	return &Backend{client: cli}, nil
}

// CreateContainer creates and starts a container. Returns a handle for subsequent operations.
func (b *Backend) CreateContainer(ctx context.Context, opts ports.CreateContainerOpts) (ports.ContainerHandle, error) {
	cpuQuota := int64(opts.CPU) * 100000
	memLimit := int64(opts.MemoryMB) * 1024 * 1024

	var mounts []mount.Mount
	for _, m := range opts.Mounts {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Source: m.Source,
			Target: m.Target,
		})
	}

	containerConfig := &container.Config{
		Image:  opts.Image,
		Labels: opts.Labels,
		Env:    opts.Env,
		Cmd:    []string{"sleep", "infinity"},
	}

	// Map string slices to Docker API strslice type for capabilities
	var capDrop, capAdd []string
	capDrop = append(capDrop, opts.CapDrop...)
	capAdd = append(capAdd, opts.CapAdd...)

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			CPUQuota: cpuQuota,
			Memory:   memLimit,
		},
		NetworkMode: container.NetworkMode(opts.Network),
		Mounts:      mounts,
		CapDrop:     capDrop,
		CapAdd:      capAdd,
		SecurityOpt: opts.SecurityOpts,
	}

	resp, err := b.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, opts.Name)
	if err != nil {
		return ports.ContainerHandle{}, fmt.Errorf("creating container: %w", err)
	}

	slog.Info("starting container", "component", "docker", "container_id", resp.ID[:12])
	if err := b.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		b.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
		return ports.ContainerHandle{}, fmt.Errorf("starting container: %w", err)
	}
	slog.Info("container is running", "component", "docker", "name", opts.Name, "container_id", resp.ID[:12])

	return ports.ContainerHandle{
		ID:     resp.ID,
		Name:   opts.Name,
		State:  "running",
		Labels: opts.Labels,
	}, nil
}

func (b *Backend) RemoveContainer(ctx context.Context, id string) error {
	return b.client.ContainerRemove(ctx, id, container.RemoveOptions{Force: true})
}

func (b *Backend) PauseContainer(ctx context.Context, id string) error {
	return b.client.ContainerPause(ctx, id)
}

func (b *Backend) UnpauseContainer(ctx context.Context, id string) error {
	return b.client.ContainerUnpause(ctx, id)
}

// Exec runs a shell command (via sh -c) and waits for completion.
// Returns an error if the command exits with a non-zero code.
func (b *Backend) Exec(ctx context.Context, id string, cmd string) (int, error) {
	execResp, err := b.client.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, fmt.Errorf("creating exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{}); err != nil {
		return -1, fmt.Errorf("starting exec: %w", err)
	}

	exitCode, err := b.waitExec(ctx, execResp.ID)
	if err != nil {
		return exitCode, err
	}
	if exitCode != 0 {
		return exitCode, fmt.Errorf("command exited with code %d", exitCode)
	}
	return 0, nil
}

// ExecWithOutput runs a shell command and returns its stdout.
func (b *Backend) ExecWithOutput(ctx context.Context, id string, cmd string) (string, error) {
	execResp, err := b.client.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return "", fmt.Errorf("creating exec: %w", err)
	}

	attach, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attach.Close()

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, attach.Reader)

	exitCode, err := b.waitExec(ctx, execResp.ID)
	if err != nil {
		return buf.String(), err
	}
	if exitCode != 0 {
		return buf.String(), fmt.Errorf("command exited with code %d", exitCode)
	}
	return buf.String(), nil
}

// ExecRaw runs a command with a raw argument list (no shell wrapping) and returns output.
func (b *Backend) ExecRaw(ctx context.Context, id string, cmd []string) (int, string, error) {
	execResp, err := b.client.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:          cmd,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return -1, "", fmt.Errorf("creating exec: %w", err)
	}

	attach, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return -1, "", fmt.Errorf("attaching exec: %w", err)
	}
	defer attach.Close()

	var buf bytes.Buffer
	io.Copy(&buf, attach.Reader)

	exitCode, err := b.waitExec(ctx, execResp.ID)
	if err != nil {
		return -1, buf.String(), err
	}
	return exitCode, buf.String(), nil
}

// OpenTerminal creates an interactive PTY session in a container.
func (b *Backend) OpenTerminal(ctx context.Context, id string, opts ports.TerminalOpts) (ports.TerminalSession, error) {
	cmd := opts.Cmd
	if len(cmd) == 0 {
		cmd = []string{"/bin/sh", "-c", `if command -v bash >/dev/null 2>&1; then exec bash -li; else exec sh -i; fi`}
	}

	execConfig := container.ExecOptions{
		Cmd:          cmd,
		Env:          append(opts.Env, "TERM=xterm-256color"),
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true,
	}

	execResp, err := b.client.ContainerExecCreate(ctx, id, execConfig)
	if err != nil {
		return nil, fmt.Errorf("creating exec: %w", err)
	}

	hijack, err := b.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, fmt.Errorf("attaching exec: %w", err)
	}

	if err := b.client.ContainerExecStart(ctx, execResp.ID, container.ExecStartOptions{Tty: true}); err != nil {
		hijack.Close()
		return nil, fmt.Errorf("starting exec: %w", err)
	}

	return &dockerTerminalSession{
		hijack: hijack,
		execID: execResp.ID,
		client: b.client,
	}, nil
}

func (b *Backend) PullImage(ctx context.Context, image string) error {
	slog.Info("ensuring image is available", "component", "docker", "image", image)
	reader, err := b.client.ImagePull(ctx, image, dockerimage.PullOptions{})
	if err != nil {
		slog.Warn("image pull skipped, may already exist", "component", "docker", "error", err)
		return nil // non-fatal — image may already exist locally
	}
	_, _ = io.Copy(io.Discard, reader)
	reader.Close()
	slog.Info("image ready", "component", "docker")
	return nil
}

func (b *Backend) CreateNetwork(ctx context.Context, name string, labels map[string]string, internal bool) (string, error) {
	resp, err := b.client.NetworkCreate(ctx, name, network.CreateOptions{
		Driver:   "bridge",
		Labels:   labels,
		Internal: internal,
	})
	if err != nil {
		return "", fmt.Errorf("creating network: %w", err)
	}
	if internal {
		slog.Info("created internal network, no external routing", "component", "docker", "network", name)
	}
	return resp.ID, nil
}

func (b *Backend) RemoveNetwork(ctx context.Context, id string) error {
	return b.client.NetworkRemove(ctx, id)
}

func (b *Backend) CreateVolume(ctx context.Context, name string, labels map[string]string) error {
	_, err := b.client.VolumeCreate(ctx, volume.CreateOptions{
		Name:   name,
		Labels: labels,
	})
	return err
}

func (b *Backend) RemoveVolume(ctx context.Context, name string) error {
	return b.client.VolumeRemove(ctx, name, true)
}

// FindContainer returns the first container matching all given labels.
func (b *Backend) FindContainer(ctx context.Context, labels map[string]string) (ports.ContainerHandle, error) {
	handles, err := b.ListContainers(ctx, labels)
	if err != nil {
		return ports.ContainerHandle{}, err
	}
	if len(handles) == 0 {
		return ports.ContainerHandle{}, fmt.Errorf("no container found matching labels")
	}
	return handles[0], nil
}

// ListContainers returns all containers matching the given labels.
func (b *Backend) ListContainers(ctx context.Context, labels map[string]string) ([]ports.ContainerHandle, error) {
	args := filters.NewArgs()
	for k, v := range labels {
		if v == "" {
			args.Add("label", k) // match any value (label exists)
		} else {
			args.Add("label", k+"="+v) // match exact value
		}
	}

	containers, err := b.client.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: args,
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	handles := make([]ports.ContainerHandle, 0, len(containers))
	for _, c := range containers {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		handles = append(handles, ports.ContainerHandle{
			ID:              c.ID,
			Name:            name,
			State:           c.State,
			WorkspaceFolder: c.Labels["cordon.workspace-folder"],
			Labels:          c.Labels,
		})
	}
	return handles, nil
}

// waitExec polls until the exec completes, returning the exit code.
func (b *Backend) waitExec(ctx context.Context, execID string) (int, error) {
	for {
		inspect, err := b.client.ContainerExecInspect(ctx, execID)
		if err != nil {
			return -1, err
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// ListVolumes returns volume names matching the given labels.
func (b *Backend) ListVolumes(ctx context.Context, labels map[string]string) ([]string, error) {
	args := filters.NewArgs()
	for k, v := range labels {
		args.Add("label", k+"="+v)
	}
	vols, err := b.client.VolumeList(ctx, volume.ListOptions{Filters: args})
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(vols.Volumes))
	for _, v := range vols.Volumes {
		names = append(names, v.Name)
	}
	return names, nil
}
