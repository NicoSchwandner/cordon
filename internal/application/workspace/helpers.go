package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// VolumeLister is an optional interface for backends that support listing volumes by label.
type VolumeLister interface {
	ListVolumes(ctx context.Context, labels map[string]string) ([]string, error)
}

func (o *Orchestrator) findPrimaryContainer(ctx context.Context, tenantID, workspaceID uuid.UUID) (ports.ContainerHandle, error) {
	handles, err := o.backend.ListContainers(ctx, map[string]string{
		"cordon.workspace": workspaceID.String(),
		"cordon.tenant":    tenantID.String(),
	})
	if err != nil {
		return ports.ContainerHandle{}, fmt.Errorf("querying containers: %w", err)
	}
	for _, h := range handles {
		if h.Labels["cordon.service-for"] == "" {
			return h, nil
		}
	}
	if len(handles) == 0 {
		return ports.ContainerHandle{}, fmt.Errorf("workspace not found")
	}
	return handles[0], nil
}

func (o *Orchestrator) configureGit(ctx context.Context, containerID string) {
	if o.token != "" {
		credHelper := `git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=$GITHUB_TOKEN"; }; f'`
		if _, err := o.backend.Exec(ctx, containerID, credHelper); err != nil {
			log.Printf("[workspace] warning: git credential helper setup failed: %v", err)
		}
	}
	if o.gitUser != nil {
		gitCfg := fmt.Sprintf(`git config --global user.name "%s" && git config --global user.email "%s"`, o.gitUser.Name, o.gitUser.Email)
		if _, err := o.backend.Exec(ctx, containerID, gitCfg); err != nil {
			log.Printf("[workspace] warning: git identity setup failed: %v", err)
		}
	}
}

