# Cordon: Zero-Trust Developer Environment

## Vision

A remote-first secure development platform where **no code runs on the developer's machine**. Developers connect via browser or pipe sessions to their local terminal. Ephemeral workspaces run on a server. Secrets never leave the server. Every operation is classified, gated, and audit-logged. AI agents run inside sandboxes with full visibility into what they're doing.

**Design philosophy:** Like Supabase — assemble battle-tested open-source components (all MIT/Apache licensed), add a unique security and UX layer on top that nobody else provides.

## Threat Model

### Assumptions

- The developer's PC may be compromised (malware, supply chain attack, malicious dependency)
- AI coding agents execute untrusted code (prompt injection, malicious suggestions)
- Agents have legitimate need to query databases and call APIs for investigation/debugging
- Extensions and terminal commands are arbitrary code execution by design
- The only genuine isolation boundary is the network — local sandboxing is insufficient

### Attack Vectors Defended Against

1. **Credential theft** — secrets never leave the server; proxy injects credentials server-side
2. **Data exfiltration** — workspace egress filtered through Envoy proxy with allowlist
3. **Destructive operations** — SQL/HTTP mutations classified by tier and gated with approval
4. **Lateral movement** — ephemeral workspaces are isolated; destroyed after use
5. **Prompt injection via authorized channels** — tier system + bulk read detection + approval gates
6. **Persistent compromise** — workspaces are ephemeral; no state carries between sessions
7. **Extension/terminal escape** — code runs in microVM on server, not on developer's machine

### Explicitly Out of Scope (v1)

- Compromised server infrastructure (assume server is trusted)
- Hardware-level attacks
- Social engineering of the human approver
- Clipboard/screen exfiltration (v2: restrict clipboard to text-only, audit pastes)

## Architecture

```
Developer's Machine (thin client only)
  │
  │  Option A: Browser → https://zt.company.com (Web UI)
  │  Option B: zt connect <workspace> (pipe to local terminal)
  │
  │  HTTPS / WebSocket — terminal I/O only, no code execution
  ▼
┌──────────────────────────────────────────────────────────────────┐
│  Cordon Server                                                  │
│                                                                   │
│  ┌─────────────────────┐  ┌─────────────┐  ┌─────────────────┐  │
│  │ Web UI Layer         │  │ API Server  │  │ Auth            │  │
│  │ (SvelteKit)          │  │ (Go)        │  │ (Ory Kratos +   │  │
│  │                      │  │             │  │  Hydra)         │  │
│  │ • Dashboard          │  │             │  │                 │  │
│  │ • Approval overlays  │  │             │  │ SSO / OIDC      │  │
│  │ • Agent op panel     │  │             │  │                 │  │
│  │ • Audit timeline     │  │             │  │                 │  │
│  └──────────┬───────────┘  └──────┬──────┘  └─────────────────┘  │
│             │                      │                              │
│  ┌──────────▼──────────────────────▼─────────────────────────┐   │
│  │  Workspace Orchestrator (Go)                               │   │
│  │                                                            │   │
│  │  Uses: Devcontainer CLI (MIT) for build/run                │   │
│  │  Uses: Firecracker (prod) / Docker+gVisor (dev)            │   │
│  │                                                            │   │
│  │  Each workspace contains:                                  │   │
│  │   ├── OpenVSCode Server (MIT) — full VS Code in browser    │   │
│  │   ├── xterm.js terminal via WebSocket relay                │   │
│  │   ├── Cloned repo (read-write within workspace)            │   │
│  │   ├── Language servers, build tools, AI agents             │   │
│  │   ├── ZT Proxy Sidecar                                     │   │
│  │   │    ├── Envoy (routing + TLS)                           │   │
│  │   │    ├── Go ext_authz service (tier classify, secret     │   │
│  │   │    │   swap, audit, approval gate)                     │   │
│  │   │    └── ~1-2ms overhead per request                     │   │
│  │   └── TTL (auto-suspend/destroy after idle)                │   │
│  └───────────────────────────────────────────────────────────┘   │
│                                                                   │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐  ┌──────────────────┐ │
│  │ Secret   │  │ Audit    │  │ Policy   │  │ Approval Queue   │ │
│  │ Vault    │  │ Store    │  │ Engine   │  │ (WebSocket push  │ │
│  │ (SOPS)   │  │ (PG)    │  │          │  │  to browser/CLI) │ │
│  └──────────┘  └──────────┘  └──────────┘  └──────────────────┘ │
└──────────────────────────────────────────────────────────────────┘
```

