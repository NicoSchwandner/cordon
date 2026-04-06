package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

func (o *Orchestrator) createBare(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	cName := containerName(config.Name, wsID)
	now := time.Now().UTC()

	log.Printf("[workspace] creating bare container %s (tenant=%s)", cName, tenantID.String()[:8])

	networkName := cName + "-net"
	internalProxyAddr, err := o.createIsolatedNetwork(ctx, networkName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	image := "mcr.microsoft.com/devcontainers/base:ubuntu"
	o.backend.PullImage(ctx, image)

	labels := baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	env = append(env, proxyEnv(internalProxyAddr)...)

	if _, err := o.backend.CreateContainer(ctx, ports.CreateContainerOpts{
		Name:     cName,
		Image:    image,
		Labels:   labels,
		Env:      env,
		Network:  networkName,
		CPU:      config.CPU,
		MemoryMB: config.MemoryMB,
	}); err != nil {
		o.backend.RemoveNetwork(ctx, networkName)
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

func (o *Orchestrator) createFromRepos(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	cName := containerName(config.Name, wsID)
	now := time.Now().UTC()
	start := time.Now()

	primary := config.PrimaryRepo()
	if primary == nil {
		return domain.Workspace{}, fmt.Errorf("no repos configured")
	}

	emit := o.emitter(wsID)

	// Send first event with time estimate
	if o.progress != nil {
		o.progress.SendWithEstimate(wsID, primary.URL, "resolving_branch", "Resolving branch...")
	}

	// Resolve branches for all repos
	for i := range config.Repos {
		repo := &config.Repos[i]
		if repo.Branch == "" {
			repo.Branch = o.resolveDefaultBranch(repo.URL)
		}
		if repo.BaseBranch == "" {
			repo.BaseBranch = repo.Branch
		}
	}

	log.Printf("[workspace] creating devcontainer %s from %s (%d repos)", cName, primary.URL, len(config.Repos))

	// Build devcontainer image from the primary repo's base branch
	emit("building_image", "Building devcontainer image...")
	dcPath := primary.DevcontainerPath
	if dcPath == "" {
		dcPath = config.DevcontainerPath
	}
	buildToken, _ := o.githubToken(ctx)
	result, err := o.builder.Build(ctx, primary.URL, primary.BaseBranch, buildToken, dcPath)
	if err != nil {
		return domain.Workspace{}, fmt.Errorf("building devcontainer image: %w", err)
	}
	if result.Cached {
		emit("building_image", "Using cached image")
	}

	// Create network with egress enforcement
	emit("creating_container", "Creating container...")
	networkName := cName + "-net"
	internalProxyAddr, err := o.createIsolatedNetwork(ctx, networkName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	// Labels
	labels := baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	reposJSON, _ := json.Marshal(config.Repos)
	labels["cordon.repos"] = string(reposJSON)
	if len(config.Repos) == 1 {
		labels["cordon.workspace-folder"] = "/workspace/" + domain.RepoShortName(config.Repos[0].URL)
	} else {
		labels["cordon.workspace-folder"] = "/workspace"
	}

	// Environment
	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}

	for k, v := range result.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, proxyEnv(internalProxyAddr)...)

	// Create and start the container
	handle, err := o.backend.CreateContainer(ctx, ports.CreateContainerOpts{
		Name:     cName,
		Image:    result.ImageName,
		Labels:   labels,
		Env:      env,
		Network:  networkName,
		CPU:      config.CPU,
		MemoryMB: config.MemoryMB,
	})
	if err != nil {
		if o.egress.Enabled {
			o.backend.RemoveProxyAccess(ctx, networkName)
		}
		o.backend.RemoveNetwork(ctx, networkName)
		return domain.Workspace{}, err
	}

	ws := domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      config.Name,
		Status:    domain.WorkspaceRunning,
		CreatedAt: now,
		ExpiresAt: now.Add(config.MaxLifetime),
		Config:    config,
	}

	cid := handle.ID

	// Register this container's IP so the CONNECT handler can attribute traffic
	o.registerWorkspaceIP(ctx, cid, wsID)

	// Install MITM CA cert so TLS through the proxy is trusted
	o.installCACert(ctx, cid)

	// Configure git
	emit("configuring_git", "Configuring git credentials...")
	o.configureGit(ctx, cid, internalProxyAddr)

	// Create workspace parent directory
	o.backend.Exec(ctx, cid, "mkdir -p /workspace")

	// Clone each repo as a sibling under /workspace/<short-name>/
	o.cloneRepos(ctx, cid, config.Repos, emit)

	// Phase 2: Start service containers for repos that need their own runtime
	serviceContainers := o.startServiceContainers(ctx, cName, networkName, wsID, tenantID, config, now, emit)

	// Phase 3: Install routing layer if service containers exist
	if len(serviceContainers) > 0 {
		o.installRoutingLayer(ctx, cid, wsID, tenantID, serviceContainers, emit)
	}

	// Run postCreateCommand from the primary repo's devcontainer
	if len(result.PostCreateCommand) > 0 {
		emit("post_create", "Running post-create commands...")
	}
	primaryDir := "/workspace/" + domain.RepoShortName(primary.URL)
	for _, cmd := range result.PostCreateCommand {
		log.Printf("[workspace] running postCreateCommand: %s", cmd)
		shellCmd := fmt.Sprintf("cd %s && %s", primaryDir, cmd)
		if _, err := o.backend.Exec(ctx, cid, shellCmd); err != nil {
			log.Printf("[workspace] warning: postCreateCommand failed: %v", err)
		}
	}

	// Record build timing for future estimates
	if o.progress != nil && o.progress.Timing() != nil {
		o.progress.Timing().Record(primary.URL, time.Since(start))
	}

	log.Printf("[workspace] %s is ready (%d repos, %d service containers)", cName, len(config.Repos), len(serviceContainers))
	return ws, nil
}

func (o *Orchestrator) createInvestigation(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	wsID := config.ID
	if wsID == uuid.Nil {
		wsID = uuid.New()
	}
	cName := containerName(config.Name, wsID)
	now := time.Now().UTC()

	if config.Investigation == nil || config.Investigation.CatalogOrg == "" {
		return domain.Workspace{}, fmt.Errorf("investigation workspace requires catalog org")
	}
	org := config.Investigation.CatalogOrg
	emit := o.emitter(wsID)

	emit("listing_repos", fmt.Sprintf("Fetching repos from %s...", org))

	if o.gitHost == nil {
		return domain.Workspace{}, fmt.Errorf("GitHub client not configured")
	}
	orgRepos, err := o.gitHost.ListOrgRepos(org)
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

	log.Printf("[workspace] creating investigation workspace %s from %s (%d repos)", cName, org, len(repoURLs))

	image := "mcr.microsoft.com/devcontainers/base:ubuntu"
	emit("pulling_image", "Pulling base image...")
	o.backend.PullImage(ctx, image)

	emit("creating_container", "Creating container...")
	networkName := cName + "-net"
	internalProxyAddr, err := o.createIsolatedNetwork(ctx, networkName, tenantID, wsID)
	if err != nil {
		return domain.Workspace{}, err
	}

	labels := baseLabels(tenantID, wsID, config.Name, now, config.MaxLifetime)
	labels["cordon.mode"] = string(domain.WorkspaceModeInvestigation)
	labels["cordon.org"] = org
	shallowJSON, _ := json.Marshal(repoURLs)
	labels["cordon.shallow-repos"] = string(shallowJSON)
	labels["cordon.workspace-folder"] = "/workspace"

	env := []string{
		"ZT_WORKSPACE_ID=" + wsID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}

	env = append(env, proxyEnv(internalProxyAddr)...)

	handle, err := o.backend.CreateContainer(ctx, ports.CreateContainerOpts{
		Name:     cName,
		Image:    image,
		Labels:   labels,
		Env:      env,
		Network:  networkName,
		CPU:      config.CPU,
		MemoryMB: config.MemoryMB,
	})
	if err != nil {
		if o.egress.Enabled {
			o.backend.RemoveProxyAccess(ctx, networkName)
		}
		o.backend.RemoveNetwork(ctx, networkName)
		return domain.Workspace{}, err
	}

	cid := handle.ID

	// Register this container's IP so the CONNECT handler can attribute traffic
	o.registerWorkspaceIP(ctx, cid, wsID)

	// Install MITM CA cert so TLS through the proxy is trusted
	o.installCACert(ctx, cid)

	// Configure git credentials
	emit("configuring_git", "Configuring git credentials...")
	o.configureGit(ctx, cid, internalProxyAddr)
	o.backend.Exec(ctx, cid, "mkdir -p /workspace/.cordon")

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
		go func(u string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			shortName := domain.RepoShortName(u)
			cloneURL := o.cloneURL(u)
			cloneCmd := fmt.Sprintf("GIT_LFS_SKIP_SMUDGE=1 git clone --depth=1 --single-branch %s /workspace/%s", cloneURL, shortName)
			if _, err := o.backend.Exec(ctx, cid, cloneCmd); err != nil {
				log.Printf("[workspace] warning: shallow clone of %s failed: %v", u, err)
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
	o.backend.Exec(ctx, cid, `which rg > /dev/null 2>&1 || (apt-get update -qq && apt-get install -y -qq ripgrep > /dev/null 2>&1)`)

	// Write initial activated-repos file
	o.backend.Exec(ctx, cid, `echo '[]' > /workspace/.cordon/activated-repos.json`)

	ws := domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      config.Name,
		Status:    domain.WorkspaceRunning,
		CreatedAt: now,
		ExpiresAt: now.Add(config.MaxLifetime),
		Config:    config,
	}
	ws.Config.Mode = domain.WorkspaceModeInvestigation
	ws.Config.Investigation = &domain.InvestigationState{
		CatalogOrg:     org,
		ShallowRepos:   repoURLs,
		ActivatedRepos: []string{},
	}

	cloned := total - len(failed)
	log.Printf("[workspace] investigation workspace %s ready (%d/%d repos cloned)", cName, cloned, total)
	return ws, nil
}

// cloneRepos clones each repo into the primary container.
func (o *Orchestrator) cloneRepos(ctx context.Context, cid string, repos []domain.RepoConfig, emit func(string, string)) {
	tokenForRedaction, _ := o.githubToken(ctx)
	for _, repo := range repos {
		shortName := domain.RepoShortName(repo.URL)
		cloneDir := "/workspace/" + shortName
		emit("cloning_repo", fmt.Sprintf("Cloning %s (branch: %s)...", shortName, repo.Branch))

		cloneURL := o.cloneURL(repo.URL)
		log.Printf("[workspace] cloning %s into %s (branch=%s)", repo.URL, cloneDir, repo.Branch)

		cloneCmd := fmt.Sprintf("git clone --branch %s %s %s 2>&1", repo.Branch, cloneURL, cloneDir)
		if output, err := o.backend.ExecWithOutput(ctx, cid, cloneCmd); err != nil {
			if repo.Branch != repo.BaseBranch {
				log.Printf("[workspace] branch %s not found, cloning %s and creating branch", repo.Branch, repo.BaseBranch)
				emit("cloning_repo", fmt.Sprintf("Branch %s not found, cloning %s...", repo.Branch, repo.BaseBranch))
				fallbackCmd := fmt.Sprintf("git clone --branch %s %s %s 2>&1", repo.BaseBranch, cloneURL, cloneDir)
				if output, err := o.backend.ExecWithOutput(ctx, cid, fallbackCmd); err != nil {
					log.Printf("[workspace] warning: clone of %s failed: %v\n%s", repo.URL, err, redactToken(output, tokenForRedaction))
				} else {
					checkoutCmd := fmt.Sprintf("cd %s && git checkout -b %s", cloneDir, repo.Branch)
					if _, err := o.backend.Exec(ctx, cid, checkoutCmd); err != nil {
						log.Printf("[workspace] warning: creating branch %s failed: %v", repo.Branch, err)
					}
				}
			} else {
				log.Printf("[workspace] warning: clone of %s failed: %v\n%s", repo.URL, err, redactToken(output, tokenForRedaction))
			}
		}
	}
}

// startServiceContainers creates service containers for repos that need their own runtime.
func (o *Orchestrator) startServiceContainers(ctx context.Context, primaryName, networkName string, wsID, tenantID uuid.UUID, config domain.WorkspaceConfig, now time.Time, emit func(string, string)) map[string]string {
	serviceContainers := map[string]string{}
	for _, repo := range config.Repos {
		if !repo.ServiceContainer || (repo.Primary && !repo.ServiceContainer) {
			continue
		}
		shortName := domain.RepoShortName(repo.URL)
		emit("building_service", fmt.Sprintf("Building service container for %s...", shortName))

		dcPath := repo.DevcontainerPath
		svcToken, _ := o.githubToken(ctx)
		svcResult, err := o.builder.Build(ctx, repo.URL, repo.BaseBranch, svcToken, dcPath)
		if err != nil {
			log.Printf("[workspace] warning: service container build for %s failed: %v", shortName, err)
			continue
		}
		if svcResult.Cached {
			emit("building_service", fmt.Sprintf("Using cached image for %s", shortName))
		}

		volName := fmt.Sprintf("cordon-vol-%s-%s", wsID.String()[:8], shortName)
		if err := o.backend.CreateVolume(ctx, volName, map[string]string{
			"cordon.workspace": wsID.String(),
			"cordon.repo":     shortName,
		}); err != nil {
			log.Printf("[workspace] warning: volume create for %s failed: %v", shortName, err)
			continue
		}

		svcContainerName := fmt.Sprintf("%s-%s-svc", primaryName, shortName)
		svcLabels := map[string]string{
			"cordon.tenant":      tenantID.String(),
			"cordon.workspace":   wsID.String(),
			"cordon.service-for": shortName,
			"cordon.name":        config.Name + "-" + shortName,
			"cordon.created":     now.Format(time.RFC3339),
		}

		svcEnv := []string{
			"ZT_WORKSPACE_ID=" + wsID.String(),
			"ZT_TENANT_ID=" + tenantID.String(),
			"CORDON_SERVICE_REPO=" + shortName,
		}
		for k, v := range svcResult.Env {
			svcEnv = append(svcEnv, k+"="+v)
		}

		svcHandle, err := o.backend.CreateContainer(ctx, ports.CreateContainerOpts{
			Name:   svcContainerName,
			Image:  svcResult.ImageName,
			Labels: svcLabels,
			Env:    svcEnv,
			Mounts: []ports.MountSpec{{
				Source: volName,
				Target: "/workspace/" + shortName,
			}},
			Network:  networkName,
			CPU:      config.CPU,
			MemoryMB: config.MemoryMB,
		})
		if err != nil {
			log.Printf("[workspace] warning: service container start for %s failed: %v", shortName, err)
			continue
		}

		cloneURL := o.cloneURL(repo.URL)
		cloneDir := "/workspace/" + shortName
		cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", repo.Branch, cloneURL, cloneDir)
		if _, err := o.backend.Exec(ctx, svcHandle.ID, cloneCmd); err != nil {
			log.Printf("[workspace] warning: clone into service container %s failed: %v", shortName, err)
		}

		for _, cmd := range svcResult.PostCreateCommand {
			shellCmd := fmt.Sprintf("cd %s && %s", cloneDir, cmd)
			if _, err := o.backend.Exec(ctx, svcHandle.ID, shellCmd); err != nil {
				log.Printf("[workspace] warning: service postCreateCommand for %s failed: %v", shortName, err)
			}
		}

		serviceContainers[shortName] = svcContainerName
		log.Printf("[workspace] service container %s is running for repo %s", svcContainerName, shortName)
	}
	return serviceContainers
}
