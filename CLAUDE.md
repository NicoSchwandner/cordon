# Cordon

Zero-trust developer environment. Go backend + SvelteKit frontend.

## Build & Test

```bash
# Go (from repo root)
go build ./...                           # compile all
go test ./... -short                     # unit + arch tests (skip testcontainers)
go test ./internal/infrastructure/postgres/... -v  # integration tests (needs Docker)
go test ./e2e/... -tags=e2e -v           # E2E tests (needs docker compose stack)

# Frontend (from web/)
cd web && npm install && npm run dev     # dev server (proxies to :8443)
cd web && npm run build                  # production build
cd web && npx svelte-check               # type checking

# Full stack
docker compose up -d --build             # postgres + migrations + Go server on :8443
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

Enforced by `archtest/layers_test.go`:

- `domain` must not import `application`, `infrastructure`, or `api`
- `application` must not import `infrastructure` or `api`
- `infrastructure` must not import `api`

## Key Patterns

- **Tier classification**: SQL keyword-based (T1-T4), HTTP method-based. Overrides via `.cordon.yaml`.
- **Secret swap**: Placeholder strings in requests replaced with real credentials at proxy level. Real values never in audit logs or API responses.
- **Error responses**: RFC 7807 ProblemDetails (`application/problem+json`), type URLs at `cordon.dev/problems/`.
- **Auth**: Pluggable via `AuthValidator` interface. Static mode (single tenant) for dev, token mode for multi-tenant.
- **Frontend**: SvelteKit 5 with Svelte 5 runes, Tailwind v4, `@theme` semantic tokens, adapter-static (SPA).

## Environment Variables

| Variable            | Default                               | Description               |
| ------------------- | ------------------------------------- | ------------------------- |
| `PORT`              | `8443`                                | Server listen port        |
| `DATABASE_URL`      | `postgres://...localhost:5432/cordon` | PostgreSQL connection     |
| `AUTH_MODE`         | `static`                              | `static` or `token`       |
| `DEFAULT_TENANT_ID` | `00000000-...0001`                    | Tenant ID for static auth |
| `CORDON_SERVER`     | `http://localhost:8443`               | CLI/E2E server URL        |
