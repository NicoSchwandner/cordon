package docker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/application/progress"
	"github.com/nicobistolfi/cordon/internal/domain"
	"github.com/nicobistolfi/cordon/internal/infrastructure/devcontainer"
	gh "github.com/nicobistolfi/cordon/internal/infrastructure/github"
)

// Provider manages Docker-based workspaces.
// Container labels are the source of truth — no in-memory state survives restarts.
type Provider struct {
	client   *client.Client
	builder  *devcontainer.Builder // nil = bare containers only
	token    string                // GitHub token for authenticated git operations
	gitUser  *gh.GitIdentity       // resolved from GitHub API at startup
	github   *gh.Client            // shared GitHub API client
	progress *progress.Store       // optional progress event emitter
}

// NewProvider creates a Docker workspace provider.
// builder may be nil (devcontainer features disabled).
// token may be empty (only public repos supported).
// ghClient provides shared GitHub API access (may be nil).
func NewProvider(builder *devcontainer.Builder, token string, ghClient *gh.Client) (*Provider, error) {
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

	p := &Provider{client: cli, builder: builder, token: token, github: ghClient}

	// Resolve git identity from GitHub API
	if ghClient != nil && ghClient.HasToken() {
		if id, err := ghClient.FetchUser(); err != nil {
			log.Printf("[workspace] warning: could not resolve git identity: %v", err)
		} else {
			log.Printf("[workspace] git identity: %s <%s>", id.Name, id.Email)
			p.gitUser = id
		}
	}

	return p, nil
}

// GitHub returns the shared GitHub client for use by handlers.
func (p *Provider) GitHub() *gh.Client {
	return p.github
}

// SetProgressStore attaches a progress store for emitting creation events.
func (p *Provider) SetProgressStore(s *progress.Store) {
	p.progress = s
}

func (p *Provider) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	if config.IsInvestigation() {
		return p.createInvestigation(ctx, tenantID, config)
	}
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

