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

**Status:** Done
**Impact:** UX — the POST blocks for 1-4 minutes with zero feedback

**Implementation:** `POST /api/workspaces` now returns immediately (HTTP 202) for repo-based workspaces and runs creation in a background goroutine. Progress events stream via SSE at `GET /api/workspaces/{id}/logs`. The frontend shows a progress bar with estimated time remaining (based on historical build durations stored in `data/build-timings.json`) and a step-by-step checklist.

### Image Caching

**Status:** Done
**Impact:** Repeated creates from the same repo rebuild the devcontainer image every time

**Implementation:** Image tags now include a hash of the devcontainer.json content (`repo:branch:config-hash`). Before building, the system checks if the image already exists via `docker image inspect`. If found, the build is skipped entirely — taking the creation time from minutes down to seconds for repeat builds. Config changes automatically invalidate the cache.

### Default Branch Detection

**Status:** Done
**Impact:** Base branch defaults to "development" which is Wint-specific

**Implementation:** When base branch is left empty, the system queries the GitHub API (`GET /repos/{owner}/{repo}`) to read the `default_branch` field. Results are cached per-repo for the server lifetime. Falls back to "main" if the API call fails or no GitHub token is configured. The frontend fetches the detected default branch (debounced) and shows it as the placeholder. Exposed via `GET /api/github/default-branch?repo=...`.

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

**Status:** Done (see "Creation Progress Feedback" above)

Workspace creation from repo is now async with SSE progress streaming, estimated time countdown, and step-by-step progress UI.

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

**Status:** Done

**Implementation:** Multi-repo workspaces implemented in three phases:

1. **Multi-repo in single container** — Multiple repos cloned as siblings under `/workspace/<name>/` in the primary devcontainer. Single Claude Code session edits all repos. API accepts `repos` array with `URL@branch` syntax.

2. **Service containers** — Repos marked with `service_container: true` get their own devcontainer-built containers on the shared workspace network. Files shared via named Docker volumes. `POST /api/workspaces/{id}/exec` routes commands to the right container based on `repo` parameter. Enhanced destroy cleans up all containers + volumes.

3. **Path-based auto-routing** — A sidecar agent (`cordon-agent`) binary installed in containers. Shell wrappers for common tools (dotnet, npm, go, etc.) intercept commands and route to the correct service container based on `$PWD`. `cd /workspace/frontend && npm test` transparently executes in the frontend's service container.

## Image Caching

**Status:** Done (see "Image Caching" in Near-Term section above)

Images are cached by `repo:branch:devcontainer-config-hash`. Webhook-based invalidation is future work.

## MCP Proxy Support

**Status:** Deferred
**Impact:** Critical — MCP tools (database access, etc.) don't go through the zero-trust pipeline

MCP tools are the primary way AI agents interact with databases and external services. Currently, MCP calls bypass Cordon entirely — there's no interception, classification, or credential swapping for MCP tool invocations.

**Planned approach:** An MCP proxy mode where Cordon acts as an MCP server that wraps real MCP servers. Agent calls MCP tool → Cordon intercepts → classifies the underlying operation (SQL tier, HTTP tier) → swaps credentials → forwards to actual MCP server → audits → returns result. The agent's MCP config points at Cordon, not the real servers.

This is the linchpin for database investigation workflows — developers keep the same DX (MCP tools work identically) while every query goes through tier classification, approval gates, and audit logging.

## HTTP Proxy Forwarding

**Status:** Done

**Implementation:** The proxy is now a true forward proxy. When a request is allowed, the handler builds an outbound HTTP request from the secret-swapped `ModifiedReq`, executes it via `http.Client` (30s timeout, no redirect following), and relays the upstream response (status, headers, body) back to the agent. The agent never needs direct network access or real credentials — both stay on the Cordon server. Response bodies are capped at 1MB.

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
