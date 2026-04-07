# Cordon

Zero-trust developer environment. Go backend + SvelteKit frontend.

## Build & Test

All commands are available via `make`. Run `make help` for the full list.

```bash
make install-tools     # one-time: install air (Go hot reload) + npm deps
make dev               # full dev stack: postgres + Go (hot reload) + Vite (HMR)
make test              # unit + arch tests (Go + frontend types)
make test-integration  # integration tests (testcontainers, needs Docker)
make test-e2e          # E2E (spins up full stack, runs tests, tears down)
make stop              # stop all services
```

## Project Structure

```
cmd/server/          Server entry point (port 8443)
cmd/cordon/          CLI entry point (cobra)
internal/
  domain/            Pure types — no imports outside stdlib
  application/
    ports/           Interfaces (TierClassifier, AuditStore, SecretVault, etc.)
    proxy/           Pipeline: classify -> egress check -> secret swap -> audit
    audit/           Audit query service
  infrastructure/
    postgres/        Audit store (pgx), testcontainers helpers
    docker/          Workspace provider (devcontainer CLI)
    sops/            Secret vault (in-memory for now)
    websocket/       Approval store (channels + timeout)
    config/          .cordon.yaml parser
  api/
    handlers/        HTTP handlers (proxy, audit, approval, workspace, health)
    middleware/      Auth + error (RFC 7807) middleware
    ws/              WebSocket handlers (terminal, audit feed, approvals)
archtest/            AST-based architecture constraint tests
e2e/                 End-to-end tests (require running stack)
migrations/          PostgreSQL migrations (golang-migrate)
web/                 SvelteKit frontend (adapter-static, SPA mode)
docs/                Spec, acceptance tests, UI audit screenshots
```

## Architecture Rules

Enforced by `archtest/`:

- `domain` must not import `application`, `infrastructure`, or `api`
- `application` must not import `infrastructure` or `api`
- `infrastructure` must not import `api`
- No `.go` file may exceed 750 lines (split methods across files in the same package)
- No function may exceed 350 lines (extract phases into well-named helpers)

## Key Patterns

- **Tier classification**: SQL keyword-based (T1-T4), HTTP method-based. Overrides via `.cordon.yaml`.
- **Secret swap**: Placeholder strings in requests replaced with real credentials at proxy level. Real values never in audit logs or API responses.
- **Error responses**: RFC 7807 ProblemDetails (`application/problem+json`), type URLs at `cordon.dev/problems/`.
- **Auth**: Pluggable via `AuthValidator` interface. Static mode (single tenant) for dev, token mode for multi-tenant.
- **Frontend**: SvelteKit 5 with Svelte 5 runes, Tailwind v4, `@theme` semantic tokens, adapter-static (SPA).

## Security Model

See README.md "Security Model — Defense in Depth" for the full threat model and layer definitions. Key principles for development:

- **Assume containers are hostile.** Never trust data originating from a workspace container. Validate at every trust boundary (proxy pipeline, API handlers, WebSocket connections).
- **Secrets never enter containers.** Real credentials exist only on the server side. Containers get placeholders; the proxy swaps them. If you're writing code that passes credentials, it must go through `SecretVault` and the swap pipeline.
- **Every operation must be audited.** Any new proxy path or operation type needs an audit entry before the response is returned. No silent operations.
- **Egress is deny-by-default.** Workspaces can only reach allowlisted hosts. New integrations must be added to the allowlist explicitly.
- **Classification before execution.** Every SQL query and HTTP request must be tier-classified before it touches the upstream service.

### Known Architecture Debt

These are pragmatic shortcuts taken for the MVP that should be addressed for production readiness. When working in these areas, prefer the principled fix over extending the shortcut.

**In-memory stores used in production paths:**

- `MemoryVault` (`infrastructure/sops/`) — secrets stored in RAM, loaded from env vars. Needs real vault backend (Azure Key Vault / HashiCorp Vault).
- `MemoryRegistry` (`application/workspace/`) — workspace IP-to-ID mapping lost on restart. Needs persistent store.
- `ApprovalStore` (`infrastructure/websocket/`) — approval grants lost on restart. Needs PostgreSQL persistence.

**Docker-specific code in application layer:**

- `workspace/helpers.go:registerWorkspaceIP()` — runs `hostname -I` via Docker exec, parses Docker binary stream headers. Should use backend-agnostic IP discovery.
- `workspace/persistent_service.go` — accesses Docker container labels directly (`dws.Labels["cordon.service-for"]`). Should read from a persistent workspace store.
- Network naming (`{name}-net`, `{name}-gateway`) assumes Docker conventions.

**Hardcoded configuration values (should be centralized):**

- Timeouts: idle (15m), approval (5m), workspace lifetime (8h), async ops (30m) — scattered across `cmd/server/main.go`, handlers, and helpers. Often duplicated.
- Resource limits: proxy response body cap (1MB), clone concurrency (12), gateway resources (50m CPU, 64MB).
- Egress allowlist: hardcoded in `cmd/server/main.go`, not configurable per-tenant or per-workspace.

**Best-effort audit writes:**

- `pipeline.go:writeAudit()` swallows errors silently. For a security-critical audit trail, this should either block the request on failure or queue locally for retry.

**Missing provider abstraction for multi-backend:**

- The `ComputeBackend` interface is well-designed, but the orchestrator layer (`workspace/`) still reaches through to Docker-specific behaviors (labels, exec output parsing, network naming).
- Gateway/egress enforcement is Docker-specific (haproxy sidecar). Azure/Firecracker backends need their own isolation mechanisms behind the same `EnsureProxyAccess()` interface.
- Workspace state lives in Docker container labels. Must migrate to a `workspaces` PostgreSQL table before adding non-Docker backends (see OPEN-ISSUES.md "Persistent Workspace Store").

**No logging abstraction:**

- 100+ `log.Printf` calls with hardcoded prefixes. No structured logging, no request/trace ID correlation. Switching to `slog` would be a low-effort improvement.

## Code Quality Guidelines

### Go Backend

- **No duplication**: If you write the same pattern 3+ times, extract a helper. Check for existing helpers first (e.g., `extractWorkspaceID` in handlers).
- **Split large files**: Go treats all files in a package as one unit — split methods across files by concern (e.g., `container.go`, `exec.go`, `workspace_ops.go`).

### Frontend

- **Error handling**: Never use `alert()`. Use the `<MessageBanner>` component with `$state` error variables. Run `make lint-web` to verify.
- **Catch blocks**: Always log errors with `console.warn()` unless the catch is intentionally silent (add `// intentional` comment explaining why).
- **State management**: Use explicit union types (e.g., `$state<'initial' | 'live' | 'filtered'>('initial')`) instead of boolean flags when a component has 3+ states. Clear state on mode transitions.

## Environment Variables

| Variable                | Default                               | Description                                                |
| ----------------------- | ------------------------------------- | ---------------------------------------------------------- |
| `PORT`                  | `8443`                                | Server listen port                                         |
| `DATABASE_URL`          | `postgres://...localhost:5432/cordon` | PostgreSQL connection                                      |
| `AUTH_MODE`             | `static`                              | `static` or `token`                                        |
| `DEFAULT_TENANT_ID`     | `00000000-...0001`                    | Tenant ID for static auth                                  |
| `CORDON_SERVER`         | `http://localhost:8443`               | CLI/E2E server URL                                         |
| `CORDON_PROXY_ADDR`     | `host.docker.internal:{PORT}`         | Address workspaces use to reach the proxy                  |
| `CORDON_EGRESS_ENFORCE` | _(enabled by default)_                | Set to `false` to disable network-level egress enforcement |