## Tech Stack

Two languages: **Go** (backend) and **TypeScript** (frontend). Single build mental model per side.

### Backend: Go

Everything server-side is Go. One language, one build system, one CI pipeline.

| Component              | Implementation                | Notes                                      |
| ---------------------- | ----------------------------- | ------------------------------------------ |
| API Server             | Go HTTP server                | Workspace mgmt, auth, WebSocket endpoints  |
| Workspace Orchestrator | Go + Devcontainer CLI         | Provision, suspend, resume, destroy        |
| Proxy Sidecar          | Go ext_authz/ext_proc service | Called by Envoy for every external request |
| Tier Classifier        | Go (in ext_authz service)     | SQL parsing + HTTP classification          |
| Secret Swap            | Go (in ext_authz service)     | Placeholder → real credential at proxy     |
| Audit Writer           | Go → PostgreSQL               | Append-only writes                         |
| Approval Engine        | Go + WebSocket                | Push to browser/CLI, collect decisions     |
| Terminal Relay         | Go WebSocket proxy            | xterm.js ↔ workspace PTY                   |
| CLI (`zt`)             | Go binary                     | Thin client: connect, agent start          |

**Why Go only (no Rust WASM):** The proxy sidecar runs as a Go process that Envoy calls via ext_authz protocol. This adds ~1-2ms per external request — negligible for developer workflows. Keeping one language means one build system, one debugging story, and contributors only need Go expertise. If we ever need sub-millisecond (unlikely), we can port just the hot filter to Rust WASM later — that's an optimization, not an architecture decision.

### Frontend: SvelteKit

| Component             | Implementation          | Notes                                      |
| --------------------- | ----------------------- | ------------------------------------------ |
| Dashboard             | SvelteKit SSR           | Workspace list, team admin, settings       |
| Audit Timeline        | Svelte + WebSocket      | Real-time feed, filterable, color-coded    |
| Approval Overlays     | Svelte + WebSocket      | Push-based, inline in editor or standalone |
| Agent Operation Panel | Svelte + WebSocket      | Live agent feed, pause/resume              |
| Editor Integration    | OpenVSCode Server embed | Full VS Code in browser via iframe         |
| Terminal              | xterm.js Svelte wrapper | WebSocket to workspace PTY                 |

### OSS Component Stack

All MIT/Apache licensed — commercially friendly.

