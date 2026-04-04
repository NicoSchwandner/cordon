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

## Code Quality Guidelines

### Go Backend

- **No duplication**: If you write the same pattern 3+ times, extract a helper. Check for existing helpers first (e.g., `extractWorkspaceID` in handlers).
- **Split large files**: Go treats all files in a package as one unit — split methods across files by concern (e.g., `container.go`, `exec.go`, `workspace_ops.go`).

### Frontend

- **Error handling**: Never use `alert()`. Use the `<MessageBanner>` component with `$state` error variables. Run `make lint-web` to verify.
- **Catch blocks**: Always log errors with `console.warn()` unless the catch is intentionally silent (add `// intentional` comment explaining why).
- **State management**: Use explicit union types (e.g., `$state<'initial' | 'live' | 'filtered'>('initial')`) instead of boolean flags when a component has 3+ states. Clear state on mode transitions.

## Environment Variables

| Variable            | Default                               | Description               |
| ------------------- | ------------------------------------- | ------------------------- |
| `PORT`              | `8443`                                | Server listen port        |
| `DATABASE_URL`      | `postgres://...localhost:5432/cordon` | PostgreSQL connection     |
| `AUTH_MODE`         | `static`                              | `static` or `token`       |
| `DEFAULT_TENANT_ID` | `00000000-...0001`                    | Tenant ID for static auth |
| `CORDON_SERVER`     | `http://localhost:8443`               | CLI/E2E server URL        |