func (o *Orchestrator) cloneURL(repo string) string {
	if o.token == "" {
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
	u.User = url.UserPassword("x-access-token", o.token)
	return u.String()
}

func (o *Orchestrator) resolveDefaultBranch(repoURL string) string {
	if o.gitHost != nil {
		return o.gitHost.ResolveDefaultBranch(repoURL)
	}
	return "main"
}

// createIsolatedNetwork creates a workspace network with optional egress enforcement.
// When egress policy is enabled, the network is internal (no external routing) and a
// gateway container bridges it to the Cordon proxy. Returns the proxy address that
// workspace containers should use (empty string if egress is disabled).
func (o *Orchestrator) createIsolatedNetwork(ctx context.Context, networkName string, tenantID, wsID uuid.UUID) (string, error) {
	labels := map[string]string{
		"cordon.tenant":    tenantID.String(),
		"cordon.workspace": wsID.String(),
	}

	_, err := o.backend.CreateNetwork(ctx, networkName, labels, o.egress.Enabled)
	if err != nil {
		return "", fmt.Errorf("creating network: %w", err)
	}

	if !o.egress.Enabled {
		return "", nil
	}

	proxyAddr, err := o.backend.EnsureProxyAccess(ctx, networkName, o.egress.ProxyAddr, labels)
	if err != nil {
		o.backend.RemoveNetwork(ctx, networkName)
		return "", fmt.Errorf("setting up proxy access: %w", err)
	}
	return proxyAddr, nil
}

func (o *Orchestrator) emitter(wsID uuid.UUID) func(step, msg string) {
	return func(step, msg string) {
		if o.progress != nil {
			o.progress.Send(wsID, step, msg)
		}
	}
}

func (o *Orchestrator) installRoutingLayer(ctx context.Context, cid string, wsID, tenantID uuid.UUID, serviceContainers map[string]string, emit func(string, string)) {
	svcJSON, _ := json.Marshal(serviceContainers)

	markerCmd := fmt.Sprintf(`mkdir -p /workspace/.cordon && echo '%s' > /workspace/.cordon/service-containers.json`, string(svcJSON))
	o.backend.Exec(ctx, cid, markerCmd)

	repoMap := map[string]map[string]string{}
	for shortName, svcName := range serviceContainers {
		repoMap[shortName] = map[string]string{
			"container": svcName,
			"path":      "/workspace/" + shortName,
		}
	}
	repoMapJSON, _ := json.Marshal(repoMap)
	repoMapCmd := fmt.Sprintf(`echo '%s' > /workspace/.cordon/repos.json`, string(repoMapJSON))
	o.backend.Exec(ctx, cid, repoMapCmd)

	emit("configuring_routing", "Setting up command routing...")
	tools := []string{"dotnet", "npm", "npx", "node", "go", "cargo", "python", "python3", "pip", "make", "gradle", "mvn", "yarn", "pnpm", "bun"}
	wrapperScript := `#!/bin/sh
exec cordon-agent exec "$(basename "$0")" "$@"`

	o.backend.Exec(ctx, cid, "mkdir -p /workspace/.cordon/bin")

	for _, tool := range tools {
		writeCmd := fmt.Sprintf(`cat > /workspace/.cordon/bin/%s << 'WRAPPER'
%s
WRAPPER
chmod +x /workspace/.cordon/bin/%s`, tool, wrapperScript, tool)
		o.backend.Exec(ctx, cid, writeCmd)
	}

	o.backend.Exec(ctx, cid, `echo 'export PATH="/workspace/.cordon/bin:$PATH"' >> /etc/profile.d/cordon-routing.sh`)

	envCmd := fmt.Sprintf(`cat >> /etc/profile.d/cordon-routing.sh << 'EOF'
export CORDON_WORKSPACE_ID="%s"
export CORDON_TENANT_ID="%s"
EOF`, wsID.String(), tenantID.String())
	o.backend.Exec(ctx, cid, envCmd)

	log.Printf("[workspace] routing layer installed (%d tool wrappers)", len(tools))
}

// handleToWorkspace converts a ContainerHandle (with labels) to a domain.Workspace.
func handleToWorkspace(h ports.ContainerHandle) domain.Workspace {
	wsID, _ := uuid.Parse(h.Labels["cordon.workspace"])
	tenantID, _ := uuid.Parse(h.Labels["cordon.tenant"])
	created, _ := time.Parse(time.RFC3339, h.Labels["cordon.created"])
	expires, _ := time.Parse(time.RFC3339, h.Labels["cordon.expires"])

	if created.IsZero() {
		created = time.Now().UTC()
	}
	if expires.IsZero() {
		expires = created.Add(24 * time.Hour)
	}

	status := domain.WorkspaceRunning
	switch h.State {
	case "paused":
		status = domain.WorkspaceSuspended
	case "exited", "dead", "removing":
		status = domain.WorkspaceDestroyed
	case "created", "restarting":
		status = domain.WorkspaceCreating
	}

	var repos []domain.RepoConfig
	if reposJSON := h.Labels["cordon.repos"]; reposJSON != "" {
		json.Unmarshal([]byte(reposJSON), &repos)
	}

	config := domain.WorkspaceConfig{Repos: repos}

	if mode := h.Labels["cordon.mode"]; mode == string(domain.WorkspaceModeInvestigation) {
		config.Mode = domain.WorkspaceModeInvestigation
		var shallowRepos []string
		if sr := h.Labels["cordon.shallow-repos"]; sr != "" {
			json.Unmarshal([]byte(sr), &shallowRepos)
		}
		config.Investigation = &domain.InvestigationState{
			CatalogOrg:   h.Labels["cordon.org"],
			ShallowRepos: shallowRepos,
		}
	}

	ws := domain.Workspace{
		ID:        wsID,
		TenantID:  tenantID,
		Name:      h.Labels["cordon.name"],
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
		Config:    config,
	}
	if sf := h.Labels["cordon.spawned-from"]; sf != "" {
		if id, err := uuid.Parse(sf); err == nil {
			ws.SpawnedFrom = &id
		}
	}
	return ws
}

func containerName(name string, wsID uuid.UUID) string {
	return fmt.Sprintf("cordon-%s-%s", sanitizeName(name), wsID.String()[:8])
}

func baseLabels(tenantID, wsID uuid.UUID, name string, now time.Time, maxLifetime time.Duration) map[string]string {
	return map[string]string{
		"cordon.tenant":    tenantID.String(),
		"cordon.workspace": wsID.String(),
		"cordon.name":      name,
		"cordon.created":   now.Format(time.RFC3339),
		"cordon.expires":   now.Add(maxLifetime).Format(time.RFC3339),
	}
}

func sanitizeName(name string) string {
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