| Concern                   | Component                                                                     | License            |
| ------------------------- | ----------------------------------------------------------------------------- | ------------------ |
| Workspace build/run       | [Devcontainer CLI](https://github.com/devcontainers/cli)                      | MIT                |
| Web IDE                   | [OpenVSCode Server](https://github.com/gitpod-io/openvscode-server)           | MIT                |
| Terminal emulation        | [xterm.js](https://github.com/xtermjs/xterm.js)                               | MIT                |
| Network proxy             | [Envoy](https://www.envoyproxy.io/)                                           | Apache 2.0         |
| Identity-aware routing    | [Pomerium](https://www.pomerium.com/)                                         | Apache 2.0         |
| Auth                      | [Ory Kratos](https://www.ory.sh/kratos/) + [Hydra](https://www.ory.sh/hydra/) | Apache 2.0         |
| Secrets                   | [SOPS](https://github.com/getsops/sops)                                       | MPL 2.0            |
| Audit store               | PostgreSQL                                                                    | PostgreSQL License |
| VM isolation (prod)       | [Firecracker](https://firecracker-microvm.github.io/)                         | Apache 2.0         |
| Container isolation (dev) | Docker + [gVisor](https://gvisor.dev/)                                        | Apache 2.0         |

## Code Architecture (Clean Architecture)

Inspired by the saas-starter pattern: strict layer separation, interface-driven DI, architecture tests that enforce invariants.

```
cordon/
├── cmd/
│   ├── server/            # Main server entry point
│   └── zt/                # CLI thin client
├── internal/
│   ├── domain/            # Entities, value objects, enums (no external deps)
│   │   ├── workspace.go   # Workspace, WorkspaceStatus
│   │   ├── tier.go        # Tier enum, TierResult
│   │   ├── audit.go       # AuditEntry
│   │   ├── secret.go      # SecretEntry, SecretRef
│   │   └── approval.go    # ApprovalRequest, ApprovalDecision
│   │
│   ├── application/       # Service interfaces, DTOs, result types (depends only on domain)
│   │   ├── ports/         # Interface definitions (what infrastructure must implement)
│   │   │   ├── workspace.go   # WorkspaceService port
│   │   │   ├── proxy.go       # ProxyService, TierClassifier port
│   │   │   ├── audit.go       # AuditStore port
│   │   │   ├── secret.go      # SecretVault port
│   │   │   └── approval.go    # ApprovalService port
│   │   ├── workspace/     # Workspace use cases
│   │   ├── proxy/         # Proxy/tier classification use cases
│   │   ├── audit/         # Audit query use cases
│   │   └── approval/      # Approval flow use cases
│   │
│   ├── infrastructure/    # Implementations (depends on application + domain)
│   │   ├── docker/        # Docker workspace provisioning
│   │   ├── firecracker/   # Firecracker workspace provisioning (prod)
│   │   ├── devcontainer/  # Devcontainer CLI integration
│   │   ├── envoy/         # Envoy ext_authz service + control plane
│   │   ├── postgres/      # Audit store, secret store implementations
│   │   ├── sops/          # SOPS secret encryption
│   │   ├── ory/           # Ory Kratos/Hydra auth integration
│   │   └── websocket/     # Terminal relay, approval push
│   │
│   └── api/               # HTTP handlers, middleware, WebSocket endpoints (outermost)
│       ├── handlers/      # Route handlers grouped by domain
│       ├── middleware/     # Auth, logging, error handling
│       └── ws/            # WebSocket handlers (terminal, approval, agent feed)
│
├── web/                   # SvelteKit frontend
│   └── src/
│       ├── lib/
│       │   ├── platform/      # Auth, workspace management, team admin
│       │   ├── security/      # Approval overlays, audit dashboard, agent panel
│       │   └── shared/
│       │       ├── components/ # Pure UI primitives (no domain knowledge)
│       │       └── api/        # Typed API client
│       └── routes/
│           ├── /              # Workspace list / landing
│           ├── /workspace/    # Editor + terminal + agent panel
│           ├── /audit/        # Audit timeline
│           └── /settings/     # Policies, secrets, egress rules
│
├── archtest/              # Architecture constraint tests
└── e2e/                   # End-to-end test scenarios
```

### Architecture Tests

Enforce critical invariants at test time — security rules that must never be violated by any code change:

```go
// Layer dependency rules
"internal/domain must not import internal/application"
"internal/domain must not import internal/infrastructure"
"internal/application must not import internal/infrastructure"
"internal/application must not import internal/api"

// Security invariants
"All api/handlers must use AuthMiddleware — no unauthenticated endpoints"
"All proxy routes must call TierClassifier — no bypass path"
"SecretVault.Decrypt must only be called from infrastructure/envoy — no other caller"
"AuditStore.Write must be called before HTTP response is sent — no fire-and-forget"
"Tier 4 classification must always return HTTP 403 — no code path allows execution"
"No real credential string may appear in any api/ response type"
"All WebSocket handlers must require authentication"
"Workspace delete must cascade-clean all associated resources"
```

### Design Patterns

| Pattern                        | Implementation                                                                                   |
| ------------------------------ | ------------------------------------------------------------------------------------------------ |
| **Dependency Injection**       | Interface-driven; `application/ports/` defines what infra must implement; wired in `cmd/server/` |
| **Result types**               | `TierResult`, `ApprovalResult`, `WorkspaceResult` — no exceptions for expected outcomes          |
| **Error responses**            | RFC 7807 ProblemDetails with stable `code` field (e.g. `tier_blocked`, `egress_denied`)          |
| **Platform vs Security split** | Frontend: `lib/platform/` (auth, workspaces) vs `lib/security/` (tiers, approvals, audit)        |
| **Shared components**          | `lib/shared/components/` — pure UI primitives with no domain knowledge                           |
| **Typed API client**           | SvelteKit `$lib/shared/api/client.ts` — typed fetch wrapper with auth token refresh              |

## What Cordon Builds Custom (Our Unique Layer)

The OSS components handle commodity concerns. Cordon's value-add is everything below:

1. **Tier Classification Engine** — SQL parser + HTTP classifier that assigns risk tiers to every operation. Supports custom overrides per project.

2. **Approval Gate** — pauses Tier 3 operations, pushes context-rich prompts to browser/CLI via WebSocket. Supports one-time, session, and pattern-based grants.

3. **Agent Operation Visibility Panel** — real-time feed of what AI agents are doing inside the workspace. Pause/resume agent execution. No existing product has this.

4. **Workspace Orchestrator Glue** — wires Devcontainer CLI + OpenVSCode Server + Envoy + xterm.js into a seamless single-command experience.

5. **`zt` CLI** — thin Go binary for piping remote sessions to the developer's own terminal and one-command agent workflows.

6. **Web Dashboard** — SvelteKit app: audit timeline, workspace management, policy configuration, team administration.

7. **Secret Swap Service** — Go ext_authz service that intercepts Envoy requests and replaces placeholder tokens with real credentials.

8. **Architecture Tests** — enforce security invariants at test time (auth, tier classification, secret isolation, cascade cleanup).

## Core Components (Detail)

### 1. Developer Access (Two Modes)

**Browser mode (primary):**

- Open `https://zt.company.com` → SSO login → workspace dashboard
- Click workspace → OpenVSCode Server + integrated terminal
- Approval prompts appear as WebSocket-pushed overlays
- Agent operation panel in sidebar

**CLI pipe mode (power users):**

```bash
# Pipe a remote workspace terminal to your local terminal
zt connect <workspace-id>

# One-command AI agent workflow
zt agent start --repo WintDev/Core --task "Fix the missing icon on ReceiptVerificationTodoHandler"
```

- Thin WebSocket client — sends keystrokes, receives terminal output
- No code execution on local machine
- Approval prompts appear inline in the piped terminal
- Session can be detached/reattached (tmux on remote)
- Works with any local terminal emulator (iTerm2, Wezterm, Alacritty)

### 2. Workspace Orchestrator

Manages ephemeral compute environments using Devcontainer CLI:

- **Production:** Firecracker microVMs (VM-level isolation, 125ms startup)
- **Local dev:** Docker + gVisor (weaker isolation, works on a laptop)
- **Auto-suspend** after 15 min idle (resume from snapshot in <3s)
- **Auto-destroy** after configurable TTL (default 24hr)
- **Devcontainer spec** compatible — teams reuse existing `.devcontainer/devcontainer.json`

### 3. ZT Proxy Sidecar (per workspace)

All workspace egress routes through Envoy → Go ext_authz service:

```
Workspace process → Envoy (routing + TLS) → Go ext_authz → target service
                                              │
                                         Tier classify
                                         Secret swap
                                         Audit write
                                         Approval gate (if Tier 3)
```

Overhead: ~1-2ms per external request. Internal workspace traffic (filesystem, language server, build tools) does not go through the proxy.

### 4. Operation Tier System

| Tier            | Action                     | Examples                                                       |
| --------------- | -------------------------- | -------------------------------------------------------------- |
| 1 - Read        | Allow + log                | SELECT, HTTP GET, SHOW, EXPLAIN                                |
| 2 - Safe Write  | Allow + notify + log       | INSERT, HTTP POST (internal)                                   |
| 3 - Destructive | **Require approval** + log | UPDATE, DELETE, DROP TABLE, HTTP DELETE, bulk reads (>1K rows) |
| 4 - Forbidden   | **Always block** + log     | DROP DATABASE, TRUNCATE, GRANT/REVOKE                          |

Additional:

- Unbounded `SELECT *` without WHERE or LIMIT → Tier 3
- Custom overrides per project in `.cordon.yaml`
- Unknown operations default to Tier 3 (safe default)

### 5. Secret Vault

- Encrypted at rest using SOPS (or HashiCorp Vault for enterprise)
- **Never leaves the server** — Go ext_authz service injects credentials into Envoy requests
- Secrets are never transmitted to the workspace, browser, or developer's machine
- Every secret use is logged with caller identity and operation context

### 6. Audit Store

- PostgreSQL with append-only table (no UPDATE/DELETE permissions on audit rows)
- Every operation recorded: timestamp, tier, operation, target, caller, decision, duration
- Queryable in real-time via dashboard and API
- Retention policy configurable (default: 90 days)

### 7. Approval Queue

- Tier 3 operations pause execution and push prompts via WebSocket
- Three approval modes:
  - **Allow** — one-time approval
  - **Allow for session** — approve this operation type for the current workspace session
  - **Pre-approve pattern** — "allow all SELECT on this table for 30 minutes"
- Auto-deny after configurable timeout (default: 5 min)
- Supports delegated approval (team lead / security reviewer)

## Project Manifest (`.cordon.yaml`)

```yaml
version: "1"
name: "my-project"

secrets:
  - name: DATABASE_URL
    placeholder: "postgresql://placeholder:placeholder@proxy:5432/mydb"
  - name: API_KEY
    placeholder: "zt-placeholder-api-key"
  - name: NUGET_FEED_TOKEN
    placeholder: "zt-placeholder-nuget-token"

services:
  - name: database
    type: postgres
    target: "staging-db.example.com:5432"
    tier_overrides:
      - pattern: "DELETE FROM audit_log"
        tier: 4

  - name: internal-api
    type: http
    target: "https://api.internal.example.com"
    allowed_methods: ["GET", "POST"]

egress:
  allowlist:
    - "*.example.com"
    - "registry.npmjs.org"
    - "nuget.org"
    - "github.com"
    - "api.anthropic.com"

workspace:
  devcontainer: ".devcontainer/devcontainer.json"
  resources:
    cpu: 2
    memory: "4Gi"
  idle_timeout: "15m"
  max_lifetime: "24h"
  tools:
    - claude-code

approval:
  tier3_timeout_seconds: 300
  allow_session_grants: true
  allow_pattern_grants: true
```

## DX Principles

1. **Zero-friction for reads** — Tier 1 operations are invisible, no prompts, no delays
2. **Nothing to install for browser users** — open URL, SSO, start working
3. **Pipe to local terminal for power users** — `zt connect` gives you your own terminal emulator
4. **Sub-second workspace resume** — snapshot-based, feels instant
5. **Standard devcontainer spec** — teams reuse existing `.devcontainer/` configs
6. **Agent transparency** — real-time panel showing every agent operation, with pause/resume
7. **"Why was this blocked?"** — every denial links to the specific policy rule
8. **Zero-config onboarding** — SSO → auto-discover team policies → guided walkthrough
9. **One-command agent workflow** — `zt agent start --repo X --task "description"` does everything

## Security Properties

1. **No code runs on the developer's machine** — browser or thin CLI pipe is the only client
2. **Secrets never leave the server** — ext_authz service injects credentials at the proxy level
3. **Ephemeral workspaces** — destroyed after use, no persistent compromise possible
4. **VM-level isolation in production** — Firecracker microVMs, not shared-kernel containers
5. **All egress filtered** — Envoy proxy enforces allowlist at the workspace network level
6. **Every operation classified, logged, and auditable** — append-only PostgreSQL audit store
7. **Destructive operations gated** — human approval required for Tier 3
8. **Forbidden operations always blocked** — Tier 4 cannot be overridden by anyone
9. **Architecture tests enforce security invariants** — auth, tier classification, secret isolation verified at test time

## Deployment Modes

| Mode       | Runtime              | Isolation       | Use Case                                             |
| ---------- | -------------------- | --------------- | ---------------------------------------------------- |
| Local dev  | Docker + gVisor      | Container-level | Developing Cordon itself, small team testing       |
| Production | Firecracker microVMs | VM-level        | Enterprise deployment, multi-tenant                  |
| Hybrid     | Mix                  | Per-workspace   | Dev workspaces in Docker, prod-access in Firecracker |

```bash
# Local dev mode
cordon serve --mode local --port 8443

# Production
cordon serve --mode production --config /etc/cordon/config.yaml
```

## Competitive Landscape

The market is split in two halves that don't talk to each other:

| Category           | Products                                | What they do             | What they lack                                     |
| ------------------ | --------------------------------------- | ------------------------ | -------------------------------------------------- |
| **CDEs**           | Coder, E2B, Daytona, Codespaces, Gitpod | Sandboxed code execution | No credential brokering, no operation gating       |
| **Access proxies** | StrongDM, Teleport, Boundary, hoop.dev  | Gate database/API access | No development environment, no AI agent visibility |

**Cordon sits in the seam** — unified sandbox + access proxy with tiered operation classification. Closest competitor: hoop.dev (no sandbox, no tier system, no agent visibility).

### Why Not Coder?

- AGPL-3.0 license — cannot build commercial product on top
- No UI extension points — monolithic React, no plugin system
- Full evaluation: `CODER_EVALUATION.md`

## Cost Model (Production)

| Resource              | Per workspace | 50 devs | 200 devs  |
| --------------------- | ------------- | ------- | --------- |
| Active (2 vCPU, 4GB)  | ~$0.10/hr     | $40/day | $160/day  |
| Suspended (disk only) | ~$0.01/hr     | $4/day  | $16/day   |
| Storage (snapshots)   | ~$5/mo/dev    | $250/mo | $1,000/mo |

## Testing Strategy

### Automated Tests

Three layers, matching the clean architecture:

| Layer                  | What it tests                                                                                                       | Tools                              |
| ---------------------- | ------------------------------------------------------------------------------------------------------------------- | ---------------------------------- |
| **Architecture tests** | Security invariants (auth on all endpoints, tier classifier on all proxy routes, secret isolation, cascade cleanup) | Go test + custom constraint checks |
| **Unit tests**         | Domain logic (tier classification, secret swap, approval decisions) in isolation                                    | Go test                            |
| **Integration tests**  | Full proxy flow (request → classify → approve → audit), workspace lifecycle                                         | Go test + testcontainers           |

### Acceptance Testing (E2E)

Defined in `ACCEPTANCE-TESTS.md` — 12 real developer workflow scenarios that must pass before any release. These are the ground truth for "does Cordon actually work for developers?"

Key scenarios:

- AI agent fix → build → test → PR (the core workflow, modeled on a real PR)
- Developer pipes remote session to local terminal
- Database investigation via MCP with credential swap
- Destructive operation approval flow
- Forbidden operation always blocked
- Egress blocking prevents data exfiltration
- Workspace lifecycle (start, suspend, resume, destroy)

Plus 10 security invariants that must **never** be violated.

### Visual/UI Testing with `/ui-audit`

The `/ui-audit` skill is used to verify the web UI (dashboard, approval overlays, audit timeline, agent panel) works correctly across scenarios.

**Critical rule: the audit log is append-only.**

The UI audit produces a persistent test log file (`UI-AUDIT-LOG.md`) that documents the full testing process as a chronological narrative:

1. **Test run** — what was tested, what was found (screenshots, descriptions)
2. **Bugs found** — what broke, with visual evidence
3. **Fixes applied** — what code was changed to fix each bug
4. **Re-test** — verification that the fix worked, with new screenshots

This file is **never deleted or overwritten** — each audit session appends to it. It reads as a complete history of UI quality over time: bugs discovered, how they were fixed, and confirmation they stayed fixed.

```markdown
# UI Audit Log

## 2026-04-03 — Initial audit

### Test: Approval overlay appears on Tier 3 operation

- Status: FAIL
- Bug: Overlay renders behind the terminal panel (z-index issue)
- Screenshot: [link]

### Fix: Set z-index: 1000 on approval overlay

- Commit: abc123
- Files changed: web/src/lib/security/ApprovalOverlay.svelte

### Re-test: Approval overlay appears on Tier 3 operation

- Status: PASS
- Screenshot: [link]

## 2026-04-05 — Post-refactor audit

...
```

---

## Decisions Made

| #   | Question                         | Decision                                 | Rationale                                                                                                                                                                            |
| --- | -------------------------------- | ---------------------------------------- | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------ |
| 1   | Package manager sandboxing       | **Egress allowlist sufficient for v1**   | Workspace is ephemeral + egress-filtered. Vetted package proxy is v2.                                                                                                                |
| 2   | Git hooks                        | **Contained in workspace, low risk**     | Hooks run inside ephemeral VM with egress filtering. Recommend server-side hooks in docs. Revisit if attack patterns emerge.                                                         |
| 3   | CI/CD gating                     | **v2**                                   | Focus on workspace security first. CI/CD workflow file gating later.                                                                                                                 |
| 4   | Clipboard                        | **Audit-only in v1**                     | Log clipboard transfers but don't restrict. Restricting kills DX. Size limits + text-only in v2.                                                                                     |
| 5   | Bulk read thresholds             | **1K default, configurable per service** | `SELECT *` without WHERE/LIMIT → Tier 3. Threshold configurable in `.cordon.yaml` per service.                                                                                   |
| 6   | Offline development              | **Not supported in v1**                  | Remote-first is the security promise. No degraded local mode — it creates confusion about what's protected.                                                                          |
| 7   | VS Code Remote SSH               | **Supported as opt-in**                  | Exposes SSH endpoint per workspace. Developer keeps their full local VS Code + extensions. Documented tradeoff: extensions run locally with host access. Teams can disable.          |
| 8   | Multi-tenant isolation           | **Self-hosted per company in v1**        | V1 is self-hosted — each company runs their own instance. Tenant ID on every workspace from day one so centralized multi-tenant hosting is an infra change, not a data model change. |
| 9   | Agent approval delegation        | **v2 roadmap**                           | No unattended agent approval in v1. All Tier 3 ops require a human. Trusted agent profiles added later.                                                                              |
| 10  | OpenVSCode Server vs code-server | **code-server (Coder's)**                | More production deployments (30K+), both MIT. Can swap later — both serve VS Code over HTTP.                                                                                         |

## VS Code Remote SSH (Opt-In)

When enabled, each workspace exposes an SSH endpoint. Developers connect with their local VS Code via Remote SSH extension, keeping their full extension setup and keybindings.

**How it works:**

- Workspace orchestrator generates a short-lived SSH key pair per session
- SSH endpoint is accessible via `zt connect --ssh <workspace-id>` (configures local SSH config)
- All code execution still happens in the workspace (remote)
- Extensions run locally (this is the tradeoff)
- Terminal commands run remotely through the SSH tunnel
- All egress still routes through the proxy sidecar

**Security tradeoff (documented to users):**

- Extensions run on the developer's machine with local filesystem access
- A compromised extension could read local files (but not workspace secrets — those are server-side)
- Teams that prioritize security over convenience can disable SSH access via policy

**Configuration:**

```yaml
# In team policy or .cordon.yaml
workspace:
  allow_ssh: true # default: true. Set false to enforce browser-only.
```

## v2 Roadmap

Features deferred from v1 that are on the roadmap:

### Security Hardening

- **Vetted package proxy** — strip postinstall scripts from npm/pip packages, allow only pre-approved packages
- **CI/CD workflow gating** — Tier 3 approval for changes to GitHub Actions, Dockerfiles, IaC
- **Clipboard restrictions** — text-only, size limits, audit all transfers
- **Git hook policy** — option to strip client-side hooks and enforce server-side only, if attack patterns emerge targeting `.husky/` or `.pre-commit-config.yaml`

### Agent Autonomy

- **Trusted agent profiles** — admin pre-approves specific operation patterns per agent (e.g., "Claude can SELECT on staging tables without human approval")
- **Unattended agent workflows** — agents run tasks overnight with pre-approved operation budgets
- **Agent reputation scoring** — track agent behavior over time, auto-escalate anomalous patterns

### Hosted Multi-Tenant Service

- **Centralized hosting** — Cordon as a managed service where organizations run dev sandboxes on our infrastructure instead of self-hosting
- **Network-level tenant isolation** — VLAN/VPC segmentation between organizations on shared infrastructure
- **Per-org billing** — usage-based pricing per workspace-hour
- **Org onboarding** — self-service sign-up, connect git provider, configure policies, start working

### Enterprise

- **SAML/SCIM** — enterprise identity provider integration
- **Custom compliance policies** — SOC 2, HIPAA-specific operation rules
- **Audit export** — compliance reporting, SIEM integration
- **Multi-region deployment** — workspaces close to the developer for latency

### DX Improvements

- **Workspace templates marketplace** — pre-configured workspaces for common stacks
- **Persistent workspace option** — for long-running projects that can't be ephemeral
- **IDE plugin ecosystem** — JetBrains, Neovim gateway support alongside VS Code
