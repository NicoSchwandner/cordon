package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/application/progress"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// Orchestrator implements ports.WorkspaceService by composing a ComputeBackend
// with devcontainer building, GitHub operations, and progress reporting.
type Orchestrator struct {
	backend  ports.ComputeBackend
	builder  ports.ImageBuilder    // nil = bare containers only
	token    string                // GitHub token for authenticated git operations
	gitUser  *ports.GitIdentity    // resolved from git host API at startup
	gitHost  ports.GitHostClient   // git hosting platform client (nil-safe)
	progress *progress.Store       // optional progress event emitter
	egress   domain.EgressPolicy   // network-level egress enforcement
	caPem    []byte                // MITM CA cert PEM for container trust store injection
}

// Compile-time check that Orchestrator satisfies WorkspaceService.
var _ ports.WorkspaceService = (*Orchestrator)(nil)

// OrchestratorConfig holds the dependencies for creating an Orchestrator.
type OrchestratorConfig struct {
	Backend  ports.ComputeBackend
	Builder  ports.ImageBuilder
	Token    string
	GitHost  ports.GitHostClient
	Progress *progress.Store
	Egress   domain.EgressPolicy
	CAPem    []byte // MITM CA certificate to inject into containers
}

// NewOrchestrator creates a workspace orchestrator.
func NewOrchestrator(cfg OrchestratorConfig) *Orchestrator {
	o := &Orchestrator{
		backend:  cfg.Backend,
		builder:  cfg.Builder,
		token:    cfg.Token,
		gitHost:  cfg.GitHost,
		progress: cfg.Progress,
		egress:   cfg.Egress,
		caPem:    cfg.CAPem,
	}
	if o.egress.Enabled {
		log.Printf("[workspace] network-level egress enforcement enabled (proxy=%s)", o.egress.ProxyAddr)
	}

	// Resolve git identity from hosting platform API
	if cfg.GitHost != nil && cfg.GitHost.HasToken() {
		if id, err := cfg.GitHost.FetchUser(); err != nil {
			log.Printf("[workspace] warning: could not resolve git identity: %v", err)
		} else {
			log.Printf("[workspace] git identity: %s <%s>", id.Name, id.Email)
			o.gitUser = id
		}
	}

	// Log existing cordon containers from previous runs
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	existing, _ := cfg.Backend.ListContainers(ctx, map[string]string{"cordon.workspace": ""})
	if len(existing) > 0 {
		log.Printf("[workspace] found %d existing cordon container(s) from previous run", len(existing))
	}

	return o
}

func (o *Orchestrator) Create(ctx context.Context, tenantID uuid.UUID, config domain.WorkspaceConfig) (domain.Workspace, error) {
	if config.IsInvestigation() {
		return o.createInvestigation(ctx, tenantID, config)
	}
	if config.HasRepos() && o.builder != nil {
		return o.createFromRepos(ctx, tenantID, config)
	}
	return o.createBare(ctx, tenantID, config)
}

func (o *Orchestrator) Get(ctx context.Context, tenantID, workspaceID uuid.UUID) (domain.Workspace, error) {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	ws := handleToWorkspace(handle)

	// Enrich investigation state from in-container file (labels are immutable)
	if ws.Config.Mode == domain.WorkspaceModeInvestigation && ws.Status == domain.WorkspaceRunning {
		output, err := o.backend.ExecWithOutput(ctx, handle.ID, `cat /workspace/.cordon/activated-repos.json 2>/dev/null || echo '[]'`)
		if err == nil && ws.Config.Investigation != nil {
			var activated []string
			json.Unmarshal([]byte(strings.TrimSpace(output)), &activated)
			ws.Config.Investigation.ActivatedRepos = activated
		}
	}
	return ws, nil
}

func (o *Orchestrator) List(ctx context.Context, tenantID uuid.UUID) ([]domain.Workspace, error) {
	handles, err := o.backend.ListContainers(ctx, map[string]string{
		"cordon.tenant": tenantID.String(),
	})
	if err != nil {
		return nil, fmt.Errorf("listing containers: %w", err)
	}

	result := make([]domain.Workspace, 0, len(handles))
	for _, h := range handles {
		if h.Labels["cordon.service-for"] != "" || h.Labels["cordon.egress-gateway"] != "" {
			continue
		}
		result = append(result, handleToWorkspace(h))
	}
	return result, nil
}

func (o *Orchestrator) Suspend(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	return o.backend.PauseContainer(ctx, handle.ID)
}

func (o *Orchestrator) Resume(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return err
	}
	return o.backend.UnpauseContainer(ctx, handle.ID)
}

