package docker

import (
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
	"github.com/docker/docker/api/types/network"
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
	if config.Repo != "" && p.builder != nil {
		return p.createFromRepo(ctx, tenantID, config)
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

// createFromRepo builds a devcontainer image, creates a container, clones the repo inside, and runs setup.
func (p *Provider) createFromRepo(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	containerName := p.containerName(config.Name, wsID)
	now := time.Now().UTC()
	start := time.Now()

	emit := func(step, msg string) {
		if p.progress != nil {
			p.progress.Send(wsID, step, msg)
		}
	}

	// Send first event with time estimate
	if p.progress != nil {
		p.progress.SendWithEstimate(wsID, config.Repo, "resolving_branch", "Resolving branch...")
	}

	// BaseBranch = build the devcontainer image from (where devcontainer.json lives)
	// Branch = checkout this branch in the workspace
	branch := config.Branch
	if branch == "" {
		branch = p.ResolveDefaultBranch(config.Repo)
	}
	baseBranch := config.BaseBranch
	if baseBranch == "" {
		baseBranch = branch
	}

	log.Printf("[workspace] creating devcontainer %s from %s (base=%s, branch=%s)", containerName, config.Repo, baseBranch, branch)

	// Build the devcontainer image from the base branch
	emit("building_image", "Building devcontainer image...")
	result, err := p.builder.Build(ctx, config.Repo, baseBranch, p.token, config.DevcontainerPath)
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

	// Labels include repo metadata
	labels := p.baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	labels["cordon.repo"] = config.Repo
	labels["cordon.base-branch"] = baseBranch
	labels["cordon.branch"] = branch
	labels["cordon.workspace-folder"] = result.WorkspaceFolder

	// Environment: merge devcontainer env + cordon env + git auth
	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	if p.token != "" {
		env = append(env,
			"GITHUB_TOKEN="+p.token,  // gh CLI reads this
			"GH_TOKEN="+p.token,      // gh CLI also reads this
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

	// Clone repo inside the running container
	emit("cloning_repo", fmt.Sprintf("Cloning repository (branch: %s)...", branch))
	cloneURL := p.cloneURL(config.Repo)
	log.Printf("[workspace] cloning repo inside container (branch=%s)", branch)
	cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", branch, cloneURL, result.WorkspaceFolder)
	if _, err := p.execInContainer(ctx, cid, cloneCmd); err != nil {
		if branch != baseBranch {
			// Branch doesn't exist yet — clone from base branch, then create the feature branch
			log.Printf("[workspace] branch %s not found, cloning %s and creating branch", branch, baseBranch)
			emit("cloning_repo", fmt.Sprintf("Branch %s not found, cloning %s and creating branch...", branch, baseBranch))
			fallbackCmd := fmt.Sprintf("git clone --branch %s %s %s", baseBranch, cloneURL, result.WorkspaceFolder)
			if _, err := p.execInContainer(ctx, cid, fallbackCmd); err != nil {
				log.Printf("[workspace] warning: clone failed: %v", err)
			} else {
				checkoutCmd := fmt.Sprintf("cd %s && git checkout -b %s", result.WorkspaceFolder, branch)
				if _, err := p.execInContainer(ctx, cid, checkoutCmd); err != nil {
					log.Printf("[workspace] warning: creating branch %s failed: %v", branch, err)
				}
			}
		} else {
			log.Printf("[workspace] warning: clone failed: %v", err)
		}
	}

	// Run postCreateCommand
	if len(result.PostCreateCommand) > 0 {
		emit("post_create", "Running post-create commands...")
	}
	for _, cmd := range result.PostCreateCommand {
		log.Printf("[workspace] running postCreateCommand: %s", cmd)
		shellCmd := fmt.Sprintf("cd %s && %s", result.WorkspaceFolder, cmd)
		if _, err := p.execInContainer(ctx, ws.ID.String()[:12], shellCmd); err != nil {
			log.Printf("[workspace] warning: postCreateCommand failed: %v", err)
		}
	}

	// Record build timing for future estimates
	if p.progress != nil && p.progress.Timing() != nil {
		p.progress.Timing().Record(config.Repo, time.Since(start))
	}

	log.Printf("[workspace] %s is ready", containerName)
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

func (p *Provider) startContainer(ctx context.Context, containerName, image string, labels map[string]string, env []string, config domain.WorkspaceConfig, networkID string, tenantID, wsID uuid.UUID, now time.Time) (domain.Workspace, error) {
	cpuQuota := int64(config.CPU) * 100000
	memLimit := int64(config.MemoryMB) * 1024 * 1024

	containerConfig := &container.Config{
		Image:  image,
		Labels: labels,
		Env:    env,
		Cmd:    []string{"sleep", "infinity"},
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
		return domain.Workspace{}, fmt.Errorf("creating container: %w", err)
	}

	log.Printf("[workspace] starting container %s", resp.ID[:12])
	if err := p.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		p.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true})
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
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}

	name := c.Labels["cordon.name"]
	log.Printf("[workspace] destroying %s (container=%s)", name, c.ID[:12])

	if err := p.client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
		log.Printf("[workspace] warning: container remove failed: %v", err)
	}

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

	return domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      c.Labels["cordon.name"],
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
		Config: domain.WorkspaceConfig{
			Repo:       c.Labels["cordon.repo"],
			BaseBranch: c.Labels["cordon.base-branch"],
			Branch:     c.Labels["cordon.branch"],
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
