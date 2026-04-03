# Coder as Foundation for Cordon — Technical Evaluation

## 1. Architecture

Coder is a monolithic Go server (`coderd`) with a React frontend (`site/`). Workspaces are provisioned via **Terraform** — each "template" is a Terraform module executed by a **provisioner daemon**. The `coder_agent` binary runs inside each workspace and phones home over **Tailscale/DERP** (WireGuard-based overlay network). There is no plugin system; extension happens through Terraform providers and the `coder_app` resource (which exposes arbitrary HTTP services from the workspace). The enterprise layer (`enterprise/`) wraps the AGPL core with license-gated features.

## 2. API

Full REST API with **22,500-line OpenAPI spec** (`coderd/apidoc/swagger.json`). Covers: workspace CRUD, builds (start/stop/destroy via `postWorkspaceBuilds`), template management, user auth, agents, and more. Terminal access uses **reconnecting-pty over WebSocket** (not raw xterm WS — Coder's agent multiplexes PTY sessions). API is well-documented with generated docs at `docs/reference/api/`.

## 3. Templates

Templates are pure Terraform. You define `coder_agent` (the workspace agent) and `coder_app` (web apps exposed from workspace). **Adding a proxy sidecar is straightforward** — you define it as a container/process in the Terraform template alongside the main workspace. The template system is highly flexible: any infrastructure Terraform can provision, Coder can use as a workspace.

## 4. Networking — THE CRITICAL ISSUE

Coder uses a **Tailscale-based overlay** (DERP/WireGuard) between `coderd` and the workspace `coder_agent`. All IDE/terminal traffic flows through this tunnel. **Workspace egress is NOT mediated by Coder** — the workspace has whatever network access its underlying infrastructure provides (Docker bridge, K8s pod network, VM NIC). To force egress through a custom proxy, you would need to:

- Configure iptables/network policies at the infrastructure layer (K8s NetworkPolicy, Docker network config)
- Or run a transparent proxy sidecar and configure the workspace to route through it

Coder's networking model **does not fight this** but **provides no help** — it's orthogonal. You own the egress story entirely.

## 5. Web Terminal & UI Customization

Built-in web terminal using **@xterm/xterm 5.5.0**. The terminal connects via reconnecting-pty WebSocket to the agent. **Injecting custom UI elements (approval overlays, agent panels) is hard** — the frontend is a single React app, not a micro-frontend. You would need to fork the `site/` directory or build a wrapper that embeds Coder's UI in an iframe. No extension points for custom UI panels.

## 6. License

**AGPL-3.0**. This is the main concern. AGPL requires that if you run modified Coder as a network service, you must release your source. Enterprise features gated behind license: workspace proxies, RBAC, audit logs, SCIM, HA, appearance customization, multi-org, AI bridge. **You cannot build a proprietary commercial product on AGPL Coder without either: (a) keeping your additions as a separate service that calls Coder's API, or (b) negotiating a commercial license with Coder Inc.**

## 7. Pain Points

- AGPL license is the biggest blocker for commercial use
- Terraform-based provisioning adds latency to workspace starts (20-60s typical)
- Enterprise features (workspace proxies, RBAC) needed for production use are license-gated
- Frontend is not designed for embedding or extension
- Breaking changes: Coder v2 was a complete rewrite from v1; the project is stable now but tightly coupled

## 8. Alternatives

| Component            | Coder                      | DevPod                            | Hocus                        | Eclipse Che         | Devcontainer CLI      | Raw Firecracker    |
| -------------------- | -------------------------- | --------------------------------- | ---------------------------- | ------------------- | --------------------- | ------------------ |
| **License**          | AGPL-3.0                   | MPL-2.0                           | MIT (archived)               | EPL-2.0             | MIT                   | Apache-2.0         |
| **Orchestration**    | Full (Terraform)           | Full (provider plugins)           | Full (Firecracker)           | Full (K8s)          | Minimal (Docker only) | None               |
| **Web UI/Terminal**  | Yes (React+xterm)          | Desktop app only, no web terminal | Yes                          | Yes (Theia/VS Code) | None                  | None               |
| **Proxy sidecar**    | Template-level (Terraform) | devcontainer.json + features      | N/A (dead)                   | K8s sidecar         | docker-compose        | Full control       |
| **UI customization** | Fork required              | N/A (desktop)                     | N/A (dead)                   | Theia plugins       | N/A                   | Build from scratch |
| **Status**           | Active, VC-backed          | Active, Loft Labs                 | **Dead** (archived Sep 2024) | Active, heavy       | Active, Microsoft     | Active             |

**DevPod** (MPL-2.0): Client-only, no server component. Providers are shell-script plugins. Great for devcontainer orchestration but **no web UI, no multi-user server, no centralized management**. You'd build the entire server/web layer yourself. Good license though.

**Eclipse Che**: K8s-native, heavy operator install. Uses Theia or VS Code. Extensible via plugins but massive operational overhead. EPL-2.0 is permissive enough.

**Devcontainer CLI** (MIT): Just the build/run layer. No lifecycle management, no UI. Maximum flexibility, minimum for free.

**Raw Firecracker/Kata**: Maximum control over isolation and networking. You build everything. Best for security guarantees but 6+ months of orchestration work.

## 9. The "Supabase Stack" for Cordon

| Concern                     | Recommended OSS Component                         | License          | Notes                                         |
| --------------------------- | ------------------------------------------------- | ---------------- | --------------------------------------------- |
| **Workspace orchestration** | Devcontainer CLI + custom Go service              | MIT              | Avoids AGPL; devcontainers are standard       |
| **Web IDE/editor**          | VS Code Server (code-server) or OpenVSCode Server | MIT              | Gitpod's OpenVSCode Server is clean           |
| **Terminal**                | xterm.js + custom WebSocket relay                 | MIT              | ~500 lines to build                           |
| **Secret management**       | HashiCorp Vault or SOPS                           | MPL-2.0 / Apache | Vault is overkill; SOPS for simple cases      |
| **Access proxy**            | Envoy or Pomerium                                 | Apache / Apache  | Pomerium = zero-trust proxy, purpose-built    |
| **Auth**                    | Ory Kratos + Ory Hydra (or Keycloak)              | Apache / Apache  | Ory is lightweight; Keycloak if you want SAML |
| **Egress control**          | Envoy + custom WASM filter or eBPF                | Apache           | This is Cordon's core differentiator        |

**Recommended approach**: Don't build on Coder. The AGPL license and UI inflexibility are deal-breakers. Instead, assemble: **Devcontainer CLI** (orchestration) + **OpenVSCode Server** (IDE) + **xterm.js** (terminal) + **Pomerium** (access proxy) + **Envoy** (egress proxy with custom filters) + **Ory** (auth). This gives you MIT/Apache licensing throughout, full control over the proxy layer (your core product), and a customizable web UI.
