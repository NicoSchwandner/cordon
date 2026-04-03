package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	dockerimage "github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/application/progress"
	"github.com/nicobistolfi/cordon/internal/domain"
	"github.com/nicobistolfi/cordon/internal/infrastructure/devcontainer"
)

// gitIdentity holds the git user.name and user.email for configuring containers.
type gitIdentity struct {
	Name  string
	Email string
}

// Provider manages Docker-based workspaces.
// Container labels are the source of truth — no in-memory state survives restarts.
type Provider struct {
	client          *client.Client
	builder         *devcontainer.Builder // nil = bare containers only
	token           string                // GitHub token for authenticated git operations
	gitUser         *gitIdentity          // resolved from GitHub API at startup
	defaultBranches sync.Map              // repo URL -> default branch name
	progress        *progress.Store       // optional progress event emitter
}

// NewProvider creates a Docker workspace provider.
// builder may be nil (devcontainer features disabled).
// token may be empty (only public repos supported).
func NewProvider(builder *devcontainer.Builder, token string) (*Provider, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("creating docker client: %w", err)
	}

	// Log existing cordon containers from previous runs
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	existing, _ := cli.ContainerList(ctx, container.ListOptions{
		Filters: filters.NewArgs(filters.Arg("label", "cordon.workspace")),
	})
	if len(existing) > 0 {
		log.Printf("[workspace] found %d existing cordon container(s) from previous run", len(existing))
	}

	p := &Provider{client: cli, builder: builder, token: token}

	// Resolve git identity from GitHub API
	if token != "" {
		if id, err := fetchGitHubUser(token); err != nil {
			log.Printf("[workspace] warning: could not resolve git identity: %v", err)
		} else {
			log.Printf("[workspace] git identity: %s <%s>", id.Name, id.Email)
			p.gitUser = id
		}
	}

	return p, nil
}

// SetProgressStore attaches a progress store for emitting creation events.
func (p *Provider) SetProgressStore(s *progress.Store) {
	p.progress = s
}

func (p *Provider) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	if config.HasRepos() && p.builder != nil {
		return p.createFromRepos(ctx, tenantID, config)
	}
	return p.createBare(ctx, tenantID, config)
}

// createBare creates a container from the default base image (no repo).
func (p *Provider) createBare(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	containerName := p.containerName(config.Name, wsID)
	now := time.Now().UTC()

	log.Printf("[workspace] creating bare container %s (tenant=%s)", containerName, tenantID.String()[:8])

	networkID, err := p.createNetwork(ctx, containerName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	image := "mcr.microsoft.com/devcontainers/base:ubuntu"
	p.pullImage(ctx, image)

	labels := p.baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}

	ws, err := p.startContainer(ctx, containerName, image, labels, env, config, networkID, tenantID, wsID, now)
	if err != nil {
		p.client.NetworkRemove(ctx, networkID)
		return domain.Workspace{}, err
	}
	return ws, nil
}