func (o *Orchestrator) Destroy(ctx context.Context, tenantID, workspaceID uuid.UUID) error {
	handles, err := o.backend.ListContainers(ctx, map[string]string{
		"cordon.workspace": workspaceID.String(),
	})
	if err != nil {
		return fmt.Errorf("querying containers: %w", err)
	}
	if len(handles) == 0 {
		return fmt.Errorf("workspace not found")
	}

	log.Printf("[workspace] destroying workspace %s (%d containers)", workspaceID.String()[:8], len(handles))

	var networkName string
	for _, h := range handles {
		if h.Labels["cordon.egress-gateway"] != "" {
			continue // gateway containers are cleaned up via RemoveProxyAccess
		}
		svcFor := h.Labels["cordon.service-for"]
		if svcFor != "" {
			log.Printf("[workspace] removing service container for %s (%s)", svcFor, h.ID[:12])
		} else {
			log.Printf("[workspace] removing primary container (%s)", h.ID[:12])
			if h.Name != "" {
				networkName = h.Name + "-net"
			}
		}
		if err := o.backend.RemoveContainer(ctx, h.ID); err != nil {
			log.Printf("[workspace] warning: container remove failed: %v", err)
		}
	}

	if volumeLister, ok := o.backend.(VolumeLister); ok {
		volumes, _ := volumeLister.ListVolumes(ctx, map[string]string{
			"cordon.workspace": workspaceID.String(),
		})
		for _, name := range volumes {
			log.Printf("[workspace] removing volume %s", name)
			if err := o.backend.RemoveVolume(ctx, name); err != nil {
				log.Printf("[workspace] warning: volume remove failed: %v", err)
			}
		}
	}

	if networkName != "" {
		if o.egress.Enabled {
			if err := o.backend.RemoveProxyAccess(ctx, networkName); err != nil {
				log.Printf("[workspace] warning: proxy access cleanup failed: %v", err)
			}
		}
		if err := o.backend.RemoveNetwork(ctx, networkName); err != nil {
			log.Printf("[workspace] warning: network remove failed: %v", err)
		}
	}

	log.Printf("[workspace] workspace destroyed")
	return nil
}

func (o *Orchestrator) Exec(ctx context.Context, tenantID, workspaceID uuid.UUID, cmd []string) (int, error) {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return -1, err
	}
	exitCode, _, err := o.backend.ExecRaw(ctx, handle.ID, cmd)
	return exitCode, err
}

func (o *Orchestrator) ExecInRepo(ctx context.Context, workspaceID uuid.UUID, repoName string, cmd []string) (int, string, error) {
	handles, err := o.backend.ListContainers(ctx, map[string]string{
		"cordon.workspace": workspaceID.String(),
	})
	if err != nil {
		return -1, "", fmt.Errorf("querying containers: %w", err)
	}

	var targetID string
	for _, h := range handles {
		svcFor := h.Labels["cordon.service-for"]
		if repoName != "" && svcFor == repoName {
			targetID = h.ID
			break
		}
		if svcFor == "" {
			if repoName == "" {
				targetID = h.ID
				break
			}
			if targetID == "" {
				targetID = h.ID
			}
		}
	}
	if targetID == "" {
		return -1, "", fmt.Errorf("no container found for repo %q", repoName)
	}

	return o.backend.ExecRaw(ctx, targetID, cmd)
}

// OpenTerminal creates an interactive terminal session for a workspace.
func (o *Orchestrator) OpenTerminal(ctx context.Context, tenantID, workspaceID uuid.UUID, opts ports.TerminalOpts) (ports.TerminalSession, error) {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return nil, err
	}

	if opts.WorkingDir == "" {
		opts.WorkingDir = handle.WorkspaceFolder
	}
	if opts.WorkingDir == "" {
		opts.WorkingDir = "/"
	}

	if len(opts.Cmd) == 0 {
		shellCmd := fmt.Sprintf(
			`cd %s && if command -v bash >/dev/null 2>&1; then exec bash -li; else exec sh -i; fi`,
			opts.WorkingDir,
		)
		opts.Cmd = []string{"/bin/sh", "-c", shellCmd}
	}

	return o.backend.OpenTerminal(ctx, handle.ID, opts)
}