// createInvestigation creates a container with shallow clones of all repos from a GitHub org.
func (p *Provider) createInvestigation(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	containerName := p.containerName(config.Name, wsID)
	now := time.Now().UTC()

	if config.Investigation == nil || config.Investigation.CatalogOrg == "" {
		return domain.Workspace{}, fmt.Errorf("investigation workspace requires catalog org")
	}
	org := config.Investigation.CatalogOrg

	emit := func(step, msg string) {
		if p.progress != nil {
			p.progress.Send(wsID, step, msg)
		}
	}

	emit("listing_repos", fmt.Sprintf("Fetching repos from %s...", org))

	// Fetch org repos, filter archived
	if p.github == nil {
		return domain.Workspace{}, fmt.Errorf("GitHub client not configured")
	}
	orgRepos, err := p.github.ListOrgRepos(org)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("listing org repos: %w", err)
	}
	var repoURLs []string
	for _, r := range orgRepos {
		if !r.Archived {
			repoURLs = append(repoURLs, "github.com/"+r.FullName)
		}
	}
	if len(repoURLs) == 0 {
		return domain.Workspace{}, fmt.Errorf("no non-archived repos found in org %s", org)
	}

	log.Printf("[workspace] creating investigation workspace %s from %s (%d repos)", containerName, org, len(repoURLs))

	// Pull base image (no devcontainer build)
	image := "mcr.microsoft.com/devcontainers/base:ubuntu"
	emit("pulling_image", "Pulling base image...")
	p.pullImage(ctx, image)

	// Create network
	emit("creating_container", "Creating container...")
	networkID, err := p.createNetwork(ctx, containerName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	// Labels: investigation-specific
	labels := p.baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	labels["cordon.mode"] = string(domain.WorkspaceModeInvestigation)
	labels["cordon.org"] = org
	shallowJSON, _ := json.Marshal(repoURLs)
	labels["cordon.shallow-repos"] = string(shallowJSON)
	labels["cordon.workspace-folder"] = "/workspace"

	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	if p.token != "" {
		env = append(env, "GITHUB_TOKEN="+p.token, "GH_TOKEN="+p.token)
	}

	ws, err := p.startContainer(ctx, containerName, image, labels, env, config, networkID, tenantID, wsID, now)
	if err != nil {
		p.client.NetworkRemove(ctx, networkID)
		return domain.Workspace{}, err
	}

	cid := ws.ID.String()[:12]

	// Configure git credentials
	emit("configuring_git", "Configuring git credentials...")
	if p.token != "" {
		credHelper := `git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f'`
		p.execInContainer(ctx, cid, credHelper)
	}
	if p.gitUser != nil {
		gitCfg := fmt.Sprintf(`git config --global user.name "%s" && git config --global user.email "%s"`, p.gitUser.Name, p.gitUser.Email)
		p.execInContainer(ctx, cid, gitCfg)
	}

	p.execInContainer(ctx, cid, "mkdir -p /workspace/.cordon")

	// Parallel shallow clone with semaphore
	const concurrency = 12
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	var completed atomic.Int32
	total := len(repoURLs)
	var failedMu sync.Mutex
	var failed []string

	emit("cloning_repos", fmt.Sprintf("Cloning 0/%d repos...", total))

	for _, repoURL := range repoURLs {
		wg.Add(1)
		go func(url string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			shortName := domain.RepoShortName(url)
			cloneURL := p.cloneURL(url)
			cloneCmd := fmt.Sprintf("GIT_LFS_SKIP_SMUDGE=1 git clone --depth=1 --single-branch %s /workspace/%s", cloneURL, shortName)
			if _, err := p.execInContainer(ctx, cid, cloneCmd); err != nil {
				log.Printf("[workspace] warning: shallow clone of %s failed: %v", url, err)
				failedMu.Lock()
				failed = append(failed, shortName)
				failedMu.Unlock()
			}

			n := completed.Add(1)
			emit("cloning_repos", fmt.Sprintf("Cloning %d/%d repos...", n, total))
		}(repoURL)
	}
	wg.Wait()

	if len(failed) > 0 {
		log.Printf("[workspace] %d repos failed to clone: %v", len(failed), failed)
	}

	// Install ripgrep for fast search
	emit("installing_tools", "Installing ripgrep...")
	p.execInContainer(ctx, cid, `which rg > /dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq ripgrep > /dev/null 2>&1)`)

	// Write initial activated-repos file
	p.execInContainer(ctx, cid, `echo '[]' > /workspace/.cordon/activated-repos.json`)

	// Populate investigation state on the returned workspace
	ws.Config.Mode = domain.WorkspaceModeInvestigation
	ws.Config.Investigation = &domain.InvestigationState{
		CatalogOrg:     org,
		ShallowRepos:   repoURLs,
		ActivatedRepos: []string{},
	}

	cloned := total - len(failed)
	log.Printf("[workspace] investigation workspace %s ready (%d/%d repos cloned)", containerName, cloned, total)
	return ws, nil
}

