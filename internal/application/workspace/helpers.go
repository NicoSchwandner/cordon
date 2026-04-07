package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
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

func (o *Orchestrator) configureGit(ctx context.Context, containerID, proxyAddr string) {
	// Credential helper returns a placeholder — the MITM proxy swaps it
	// with the real token. The real secret never enters the container.
	if placeholder, err := o.githubPlaceholder(ctx); err == nil {
		credHelper := fmt.Sprintf(`git config --global credential.helper '!f() { echo "username=x-access-token"; echo "password=%s"; }; f'`, placeholder)
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
	// Configure git to route through the Cordon proxy (same egress path as all
	// other traffic). This is more reliable than env vars alone — some git builds
	// or container images don't respect https_proxy consistently.
	if proxyAddr != "" {
		proxyURL := "http://" + proxyAddr
		proxyCfg := fmt.Sprintf(`git config --global http.proxy %s`, proxyURL)
		if _, err := o.backend.Exec(ctx, containerID, proxyCfg); err != nil {
			log.Printf("[workspace] warning: git proxy setup failed: %v", err)
		}
	}
}

// installCACert installs the MITM CA certificate into the container's system
// trust store so that TLS connections through the proxy are trusted by all tools.
func (o *Orchestrator) installCACert(ctx context.Context, containerID string) {
	if len(o.caPem) == 0 {
		return
	}
	// Write CA cert and update the trust store. Supports both Debian/Ubuntu
	// (update-ca-certificates) and Alpine (update-ca-certificates with a
	// different path). Falls back gracefully if neither is available.
	writeCmd := fmt.Sprintf(`cat > /usr/local/share/ca-certificates/cordon-ca.crt << 'CERT'
%s
CERT
`, string(o.caPem))
	if _, err := o.backend.Exec(ctx, containerID, writeCmd); err != nil {
		log.Printf("[workspace] warning: CA cert write failed: %v", err)
		return
	}
	if _, err := o.backend.Exec(ctx, containerID, "update-ca-certificates --fresh 2>/dev/null || true"); err != nil {
		log.Printf("[workspace] warning: CA cert install failed: %v", err)
	}
}

func (o *Orchestrator) cloneURL(repo string) string {
	// Plain HTTPS URL — no embedded credentials. The MITM proxy handles auth
	// by swapping the placeholder in the git credential helper's response.
	if !strings.Contains(repo, "://") {
		return "https://" + repo
	}
	return repo
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

// registerWorkspaceIP reads the workspace container's network IP and registers it
// in the workspace registry so the CONNECT handler can attribute MITM traffic.
// The gateway uses PROXY protocol to report the real client IP, so we register
// the workspace container's IP (not the gateway's).
func (o *Orchestrator) registerWorkspaceIP(ctx context.Context, containerID string, wsID uuid.UUID) {
	if o.registry == nil {
		return
	}
	output, err := o.backend.ExecWithOutput(ctx, containerID, "hostname -I")
	if err != nil {
		log.Printf("[workspace] warning: could not read container IP for registry: %v", err)
		return
	}
	// Extract IP from output — Docker exec may include binary stream headers,
	// so we match an IPv4 pattern rather than trusting field splitting.
	ip := extractIPv4(output)
	if ip == "" {
		log.Printf("[workspace] warning: could not parse container IP from output %q for workspace %s", output, wsID.String()[:8])
		return
	}
	log.Printf("[workspace] registering container IP %s → workspace %s", ip, wsID.String()[:8])
	o.registry.Register(ip, wsID)
}

// proxyEnv returns env vars that configure standard HTTP proxy settings,
// routing tools like git, curl, and apt through the Cordon proxy. Empty if
// egress enforcement is disabled.
func proxyEnv(internalProxyAddr string) []string {
	if internalProxyAddr == "" {
		return nil
	}
	proxyURL := "http://" + internalProxyAddr
	return []string{
		"CORDON_PROXY_ADDR=" + internalProxyAddr,
		"http_proxy=" + proxyURL,
		"https_proxy=" + proxyURL,
		"HTTP_PROXY=" + proxyURL,
		"HTTPS_PROXY=" + proxyURL,
		"no_proxy=localhost,127.0.0.1",
		"NO_PROXY=localhost,127.0.0.1",
	}
}

// sanitizeEnv replaces real secret values in env vars with their placeholder
// equivalents. This prevents real credentials from entering containers when
// devcontainer.json resolves ${localEnv:VAR} from the server's environment.
func (o *Orchestrator) sanitizeEnv(ctx context.Context, env map[string]string) map[string]string {
	if o.vault == nil {
		return env
	}
	refs, err := o.vault.ListRefs(ctx, o.tenantID)
	if err != nil || len(refs) == 0 {
		return env
	}

	// Build real→placeholder reverse map
	reverse := make(map[string]string, len(refs))
	for _, ref := range refs {
		real, err := o.vault.Resolve(ctx, o.tenantID, ref.Placeholder)
		if err != nil {
			continue
		}
		reverse[real] = ref.Placeholder
	}

	sanitized := make(map[string]string, len(env))
	for k, v := range env {
		for real, placeholder := range reverse {
			if strings.Contains(v, real) {
				v = strings.ReplaceAll(v, real, placeholder)
			}
		}
		sanitized[k] = v
	}
	return sanitized
}

// githubPlaceholder returns the placeholder string for the GITHUB_TOKEN secret.
func (o *Orchestrator) githubPlaceholder(ctx context.Context) (string, error) {
	if o.vault == nil {
		return "", fmt.Errorf("no vault configured")
	}
	return o.vault.PlaceholderFor(ctx, o.tenantID, "GITHUB_TOKEN")
}

// githubToken returns the real GitHub token value from the vault.
func (o *Orchestrator) githubToken(ctx context.Context) (string, error) {
	if o.vault == nil {
		return "", fmt.Errorf("no vault configured")
	}
	return o.vault.RevealValue(ctx, o.tenantID, "GITHUB_TOKEN")
}

// redactToken removes a GitHub token from output to prevent leaking in logs.
func redactToken(output, token string) string {
	if token == "" {
		return output
	}
	return strings.ReplaceAll(output, token, "***")
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
		expires = created.Add(8 * time.Hour) // fallback for containers with missing label
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

var ipv4Re = regexp.MustCompile(`\b(\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3})\b`)

// extractIPv4 finds the first IPv4 address in a string, ignoring any
// binary noise (e.g. Docker multiplexed stream headers).
func extractIPv4(s string) string {
	return ipv4Re.FindString(s)
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
