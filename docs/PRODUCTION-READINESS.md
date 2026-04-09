# Production Readiness Plan

Action plan to take Cordon from working MVP to production-ready, organized into phases with explicit dependencies. Each phase builds on the previous one — the order matters.

Reference: [Security Model](../README.md#security-model--defense-in-depth) defines the 7 defense layers. [OPEN-ISSUES.md](./OPEN-ISSUES.md) tracks deferred decisions. [SPEC.md](./spec/SPEC.md) describes the target architecture.

---

## Phase 0: Foundation ✅

Structural changes that unblock everything else. No new features — just moving from "it works" to "it's maintainable."

### 0.1 Centralized Configuration ✅

**Problem:** 14+ timeout/limit values hardcoded and duplicated across `cmd/server/main.go`, handlers, helpers. Changing a timeout requires a code change, recompile, redeploy.

**Action:**

- Define a `Config` struct in `internal/infrastructure/config/` with all server settings (timeouts, resource limits, egress allowlist, auth mode, feature flags)
- Load from environment variables with defaults, validate at startup
- Pass `Config` (or relevant sub-structs) through DI — no package-level state
- Remove all `envOr()` calls from `cmd/server/main.go` in favor of config struct fields
- Fail fast at startup if required config is missing or invalid

**Validates:** `make test` passes. All previously hardcoded values are configurable via env vars.

### 0.2 Structured Logging ✅

**Problem:** 100+ `log.Printf` calls with inconsistent prefixes. No request IDs, no tenant context, no level filtering. Can't correlate a workspace creation failure with the API request that triggered it.

**Action:**

- Adopt `log/slog` (stdlib, zero dependencies)
- Define a `Logger` interface in `application/ports/` if needed, or pass `*slog.Logger` directly (pragmatic for stdlib)
- Add middleware that injects request ID + tenant ID into the `slog` context
- Replace `log.Printf` calls incrementally, starting with security-critical paths (proxy pipeline, auth, secret swap)
- Structured JSON output for production, text output for development

**Validates:** `grep -r 'log.Printf' internal/` count decreases. Proxy pipeline logs include request ID and tenant ID.

### 0.3 Persistent Workspace Store

**Problem:** Workspace state lives in Docker container labels. Lost if container removed outside Cordon. Blocks multi-backend support (Azure, Firecracker have different metadata models).

**Action:**

- Add `workspaces` table in PostgreSQL (migration)
- Implement `WorkspaceStore` interface: `Create`, `Get`, `List`, `UpdateStatus`, `Delete`
- Write to DB on create, update on status changes, soft-delete on destroy
- Docker container state becomes a secondary signal for health/running status, not the source of truth
- Migrate `MemoryRegistry` (IP-to-workspace mapping) into the workspace store

**Validates:** `make test-integration` passes. Workspace survives `docker rm` of its container (shows as "lost" in UI, not silently gone).

**Dependency:** None. Can start immediately.

---

## Phase 1: Security Hardening ✅

Make the 7 defense layers production-grade. Each item hardens one or more layers from the security model.

### 1.1 Reliable Audit Trail (Layer 6) ✅ (partial)

**Problem:** `writeAudit()` silently drops errors. Security-critical events can be lost under load or during DB issues.

**Action:**

- Make audit write **synchronous and blocking** by default — if the audit store is down, the operation fails (fail-closed)
- Add a local WAL (write-ahead log) as fallback: if PostgreSQL is unreachable, buffer entries to disk and flush when connection recovers
- Add audit entries for workspace lifecycle events (create, destroy, suspend, resume) — currently only in stdout
- Add audit entries for approval decisions (who approved what, when, with what scope)
- Add response metadata to audit entries: HTTP status code, row count for SQL results, response body size

**Validates:** Kill PostgreSQL during a proxy request — request returns 503, not 200 with silent audit loss. Restart PostgreSQL — buffered entries appear in audit log.

**Dependency:** 0.2 (structured logging for the WAL)

### 1.2 Real Vault Backend (Layer 5) ✅ (AES-256-GCM encrypted PostgreSQL vault)

**Problem:** `MemoryVault` loaded from env vars. Secrets in plaintext in server process memory. No rotation, no encryption at rest.

**Action:**

- Implement `SecretVault` interface backed by Azure Key Vault (primary) or HashiCorp Vault (alternative)
- Server authenticates to vault via Managed Identity (Azure) or AppRole (HashiCorp) — no secrets in env vars
- Cache fetched secrets in memory with TTL (configurable, default 5 minutes) for performance
- Support secret rotation: vault returns new value, proxy uses it on next request without restart
- Keep `MemoryVault` for development/testing only, gated behind `AUTH_MODE=static`

**Validates:** Server starts with no secret-related env vars. Secrets fetched from vault at runtime. Rotate a secret in vault — next proxy request uses new value within cache TTL.

**Dependency:** 0.1 (vault config in centralized config struct)

### 1.3 Authentication (Layer: Tenant Isolation) ✅ (JWT with BYOIDP)

**Problem:** No real auth. Static tenant ID. Anyone reaching port 8443 is the default tenant.

**Action:**

- Implement OIDC authentication with Entra ID (Azure AD) as primary provider
- `AuthValidator` interface already exists — add `OIDCValidator` implementation
- Short-lived tokens (1h), refresh via standard OIDC flow
- Map OIDC subject to tenant ID + user ID
- User settings stored in PostgreSQL (needed for AI config parity, git identity)
- Device-bound tokens for CLI (`cordon login` flow with device code grant)

**Validates:** Unauthenticated request to any API endpoint returns 401. Two different users see isolated workspaces and audit trails.

**Dependency:** 0.3 (user settings need persistent store)

### 1.4 Container Hardening (Layer 7) ✅

**Problem:** Containers run with default Docker capabilities. No seccomp profile, no AppArmor. Default caps include `CAP_NET_RAW` (packet crafting) and `CAP_SYS_PTRACE` (debugging other processes).

**Action:**

- Drop all capabilities except the minimum needed: `CAP_CHOWN`, `CAP_DAC_OVERRIDE`, `CAP_FOWNER`, `CAP_SETGID`, `CAP_SETUID`, `CAP_NET_BIND_SERVICE`
- Apply a seccomp profile that blocks dangerous syscalls (`mount`, `reboot`, `kexec_load`, `ptrace`, `keyctl`)
- Set `no-new-privileges` security option
- Consider read-only root filesystem with writable `/tmp` and `/workspace` tmpfs (evaluate impact on devcontainer builds first)
- Add to `ComputeBackend.CreateContainer` options — each backend applies hardening with its native mechanism

**Validates:** From inside a container, `capsh --print` shows reduced capabilities. `strace` fails with EPERM. `mount` fails.

**Dependency:** None. Can be done in parallel with other Phase 1 work.

### 1.5 Persistent Approval Grants (Layer 4) ✅

**Problem:** Session/pattern approval grants stored in memory, lost on restart.

**Action:**

- Add `approval_grants` table in PostgreSQL
- Persist grants on approval decision, with TTL and scope
- Clean up expired grants via background goroutine (or PostgreSQL `AFTER` trigger)
- Load active grants on startup

**Validates:** Approve a T3 operation with session scope. Restart server. Same operation auto-approved without re-prompting.

**Dependency:** 0.3 (uses same persistent store patterns)

---

## Phase 2: Provider Abstraction ✅

Decouple the application layer from Docker so that Azure Container Instances, Firecracker, or Kubernetes can be added as backends without touching business logic.

### 2.1 Clean Application/Infrastructure Boundary ✅

**Problem:** Application layer (`workspace/`) reaches through to Docker-specific behaviors: label access, `hostname -I` exec, Docker binary stream header parsing, network naming conventions.

**Action:**

- Remove all Docker label reads from `persistent_service.go` — read from workspace store (Phase 0.3) instead
- Move `registerWorkspaceIP()` into the `ComputeBackend` interface as `ContainerIP(ctx, containerID) (string, error)` — each backend implements IP discovery with its native mechanism
- Move network naming out of the application layer — backends create networks with their own naming; the orchestrator only knows the workspace ID
- Audit all `application/` files for Docker-specific imports or assumptions

**Validates:** `archtest/` passes. `grep -r 'docker\|Docker\|label' internal/application/` returns zero hits (excluding comments).

**Dependency:** 0.3 (workspace store replaces label reads)

### 2.2 Backend-Agnostic Egress Enforcement ✅

**Problem:** Egress enforcement uses Docker-specific haproxy gateway containers. Azure and Firecracker need completely different isolation mechanisms.

**Action:**

- `EnsureProxyAccess()` already exists on the `ComputeBackend` interface — ensure its contract is well-documented
- Docker backend: current haproxy gateway approach (working, keep as-is)
- Azure backend (future): NSG rules that restrict outbound to Cordon proxy IP only
- Firecracker backend (future): Network namespace restrictions via jailer
- Move egress allowlist into `Config` (Phase 0.1) so it's not hardcoded

**Validates:** Egress allowlist configurable via env var or config file. Adding a new allowed host doesn't require code change.

**Dependency:** 0.1 (config), 2.1 (clean boundaries)

### 2.3 Per-Workspace Egress Policies (Layer 2) ✅

**Problem:** Egress allowlist is global. Can't give workspace A access to different hosts than workspace B.

**Action:**

- Add `allowed_hosts` field to workspace configuration (API + `.cordon.yaml`)
- Egress checker accepts per-workspace allowlist, merged with global defaults
- Store workspace-level policies in workspace store
- UI: show allowed hosts on workspace detail page

**Validates:** Create two workspaces with different allowlists. Workspace A can reach host X but not Y. Workspace B can reach Y but not X.

**Dependency:** 0.3 (workspace store), 2.2 (configurable allowlist)

---

## Phase 3: Advanced Security

Hardening beyond the basics. These items address the more sophisticated attack vectors in the threat model.

### 3.1 SQL Parser Upgrade (Layer 3)

**Problem:** Keyword-based classification can't detect unbounded result sets, SQL injection, or data exfiltration via benign-looking queries.

**Action:**

- Replace keyword matching with a real SQL parser (e.g., `vitess/sqlparser` or `pganalyze/pg_query_go`)
- Parse query structure: detect SELECTs without WHERE/LIMIT on known-large tables
- Enforce result set row limits (configurable per-workspace or per-database)
- Detect multi-statement queries (statement stacking attacks)
- Reject queries that can't be parsed (fail-closed for unknown SQL)

**Validates:** `SELECT * FROM users` → T3 (unbounded). `SELECT * FROM users LIMIT 999999` → T3 (excessive limit). `SELECT 1; DROP TABLE users` → T4 (multi-statement).

**Dependency:** 0.1 (row limits in config)

### 3.2 MCP Proxy (Layer 3)

**Problem:** MCP tool calls bypass the entire zero-trust pipeline. AI agents can access databases and APIs through MCP without classification, approval, or audit.

**Action:**

- Implement MCP proxy mode: Cordon acts as an MCP server that wraps real MCP servers
- Agent's MCP config points at Cordon, not the real servers
- Cordon intercepts MCP tool calls → classifies the underlying operation → swaps credentials → forwards to real MCP server → audits → returns result
- Reuse existing proxy pipeline for classification and approval (MCP calls decompose into SQL/HTTP operations)

**Validates:** Agent calls MCP tool that runs `DELETE FROM users WHERE id = 1`. Cordon classifies as T3, requests approval, audits the decision. Agent never has direct MCP server access.

**Dependency:** Phase 1 complete (audit reliability, auth)

### 3.3 DNS Filtering (Layer 1)

**Problem:** Workspace containers resolve DNS through the Docker daemon. Can resolve blocked domains (even though network access is denied). May expose internal DNS names.

**Action:**

- Run a DNS proxy (e.g., CoreDNS) on the workspace's internal network
- Workspace DNS configured to use the proxy (injected via Docker DNS settings)
- DNS proxy only resolves domains on the egress allowlist
- Log all DNS queries to audit trail

**Validates:** From workspace, `dig evil.com` returns NXDOMAIN. `dig github.com` resolves normally.

**Dependency:** 2.2 (egress allowlist in config)

### 3.4 PII Masking in Query Results (Layer 3)

**Problem:** Even allowed queries may return PII the agent doesn't need (emails, SSNs, financial data).

**Action:**

- Define column-level masking rules in `.cordon.yaml` per database/table
- Proxy inspects SQL query result columns, applies masking before returning to agent
- Masking strategies: redact, hash, partial mask (e.g., `j***@example.com`)
- Unmask requires T3 approval

**Validates:** Agent runs `SELECT email, name FROM users LIMIT 5`. Result shows `j***@e***.com` for email column. Agent requests unmask → T3 approval flow.

**Dependency:** 3.1 (SQL parser for column identification)

---

## Phase 4: Operational Readiness

Infrastructure, observability, and operational tooling needed to run Cordon reliably in production.

### 4.1 Health Checks & Metrics

**Action:**

- `/healthz` endpoint: checks PostgreSQL connectivity, vault reachability, Docker daemon (or compute backend) status
- `/readyz` endpoint: same + "has loaded config and secrets successfully"
- Prometheus metrics: request latency (by tier), audit write latency, approval wait time, active workspaces, container creation duration
- Alert on: audit write failures, vault unreachable, approval timeout rate > threshold

### 4.2 Idle Timeout Enforcement

**Action:**

- Background goroutine checks last activity timestamp per workspace
- Suspend (pause container) after idle timeout (default 15m, configurable)
- Notify via WebSocket before suspending (30s warning)
- Auto-destroy after max lifetime (default 8h, configurable)
- Resume on next API/terminal interaction

### 4.3 Image Vulnerability Scanning

**Action:**

- Integrate Trivy (or similar) to scan workspace images before first use
- Block creation if critical CVEs found (configurable severity threshold)
- Cache scan results by image digest

### 4.4 Backup & Disaster Recovery

**Action:**

- PostgreSQL backup strategy for audit data (append-only, can be large)
- Retention policy for audit entries (configurable, default 90 days)
- Export capability for compliance (CSV/JSON dump of audit entries by date range)

---

## Dependency Graph

```
Phase 0 (Foundation)
├── 0.1 Centralized Config ─────────────────────┐
├── 0.2 Structured Logging ──────────┐           │
└── 0.3 Persistent Workspace Store ──┤           │
                                     │           │
Phase 1 (Security Hardening)         │           │
├── 1.1 Reliable Audit ◄────────────┘           │
├── 1.2 Real Vault ◄────────────────────────────┘
├── 1.3 Authentication ◄── 0.3
├── 1.4 Container Hardening (independent)
└── 1.5 Persistent Approvals ◄── 0.3

Phase 2 (Provider Abstraction)
├── 2.1 Clean Boundaries ◄── 0.3
├── 2.2 Backend-Agnostic Egress ◄── 0.1, 2.1
└── 2.3 Per-Workspace Egress ◄── 0.3, 2.2

Phase 3 (Advanced Security)
├── 3.1 SQL Parser ◄── 0.1
├── 3.2 MCP Proxy ◄── Phase 1 complete
├── 3.3 DNS Filtering ◄── 2.2
└── 3.4 PII Masking ◄── 3.1

Phase 4 (Operational)
├── 4.1 Health & Metrics (independent)
├── 4.2 Idle Timeout ◄── 0.3
├── 4.3 Image Scanning (independent)
└── 4.4 Backup & DR ◄── 1.1
```

## What Can Run in Parallel

Within each phase, independent items can be developed concurrently:

- **Phase 0:** 0.1 and 0.3 are independent. 0.2 is independent.
- **Phase 1:** 1.4 (container hardening) is fully independent. 1.3 and 1.5 both need 0.3 but not each other. 1.1 needs 0.2. 1.2 needs 0.1.
- **Phase 2:** All items are sequential (each builds on the previous).
- **Phase 3:** 3.1 is independent of 3.2 and 3.3.
- **Phase 4:** 4.1 and 4.3 can happen anytime. 4.2 needs 0.3.

## Decision Points

These require a call before implementation:

1. **Vault provider:** Azure Key Vault vs. HashiCorp Vault vs. both? Drives 1.2 scope.
2. **SQL parser library:** `vitess/sqlparser` (MySQL dialect) vs. `pganalyze/pg_query_go` (PostgreSQL-native). Drives 3.1.
3. **Second compute backend:** Azure Container Instances vs. Firecracker vs. Kubernetes? Drives Phase 2 priorities.
4. **MCP proxy scope:** Wrap all MCP servers generically, or start with database-only? Drives 3.2.
5. **Audit retention:** How long to keep audit data? Compliance requirements may dictate. Drives 4.4.