// ActivateRepo promotes a shallow-cloned repo in an investigation workspace by spinning up
// a new service container with the repo's devcontainer (reuses Phase 2 infrastructure).
func (p *Provider) ActivateRepo(ctx context.Context, tenantID, workspaceID uuid.UUID, repoURL string) error {
	c, err := p.findContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}

	// Verify investigation mode
	if c.Labels["cordon.mode"] != string(domain.WorkspaceModeInvestigation) {
		return fmt.Errorf("workspace is not an investigation workspace")
	}

	// Verify repo is in the shallow list
	var shallowRepos []string
	if sr := c.Labels["cordon.shallow-repos"]; sr != "" {
		json.Unmarshal([]byte(sr), &shallowRepos)
	}
	found := false
	for _, u := range shallowRepos {
		if u == repoURL || domain.RepoShortName(u) == repoURL {
			repoURL = u // normalize to full URL
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("repo %q not found in investigation workspace", repoURL)
	}

	shortName := domain.RepoShortName(repoURL)

	emit := func(step, msg string) {
		if p.progress != nil {
			p.progress.Send(workspaceID, step, msg)
		}
	}

	emit("activating_repo", fmt.Sprintf("Activating %s...", shortName))

	// Build devcontainer image for this repo
	if p.builder == nil {
		return fmt.Errorf("devcontainer builder not available")
	}

	defaultBranch := p.ResolveDefaultBranch(repoURL)
	emit("building_image", fmt.Sprintf("Building devcontainer for %s...", shortName))
	result, err := p.builder.Build(ctx, repoURL, defaultBranch, p.token, "")
	if err != nil {
		return fmt.Errorf("building devcontainer for %s: %w", shortName, err)
	}
	if result.Cached {
		emit("building_image", fmt.Sprintf("Using cached image for %s", shortName))
	}

	// Create a full workspace for this repo (appears on dashboard like any other)
	newWSID := uuid.New()
	now := time.Now().UTC()
	maxLifetime := 24 * time.Hour
	containerName := p.containerName(shortName, newWSID)
	labels := p.baseLabels(tenantID, newWSID, shortName, now, maxLifetime)
	labels["cordon.spawned-from"] = workspaceID.String() // link back to investigation workspace

	// Single-repo config for proper workspace metadata
	repoJSON, _ := json.Marshal([]domain.RepoConfig{{URL: repoURL, Branch: defaultBranch, Primary: true}})
	labels["cordon.repos"] = string(repoJSON)

	emit("creating_workspace", fmt.Sprintf("Starting workspace for %s...", shortName))

	// Create network
	_, err = p.createNetwork(ctx, containerName, tenantID, newWSID)
	if err != nil {
		return fmt.Errorf("creating network: %w", err)
	}
	networkName := containerName + "-net"

	env := []string{
		"ZT_WORKSPACE_ID=" + newWSID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	if p.token != "" {
		env = append(env, "GITHUB_TOKEN="+p.token, "GH_TOKEN="+p.token)
	}
	for k, v := range result.Env {
		env = append(env, k+"="+v)
	}

	err = p.startContainerRaw(ctx, startContainerOpts{
		name:        containerName,
		image:       result.ImageName,
		labels:      labels,
		env:         env,
		networkName: networkName,
		cpu:         2,
		memoryMB:    4096,
	})
	if err != nil {
		return fmt.Errorf("starting workspace container: %w", err)
	}

	// Clone repo (full, not shallow)
	emit("cloning_repo", fmt.Sprintf("Cloning %s...", shortName))
	cloneURL := p.cloneURL(repoURL)
	cloneDir := "/workspace/" + shortName
	if p.token != "" {
		credHelper := `git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f'`
		p.execInContainerByName(ctx, containerName, credHelper)
	}
	if p.gitUser != nil {
		gitCfg := fmt.Sprintf(`git config --global user.name "%s" && git config --global user.email "%s"`, p.gitUser.Name, p.gitUser.Email)
		p.execInContainerByName(ctx, containerName, gitCfg)
	}
	if _, err := p.execInContainerByName(ctx, containerName, "mkdir -p /workspace"); err != nil {
		log.Printf("[workspace] warning: mkdir /workspace failed: %v", err)
	}
	cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", defaultBranch, cloneURL, cloneDir)
	if _, err := p.execInContainerByName(ctx, containerName, cloneCmd); err != nil {
		return fmt.Errorf("cloning %s: %w", shortName, err)
	}

	// Run postCreateCommand
	for _, cmd := range result.PostCreateCommand {
		shellCmd := fmt.Sprintf("cd %s && %s", cloneDir, cmd)
		if _, err := p.execInContainerByName(ctx, containerName, shellCmd); err != nil {
			log.Printf("[workspace] warning: postCreateCommand for %s failed: %v", shortName, err)
		}
	}

	// Update activated-repos.json in the investigation container
	cid := c.ID[:12]
	readCmd := `cat /workspace/.cordon/activated-repos.json 2>/dev/null || echo '[]'`
	output, _ := p.execInContainerWithOutput(ctx, cid, readCmd)
	var activated []string
	json.Unmarshal([]byte(strings.TrimSpace(output)), &activated)
	activated = append(activated, repoURL)
	activatedJSON, _ := json.Marshal(activated)
	writeCmd := fmt.Sprintf(`echo '%s' > /workspace/.cordon/activated-repos.json`, string(activatedJSON))
	p.execInContainer(ctx, cid, writeCmd)

	log.Printf("[workspace] activated %s as workspace %s (spawned from investigation %s)", shortName, newWSID.String()[:8], workspaceID.String()[:8])
	return nil
}

// Container helpers, exec helpers, workspace CRUD operations, and utility
// functions are in container.go, exec.go, and workspace_ops.go respectively.