// createFromRepos builds a devcontainer image from the primary repo, creates a container,
// clones all repos as siblings inside, and runs setup.
func (p *Provider) createFromRepos(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	containerName := p.containerName(config.Name, wsID)
	now := time.Now().UTC()
	start := time.Now()

	primary := config.PrimaryRepo()
	if primary == nil {
		return domain.Workspace{}, fmt.Errorf("no repos configured")
	}

	emit := func(step, msg string) {
		if p.progress != nil {
			p.progress.Send(wsID, step, msg)
		}
	}

	// Send first event with time estimate
	if p.progress != nil {
		p.progress.SendWithEstimate(wsID, primary.URL, "resolving_branch", "Resolving branch...")
	}

	// Resolve branches for all repos
	for i := range config.Repos {
		repo := &config.Repos[i]
		if repo.Branch == "" {
			repo.Branch = p.ResolveDefaultBranch(repo.URL)
		}
		if repo.BaseBranch == "" {
			repo.BaseBranch = repo.Branch
		}
	}

	log.Printf("[workspace] creating devcontainer %s from %s (%d repos)", containerName, primary.URL, len(config.Repos))

	// Build the devcontainer image from the primary repo's base branch
	emit("building_image", "Building devcontainer image...")
	dcPath := primary.DevcontainerPath
	if dcPath == "" {
		dcPath = config.DevcontainerPath
	}
	result, err := p.builder.Build(ctx, primary.URL, primary.BaseBranch, p.token, dcPath)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("building devcontainer image: %w", err)
	}
	if result.Cached {
		emit("building_image", "Using cached image")
	}

	// Create network
	emit("creating_container", "Creating container...")
	networkID, err := p.createNetwork(ctx, containerName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	// Labels: store repos as JSON
	labels := p.baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	reposJSON, _ := json.Marshal(config.Repos)
	labels["cordon.repos"] = string(reposJSON)
	labels["cordon.workspace-folder"] = "/workspace"

	// Environment: merge devcontainer env + cordon env + git auth
	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	if p.token != "" {
		env = append(env,
			"GITHUB_TOKEN="+p.token,
			"GH_TOKEN="+p.token,
		)
	}
	for k, v := range result.Env {
		env = append(env, k+"="+v)
	}

	// Create and start the container
	ws, err := p.startContainer(ctx, containerName, result.ImageName, labels, env, config, networkID, tenantID, wsID, now)
	if err != nil {
		p.client.NetworkRemove(ctx, networkID)
		return domain.Workspace{}, err
	}

	cid := ws.ID.String()[:12]

	// Configure git: credential helper + user identity
	emit("configuring_git", "Configuring git credentials...")
	if p.token != "" {
		credHelper := `git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f'`
		if _, err := p.execInContainer(ctx, cid, credHelper); err != nil {
			log.Printf("[workspace] warning: git credential helper setup failed: %v", err)
		}
	}
	if p.gitUser != nil {
		gitCfg := fmt.Sprintf(`git config --global user.name "%s" && git config --global user.email "%s"`, p.gitUser.Name, p.gitUser.Email)
		if _, err := p.execInContainer(ctx, cid, gitCfg); err != nil {
			log.Printf("[workspace] warning: git identity setup failed: %v", err)
		}
	}

	// Create workspace parent directory
	if _, err := p.execInContainer(ctx, cid, "mkdir -p /workspace"); err != nil {
		log.Printf("[workspace] warning: creating /workspace failed: %v", err)
	}

	// Clone each repo as a sibling under /workspace/<short-name>/
	for _, repo := range config.Repos {
		shortName := domain.RepoShortName(repo.URL)
		cloneDir := "/workspace/" + shortName
		emit("cloning_repo", fmt.Sprintf("Cloning %s (branch: %s)...", shortName, repo.Branch))

		cloneURL := p.cloneURL(repo.URL)
		log.Printf("[workspace] cloning %s into %s (branch=%s)", repo.URL, cloneDir, repo.Branch)

		cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", repo.Branch, cloneURL, cloneDir)
		if _, err := p.execInContainer(ctx, cid, cloneCmd); err != nil {
			if repo.Branch != repo.BaseBranch {
				log.Printf("[workspace] branch %s not found, cloning %s and creating branch", repo.Branch, repo.BaseBranch)
				emit("cloning_repo", fmt.Sprintf("Branch %s not found, cloning %s...", repo.Branch, repo.BaseBranch))
				fallbackCmd := fmt.Sprintf("git clone --branch %s %s %s", repo.BaseBranch, cloneURL, cloneDir)
				if _, err := p.execInContainer(ctx, cid, fallbackCmd); err != nil {
					log.Printf("[workspace] warning: clone of %s failed: %v", repo.URL, err)
				} else {
					checkoutCmd := fmt.Sprintf("cd %s && git checkout -b %s", cloneDir, repo.Branch)
					if _, err := p.execInContainer(ctx, cid, checkoutCmd); err != nil {
						log.Printf("[workspace] warning: creating branch %s failed: %v", repo.Branch, err)
					}
				}
			} else {
				log.Printf("[workspace] warning: clone of %s failed: %v", repo.URL, err)
			}
		}
	}

	// Phase 2: Start service containers for repos that need their own runtime
	serviceContainers := map[string]string{} // repoShortName -> container ID
	for _, repo := range config.Repos {
		if !repo.ServiceContainer || (repo.Primary && !repo.ServiceContainer) {
			continue
		}
		shortName := domain.RepoShortName(repo.URL)
		emit("building_service", fmt.Sprintf("Building service container for %s...", shortName))

		// Build devcontainer image for service repo
		dcPath := repo.DevcontainerPath
		svcResult, err := p.builder.Build(ctx, repo.URL, repo.BaseBranch, p.token, dcPath)
		if err != nil {
			log.Printf("[workspace] warning: service container build for %s failed: %v", shortName, err)
			continue
		}
		if svcResult.Cached {
			emit("building_service", fmt.Sprintf("Using cached image for %s", shortName))
		}

		// Create named volume for this repo's files
		volName := fmt.Sprintf("cordon-vol-%s-%s", wsID.String()[:8], shortName)
		_, err = p.client.VolumeCreate(ctx, volume.CreateOptions{
			Name: volName,
			Labels: map[string]string{
				"cordon.workspace": wsID.String(),
				"cordon.repo":     shortName,
			},
		})
		if err != nil {
			log.Printf("[workspace] warning: volume create for %s failed: %v", shortName, err)
			continue
		}

		// Copy repo files from primary container to the volume
		// We do this by creating a temp container that mounts the volume,
		// then copy from primary. Simpler: exec cp in primary after mounting.
		// Actually, we'll mount the volume in the service container and clone there.

		svcContainerName := fmt.Sprintf("%s-%s-svc", containerName, shortName)
		svcLabels := map[string]string{
			"cordon.tenant":      tenantID.String(),
			"cordon.workspace":   wsID.String(),
			"cordon.service-for": shortName,
			"cordon.name":        config.Name + "-" + shortName,
			"cordon.created":     now.Format(time.RFC3339),
		}

		svcMount := mount.Mount{
			Type:   mount.TypeVolume,
			Source: volName,
			Target: "/workspace/" + shortName,
		}

		svcEnv := []string{
			"ZT_WORKSPACE_ID=" + wsID.String(),
			"ZT_TENANT_ID=" + tenantID.String(),
			"CORDON_SERVICE_REPO=" + shortName,
		}
		if p.token != "" {
			svcEnv = append(svcEnv, "GITHUB_TOKEN="+p.token, "GH_TOKEN="+p.token)
		}
		for k, v := range svcResult.Env {
			svcEnv = append(svcEnv, k+"="+v)
		}

		networkName := containerName + "-net"
		err = p.startContainerRaw(ctx, startContainerOpts{
			name:        svcContainerName,
			image:       svcResult.ImageName,
			labels:      svcLabels,
			env:         svcEnv,
			mounts:      []mount.Mount{svcMount},
			networkName: networkName,
			cpu:         config.CPU,
			memoryMB:    config.MemoryMB,
		})
		if err != nil {
			log.Printf("[workspace] warning: service container start for %s failed: %v", shortName, err)
			continue
		}

		// Clone repo into the service container's volume
		svcCID := svcContainerName // use name for exec lookup
		cloneURL := p.cloneURL(repo.URL)
		cloneDir := "/workspace/" + shortName
		cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", repo.Branch, cloneURL, cloneDir)
		if _, err := p.execInContainerByName(ctx, svcContainerName, cloneCmd); err != nil {
			log.Printf("[workspace] warning: clone into service container %s failed: %v", shortName, err)
		}

		// Run service container's postCreateCommand
		for _, cmd := range svcResult.PostCreateCommand {
			shellCmd := fmt.Sprintf("cd %s && %s", cloneDir, cmd)
			if _, err := p.execInContainerByName(ctx, svcContainerName, shellCmd); err != nil {
				log.Printf("[workspace] warning: service postCreateCommand for %s failed: %v", shortName, err)
			}
		}

		serviceContainers[shortName] = svcContainerName
		log.Printf("[workspace] service container %s is running for repo %s", svcContainerName, shortName)
		_ = svcCID
	}

	// Phase 3: Install routing layer if service containers exist
	if len(serviceContainers) > 0 {
		svcJSON, _ := json.Marshal(serviceContainers)

		// Write service container mapping
		markerCmd := fmt.Sprintf(`mkdir -p /workspace/.cordon && echo '%s' > /workspace/.cordon/service-containers.json`, string(svcJSON))
		p.execInContainer(ctx, cid, markerCmd)

		// Write repo map for cordon-agent exec routing
		repoMap := map[string]map[string]string{}
		for shortName, svcName := range serviceContainers {
			repoMap[shortName] = map[string]string{
				"container": svcName,
				"path":      "/workspace/" + shortName,
			}
		}
		repoMapJSON, _ := json.Marshal(repoMap)
		repoMapCmd := fmt.Sprintf(`echo '%s' > /workspace/.cordon/repos.json`, string(repoMapJSON))
		p.execInContainer(ctx, cid, repoMapCmd)

		// Create shell wrapper directory and wrappers for common tools
		emit("configuring_routing", "Setting up command routing...")
		tools := []string{"dotnet", "npm", "npx", "node", "go", "cargo", "python", "python3", "pip", "make", "gradle", "mvn", "yarn", "pnpm", "bun"}
		wrapperScript := `#!/bin/sh
exec cordon-agent exec "$(basename "$0")" "$@"`

		mkdirCmd := "mkdir -p /workspace/.cordon/bin"
		p.execInContainer(ctx, cid, mkdirCmd)

		for _, tool := range tools {
			writeCmd := fmt.Sprintf(`cat > /workspace/.cordon/bin/%s << 'WRAPPER'
%s
WRAPPER
chmod +x /workspace/.cordon/bin/%s`, tool, wrapperScript, tool)
			p.execInContainer(ctx, cid, writeCmd)
		}

		// Add wrapper bin to PATH via shell profile
		profileCmd := `echo 'export PATH="/workspace/.cordon/bin:$PATH"' >> /etc/profile.d/cordon-routing.sh`
		p.execInContainer(ctx, cid, profileCmd)

		// Set routing env vars in primary container
		envCmd := fmt.Sprintf(`cat >> /etc/profile.d/cordon-routing.sh << 'EOF'
export CORDON_WORKSPACE_ID="%s"
export CORDON_TENANT_ID="%s"
EOF`, wsID.String(), tenantID.String())
		p.execInContainer(ctx, cid, envCmd)

		log.Printf("[workspace] routing layer installed (%d tool wrappers)", len(tools))
	}

	// Run postCreateCommand from the primary repo's devcontainer
	if len(result.PostCreateCommand) > 0 {
		emit("post_create", "Running post-create commands...")
	}
	primaryDir := "/workspace/" + domain.RepoShortName(primary.URL)
	for _, cmd := range result.PostCreateCommand {
		log.Printf("[workspace] running postCreateCommand: %s", cmd)
		shellCmd := fmt.Sprintf("cd %s && %s", primaryDir, cmd)
		if _, err := p.execInContainer(ctx, cid, shellCmd); err != nil {
			log.Printf("[workspace] warning: postCreateCommand failed: %v", err)
		}
	}

	// Record build timing for future estimates
	if p.progress != nil && p.progress.Timing() != nil {
		p.progress.Timing().Record(primary.URL, time.Since(start))
	}

	log.Printf("[workspace] %s is ready (%d repos, %d service containers)", containerName, len(config.Repos), len(serviceContainers))
	return ws, nil
}