// ActivateRepo promotes a shallow-cloned repo in an investigation workspace.
func (o *Orchestrator) ActivateRepo(ctx context.Context, tenantID, workspaceID uuid.UUID, repoURL string) error {
	handle, err := o.findPrimaryContainer(ctx, tenantID, workspaceID)
	if err != nil {
		return fmt.Errorf("finding workspace: %w", err)
	}

	if handle.Labels["cordon.mode"] != string(domain.WorkspaceModeInvestigation) {
		return fmt.Errorf("workspace is not an investigation workspace")
	}

	var shallowRepos []string
	if sr := handle.Labels["cordon.shallow-repos"]; sr != "" {
		json.Unmarshal([]byte(sr), &shallowRepos)
	}
	found := false
	for _, u := range shallowRepos {
		if u == repoURL || domain.RepoShortName(u) == repoURL {
			repoURL = u
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("repo %q not found in investigation workspace", repoURL)
	}

	shortName := domain.RepoShortName(repoURL)
	emit := o.emitter(workspaceID)
	emit("activating_repo", fmt.Sprintf("Activating %s...", shortName))

	if o.builder == nil {
		return fmt.Errorf("devcontainer builder not available")
	}

	defaultBranch := o.resolveDefaultBranch(repoURL)
	emit("building_image", fmt.Sprintf("Building devcontainer for %s...", shortName))
	result, err := o.builder.Build(ctx, repoURL, defaultBranch, o.token, "")
	if err != nil {
		return fmt.Errorf("building devcontainer for %s: %w", shortName, err)
	}
	if result.Cached {
		emit("building_image", fmt.Sprintf("Using cached image for %s", shortName))
	}

	newWSID := uuid.New()
	now := time.Now().UTC()
	maxLifetime := 8 * time.Hour
	cName := containerName(shortName, newWSID)
	labels := baseLabels(tenantID, newWSID, shortName, now, maxLifetime)
	labels["cordon.spawned-from"] = workspaceID.String()
	repoJSON, _ := json.Marshal([]domain.RepoConfig{{URL: repoURL, Branch: defaultBranch, Primary: true}})
	labels["cordon.repos"] = string(repoJSON)

	emit("creating_workspace", fmt.Sprintf("Starting workspace for %s...", shortName))

	networkName := cName + "-net"
	internalProxyAddr, err := o.createIsolatedNetwork(ctx, networkName, tenantID, newWSID)
	if err != nil {
		return err
	}

	env := []string{
		"ZT_WORKSPACE_ID=" + newWSID.String(),
		"ZT_TENANT_ID=" + tenantID.String(),
	}
	for k, v := range result.Env {
		env = append(env, k+"="+v)
	}
	env = append(env, proxyEnv(internalProxyAddr)...)

	newHandle, err := o.backend.CreateContainer(ctx, ports.CreateContainerOpts{
		Name:     cName,
		Image:    result.ImageName,
		Labels:   labels,
		Env:      env,
		Network:  networkName,
		CPU:      2,
		MemoryMB: 4096,
	})
	if err != nil {
		return fmt.Errorf("starting workspace container: %w", err)
	}

	o.installCACert(ctx, newHandle.ID)
	emit("cloning_repo", fmt.Sprintf("Cloning %s...", shortName))
	o.configureGit(ctx, newHandle.ID, internalProxyAddr)
	o.backend.Exec(ctx, newHandle.ID, "mkdir -p /workspace")

	cloneURL := o.cloneURL(repoURL)
	cloneDir := "/workspace/" + shortName
	cloneCmd := fmt.Sprintf("git clone --branch %s %s %s", defaultBranch, cloneURL, cloneDir)
	if _, err := o.backend.Exec(ctx, newHandle.ID, cloneCmd); err != nil {
		return fmt.Errorf("cloning %s: %w", shortName, err)
	}

	for _, cmd := range result.PostCreateCommand {
		shellCmd := fmt.Sprintf("cd %s && %s", cloneDir, cmd)
		if _, err := o.backend.Exec(ctx, newHandle.ID, shellCmd); err != nil {
			log.Printf("[workspace] warning: postCreateCommand for %s failed: %v", shortName, err)
		}
	}

	readCmd := `cat /workspace/.cordon/activated-repos.json 2>/dev/null || echo '[]'`
	output, _ := o.backend.ExecWithOutput(ctx, handle.ID, readCmd)
	var activated []string
	json.Unmarshal([]byte(strings.TrimSpace(output)), &activated)
	activated = append(activated, repoURL)
	activatedJSON, _ := json.Marshal(activated)
	writeCmd := fmt.Sprintf(`echo '%s' > /workspace/.cordon/activated-repos.json`, string(activatedJSON))
	o.backend.Exec(ctx, handle.ID, writeCmd)

	log.Printf("[workspace] activated %s as workspace %s (spawned from investigation %s)", shortName, newWSID.String()[:8], workspaceID.String()[:8])
	return nil
}
