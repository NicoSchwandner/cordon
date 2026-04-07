package workspace

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
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
		if h.ServiceFor == "" {
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
			slog.Warn("git credential helper setup failed", "component", "workspace", "error", err)
		}
	}
	if o.gitUser != nil {
		gitCfg := fmt.Sprintf(`git config --global user.name "%s" && git config --global user.email "%s"`, o.gitUser.Name, o.gitUser.Email)
		if _, err := o.backend.Exec(ctx, containerID, gitCfg); err != nil {
			slog.Warn("git identity setup failed", "component", "workspace", "error", err)
		}
	}
	// Configure git to route through the Cordon proxy (same egress path as all
	// other traffic). This is more reliable than env vars alone — some git builds
	// or container images don't respect https_proxy consistently.
	if proxyAddr != "" {
		proxyURL := "http://" + proxyAddr
		proxyCfg := fmt.Sprintf(`git config --global http.proxy %s`, proxyURL)
		if _, err := o.backend.Exec(ctx, containerID, proxyCfg); err != nil {
			slog.Warn("git proxy setup failed", "component", "workspace", "error", err)
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
		slog.Warn("CA cert write failed", "component", "workspace", "error", err)
		return
	}
	if _, err := o.backend.Exec(ctx, containerID, "update-ca-certificates --fresh 2>/dev/null || true"); err != nil {
		slog.Warn("CA cert install failed", "component", "workspace", "error", err)
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
		slog.Warn("could not read container IP for registry", "component", "workspace", "error", err)
		return
	}
	// Extract IP from output — Docker exec may include binary stream headers,
	// so we match an IPv4 pattern rather than trusting field splitting.
	ip := extractIPv4(output)
	if ip == "" {
		slog.Warn("could not parse container IP from output", "component", "workspace", "output", output, "workspace", wsID.String()[:8])
		return
	}
	slog.Info("registering container IP", "component", "workspace", "ip", ip, "workspace", wsID.String()[:8])
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

// registerEgressPolicy registers per-workspace egress rules if the workspace
// config includes an egress policy with additional allowed hosts.
func (o *Orchestrator) registerEgressPolicy(wsID uuid.UUID, config domain.WorkspaceConfig) {
	if o.egressManager == nil || config.EgressPolicy == nil || len(config.EgressPolicy.AdditionalHosts) == 0 {
		return
	}
	o.egressManager.SetWorkspacePolicy(wsID, config.EgressPolicy.AdditionalHosts)
	slog.Info("registered per-workspace egress policy", "component", "workspace", "workspace", wsID.String()[:8], "additional_hosts", len(config.EgressPolicy.AdditionalHosts))
}

// removeEgressPolicy removes per-workspace egress rules on workspace destruction.
func (o *Orchestrator) removeEgressPolicy(wsID uuid.UUID) {
	if o.egressManager == nil {
		return
	}
	o.egressManager.RemoveWorkspacePolicy(wsID)
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

	slog.Info("routing layer installed", "component", "workspace", "tool_wrappers", len(tools))
}

// handleToWorkspace converts a ContainerHandle to a domain.Workspace using typed fields.
func handleToWorkspace(h ports.ContainerHandle) domain.Workspace {
	created := h.CreatedAt
	if created.IsZero() {
		created = time.Now().UTC()
	}
	expires := h.ExpiresAt
	if expires.IsZero() {
		expires = created.Add(8 * time.Hour) // fallback for containers with missing metadata
	}

	status := mapContainerState(h.State)

	config := domain.WorkspaceConfig{Repos: h.Repos}

	if h.Mode == string(domain.WorkspaceModeInvestigation) {
		config.Mode = domain.WorkspaceModeInvestigation
		config.Investigation = &domain.InvestigationState{
			CatalogOrg:   h.CatalogOrg,
			ShallowRepos: h.ShallowRepos,
		}
	}

	ws := domain.Workspace{
		ID:        h.WorkspaceID,
		TenantID:  h.TenantID,
		Name:      h.WorkspaceName,
		Status:    status,
		CreatedAt: created,
		ExpiresAt: expires,
		Config:    config,
	}
	if h.SpawnedFrom != "" {
		if id, err := uuid.Parse(h.SpawnedFrom); err == nil {
			ws.SpawnedFrom = &id
		}
	}
	return ws
}

func mapContainerState(state string) domain.WorkspaceStatus {
	switch state {
	case "paused":
		return domain.WorkspaceSuspended
	case "exited", "dead", "removing":
		return domain.WorkspaceDestroyed
	case "created", "restarting":
		return domain.WorkspaceCreating
	default:
		return domain.WorkspaceRunning
	}
}

// workspaceCapDrop returns the Linux capabilities to drop for workspace containers.
// We drop ALL and selectively add back the minimum set needed for devcontainer
// workflows (package installation, file ownership, port binding).
func workspaceCapDrop() []string { return []string{"ALL"} }

// workspaceCapAdd returns the minimal Linux capabilities for workspace containers.
func workspaceCapAdd() []string {
	return []string{"CHOWN", "DAC_OVERRIDE", "FOWNER", "SETGID", "SETUID", "NET_BIND_SERVICE"}
}

// workspaceSecurityOpts returns Docker security options for workspace containers.
func workspaceSecurityOpts() []string { return []string{"no-new-privileges"} }

func containerName(name string, wsID uuid.UUID) string {
	return fmt.Sprintf("cordon-%s-%s", sanitizeName(name), wsID.String()[:8])
}

// workspaceNetworkName returns the network name for a workspace container.
// Centralizes the naming convention so it lives in one place.
func workspaceNetworkName(containerName string) string {
	return containerName + "-net"
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

// extractIPv4 finds the first IPv4 address in a string. Used to parse
// multi-IP output from `hostname -I`.
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