// --- Shared helpers ---

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
		io.Copy(io.Discard, reader)
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

// --- Read operations (unchanged) ---

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

// ContainerID returns the Docker container ID for a workspace (for terminal relay).
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

// --- Internal helpers ---

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

	return domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      c.Labels["cordon.name"],
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
		Config: domain.WorkspaceConfig{
			Repos: repos,
		},
	}
}

// fetchGitHubUser queries the GitHub API for the authenticated user's name and email.
func fetchGitHubUser(token string) (*gitIdentity, error) {
	req, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
		Email string `json:"email"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, err
	}

	name := user.Name
	if name == "" {
		name = user.Login
	}
	email := user.Email
	if email == "" {
		email = user.Login + "@users.noreply.github.com"
	}

	return &gitIdentity{Name: name, Email: email}, nil
}

// ResolveDefaultBranch returns the default branch for a repo, querying GitHub API
// and caching the result. Falls back to "main" on any error.
func (p *Provider) ResolveDefaultBranch(repoURL string) string {
	if cached, ok := p.defaultBranches.Load(repoURL); ok {
		return cached.(string)
	}

	branch := "main"
	if p.token != "" {
		if owner, repo, err := parseOwnerRepo(repoURL); err == nil {
			if b, err := fetchDefaultBranch(p.token, owner, repo); err == nil {
				branch = b
			} else {
				log.Printf("[workspace] warning: could not detect default branch for %s: %v", repoURL, err)
			}
		}
	}

	p.defaultBranches.Store(repoURL, branch)
	return branch
}

// parseOwnerRepo extracts owner and repo from a GitHub URL.
// Handles: "github.com/org/repo", "https://github.com/org/repo.git", etc.
func parseOwnerRepo(repoURL string) (string, string, error) {
	raw := repoURL
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(u.Host, "github.com") {
		return "", "", fmt.Errorf("not a GitHub URL: %s", repoURL)
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from %s", repoURL)
	}
	return parts[0], parts[1], nil
}

// fetchDefaultBranch queries the GitHub API for a repository's default branch.
func fetchDefaultBranch(token, owner, repo string) (string, error) {
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GitHub API returned %d", resp.StatusCode)
	}

	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if result.DefaultBranch == "" {
		return "main", nil
	}
	return result.DefaultBranch, nil
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
