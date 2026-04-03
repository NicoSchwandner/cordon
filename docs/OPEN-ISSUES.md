# Open Issues & Deferred Decisions

Documented design decisions that are deferred past the current MVP. Each item includes context on why it was deferred and what the expected approach is.

---

## Near-Term Improvements

### Provider Test Coverage

**Status:** Needed
**Impact:** No tests for `createFromRepo`, clone fallback, git identity setup

The devcontainer provisioning flow (clone, fallback branch creation, git credential/identity setup) has no unit or integration tests. The devcontainer config parser has tests, but the Docker provider's new logic is untested.

**Planned approach:** Integration tests using testcontainers. Mock the devcontainer CLI build step, test the clone/fallback/git-config logic against a real Docker daemon.

### Creation Progress Feedback

**Status:** Needed
**Impact:** UX — the POST blocks for 1-4 minutes with zero feedback

Creating a workspace from a repo is synchronous. The frontend shows "Creating..." but has no visibility into what's happening (cloning, building image, running postCreateCommand). Users don't know if it's stuck or working.

**Planned approach:** Stream build logs to the frontend during creation. Options: SSE endpoint, or WebSocket that streams logs while the POST runs in the background. See also "Async Workspace Creation with Progress" below for the full async solution.

### Image Caching

**Status:** Needed
**Impact:** Repeated creates from the same repo rebuild the devcontainer image every time

Docker layer caching helps, but the full `devcontainer build` cycle still runs (clone to tmpdir, invoke CLI, etc.) even when nothing changed. First build is ~1-4 min, subsequent builds ~15-30s due to layer cache, but this could be near-instant.

**Planned approach:** Cache built images by `repo:branch:devcontainer-hash`. Before building, check if a cached image exists and the devcontainer.json hasn't changed. Skip the build entirely if cache is valid. Invalidation: on push to branch (webhook) or TTL-based.

### Default Branch Detection

**Status:** Needed
**Impact:** Base branch defaults to "development" which is Wint-specific

When base branch is left empty, the system defaults to "development". Other companies use "main", "master", or custom default branches.

**Planned approach:** Query GitHub API (`GET /repos/{owner}/{repo}`) to read the `default_branch` field. Cache per-repo. Fall back to "main" if the API call fails.

### AI Agent Configuration Parity

**Status:** Needed
**Impact:** Developers using Claude Code in workspaces don't get their local AI configuration

Developers have local Claude Code setups — skills, rules, instructions, hooks, CLAUDE.md files — that define how their AI assistant behaves. When they create a Cordon workspace, none of this configuration carries over. The remote Claude instance inside the workspace is a blank slate.

**Planned approach:** Multi-layered config injection at workspace creation:

1. **Repo-level config** — the repo's committed `.claude/` folder (CLAUDE.md, rules, settings) is already present after clone. This works today.
2. **Org-level config** — shared rules/instructions from a central repo (e.g., `Wint.AI.Rules`). Could be cloned as a sidecar or mounted into the workspace, with a sync step in postCreateCommand.
3. **User-level config** — personal preferences, API tokens, custom skills. Stored in Cordon's user settings (future auth system) and injected into `~/.claude/` inside the container at creation time.
4. **Secrets** — API tokens for Claude, MCP servers, etc. Injected via the existing secret vault / proxy mechanism. Real tokens never stored in the workspace.

The goal: a developer creates a workspace and Claude Code works identically to their local setup — same rules, same skills, same behavior. The workspace should feel like "my machine, but ephemeral."

---

## Deferred (Post-MVP)

## Async Workspace Creation with Progress

**Status:** Deferred
**Impact:** UX — workspace creation from repo can take 2-5 minutes

Currently, `POST /api/workspaces` is synchronous. The frontend blocks with a spinner. This is acceptable for single-user dev, but won't scale.

**Planned approach:** Background goroutine + status polling or SSE. The workspace would be created with `status: creating`, and the frontend polls `GET /api/workspaces/{id}` until it transitions to `running`. Build logs streamed via SSE endpoint.

## Docker Compose Sidecars (SQL Server, etc.)

**Status:** Deferred
**Impact:** Database-dependent tests can't run without manual setup

Many repos need a database. The devcontainer spec supports `dockerComposeFile` for multi-container setups, but Cordon doesn't interpret it yet.

**Planned approach:** When devcontainer.json contains `dockerComposeFile`, Cordon starts the compose services on the workspace's network. For repos without compose, offer a "sidecar catalog" (e.g., SQL Server, Postgres, Redis) that can be added at workspace creation time.

## Pre-Warm Pool

**Status:** Deferred
**Impact:** Cold-start time (image build + clone + restore) on every workspace create

First workspace from a repo is slow. Subsequent ones could be near-instant with pre-built images.

**Planned approach:** Background pool of pre-provisioned containers per repo+branch. When a developer requests a workspace, assign one from the pool and replenish. Pool size configurable per repo. Image cache (see below) is a prerequisite.

## Workspace Groups (Multi-Repo)

**Status:** Deferred
**Impact:** Developers working across microservices need multiple workspaces

In a microservice architecture, a single feature may require changes in 2+ repos (e.g., backend API + frontend). Each repo has its own devcontainer.

**Planned approach:** A "workspace group" is a lightweight grouping that creates multiple workspaces on a shared Docker network from a single action. Each workspace is independently manageable (suspend, destroy), but the group provides a unified view. The frontend shows grouped workspaces together. Each workspace gets one repo — no multi-repo containers.

This follows the GitHub Codespaces model: one Codespace per repo.

## Image Caching

**Status:** Deferred
**Impact:** Repeated `devcontainer build` for the same repo+branch is wasteful

Currently, every workspace creation runs `devcontainer build` from scratch. The devcontainer CLI has some Docker layer caching, but the clone + build cycle still takes time.

**Planned approach:** Cache built images by `repo:branch:devcontainer-hash`. On workspace create, check if a cached image exists and is still valid (devcontainer.json hasn't changed). If valid, skip the build. Invalidation: webhook on push to branch, or TTL-based.

## MCP Proxy Support

**Status:** Deferred
**Impact:** Critical — MCP tools (database access, etc.) don't go through the zero-trust pipeline

MCP tools are the primary way AI agents interact with databases and external services. Currently, MCP calls bypass Cordon entirely — there's no interception, classification, or credential swapping for MCP tool invocations.

**Planned approach:** An MCP proxy mode where Cordon acts as an MCP server that wraps real MCP servers. Agent calls MCP tool → Cordon intercepts → classifies the underlying operation (SQL tier, HTTP tier) → swaps credentials → forwards to actual MCP server → audits → returns result. The agent's MCP config points at Cordon, not the real servers.

This is the linchpin for database investigation workflows — developers keep the same DX (MCP tools work identically) while every query goes through tier classification, approval gates, and audit logging.

## HTTP Proxy Forwarding

**Status:** Deferred
**Impact:** Security — proxy classifies requests but doesn't make the actual outbound call

The current HTTP proxy classifies, swaps secrets, and returns the modified request — but the agent must make the actual outbound HTTP call itself. This means the workspace still needs direct network access to target services, undermining the zero-trust boundary.

**Planned approach:** The proxy should be a true forward proxy: receive the request, classify it, swap secrets, make the outbound call on behalf of the agent, and return the response. The workspace has no direct internet access — all HTTP goes through the proxy.

## Network-Level Egress Enforcement

**Status:** Deferred
**Impact:** Security — egress allowlist is app-level only, bypassable

The current egress control is enforced at the application level (Go proxy checks an allowlist). A compromised agent that bypasses the proxy (e.g., raw socket, curl to a different port) can reach any host.

**Planned approach:** iptables/nftables rules on the workspace container's network that only allow traffic to the Cordon proxy. All outbound connections from the workspace must go through the proxy — enforced at the network layer, not just the application layer. Options: custom Docker network with iptables rules, or Envoy sidecar with strict egress policy.

## Real Authentication (OIDC / Entra ID)

**Status:** Deferred
**Impact:** Security — anyone who can reach port 8443 can use the system

Currently single-tenant with a static tenant ID. No user authentication, no identity provider integration.

**Planned approach:** OIDC integration with Entra ID (Azure AD) as the primary provider. Device-bound tokens, short TTLs, Conditional Access policies. Each developer gets their own tenant context. Supports the user settings system needed for AI config parity and git identity preferences.

## Vault Integration (Azure Key Vault / Managed Identity)

**Status:** Deferred
**Impact:** Security — secrets stored in env vars on the Cordon server

The secret vault is currently in-memory, loaded from environment variables. Secrets exist in plaintext on the server's process.

**Planned approach:** Integration with Azure Key Vault using Managed Identity. The Cordon server fetches secrets from Key Vault at runtime — no secrets in env vars, config files, or on disk. Supports automatic rotation. For non-Azure deployments, HashiCorp Vault as an alternative backend.

## SQL Classifier Improvements

**Status:** Deferred
**Impact:** Security — keyword-based classification can't detect subtle threats

The SQL tier classifier uses keyword matching (SELECT = T1, INSERT = T2, DELETE = T3, DROP = T4). It can't detect: SELECT \* returning millions of rows, SQL injection in agent-constructed queries, data exfiltration via benign-looking queries, or queries that combine safe keywords into dangerous operations.

**Planned approach:** Query parsing (not just keyword matching) to understand query structure. Result set limits (row caps). Parameterized query enforcement. Integration with database-level audit logs to correlate proxy classification with actual query impact.

## Query Result Filtering / PII Masking

**Status:** Deferred
**Impact:** Privacy — query results may contain PII that agents don't need

Even when a query is allowed, the result set may contain sensitive data (emails, SSNs, financial data) that the agent doesn't need for its task.

**Planned approach:** Configurable column-level masking rules. Specific columns (email, phone, SSN) are masked or redacted in query results before they reach the agent. Rule sets defined per-database or per-table in `.cordon.yaml`.

## Persistent Approval Grants

**Status:** Deferred
**Impact:** UX — approval grants (session/pattern-based) are lost on server restart

When a user approves a T3 operation with "session" or "pattern" scope, that grant is stored in memory and lost on restart. The developer has to re-approve the same operations after every server restart.

**Planned approach:** Persist grants to PostgreSQL alongside audit entries. Grants have TTLs and are scoped to tenant + workspace. Expired grants are cleaned up automatically.

## Idle Timeout Enforcement

**Status:** Deferred
**Impact:** Containers run indefinitely until max lifetime or manual destroy

The `IdleTimeout` field exists in `WorkspaceConfig` (default 15 min) but is not enforced.

**Planned approach:** Background goroutine that periodically checks last activity time (from terminal relay or exec calls). Suspend idle containers after timeout. Notify via WebSocket before suspending.

## PostgreSQL Wire Protocol Proxy

**Status:** Deferred (v2)
**Impact:** Database access only works through HTTP proxy, not raw `psql`

The current proxy intercepts HTTP requests only. PostgreSQL credentials are exchanged at connection time (libpq handshake), which can't be intercepted by an HTTP-level proxy.

**Planned approach:** Go service that accepts Postgres connections with placeholder credentials, replaces with real ones, and forwards to the actual database. Similar to PgBouncer but with secret swapping.

For v1, database access goes through `POST /api/proxy/sql` (MCP tools → HTTP → proxy → DB).
