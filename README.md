# Cordon

Zero-trust developer environment. Secrets never touch your machine. Destructive operations require approval. Everything is audit-logged.

## Status

**Go backend + SvelteKit frontend** — all phases complete. Vertical slice working end-to-end.

| Phase                             | Status         |
| --------------------------------- | -------------- |
| 0. Project Bootstrap + Arch Tests | Done           |
| 1. Tier Classification Engine     | Done           |
| 2. Audit Store (PostgreSQL)       | Done           |
| 3. Secret Swap                    | Done           |
| 4. Proxy Pipeline                 | Done           |
| 5. Approval Gate                  | Done           |
| 6. API Server + Auth              | Done           |
| 7. Workspace Orchestrator         | Done           |
| 8. Terminal Relay (WebSocket)     | Done           |
| 9. CLI (`cordon`)                 | Done           |
| 10. Config Parser                 | Done           |
| 11. E2E Tests                     | Done (7 tests) |
| 12. SvelteKit Frontend            | Done           |

## Quick Start

```bash
# Start the backend (Postgres + migrations + server)
docker compose up -d --build

# Start the frontend
cd web && npm install && npm run dev

# Verify health
curl http://localhost:8443/health

# Tier 1 — allowed
curl -s -X POST http://localhost:8443/api/proxy/sql \
  -H 'Content-Type: application/json' \
  -d '{"query":"SELECT * FROM users WHERE id = 1","caller":"my-agent"}'

# Tier 4 — blocked (403)
curl -s -X POST http://localhost:8443/api/proxy/sql \
  -H 'Content-Type: application/json' \
  -d '{"query":"TRUNCATE TABLE users","caller":"my-agent"}'

# Egress check
curl -s -X POST http://localhost:8443/api/proxy/http \
  -H 'Content-Type: application/json' \
  -d '{"method":"POST","host":"evil-exfil.com","url":"/steal","caller":"agent"}'

# View audit log
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
cordon connect <workspace-id>     # Terminal relay (WebSocket)
```

## Architecture

```
Agent (in sandbox) --> Cordon API Server (port 8443) --> Target Service
                            |
                       Tier classifier (SQL keyword / HTTP method)
                       Secret swap (placeholder --> real credential)
                       Audit logger (append-only PostgreSQL)
                       Approval gate (Tier 3 requires human approval)
                       Egress checker (allowlist-based)
```

### Clean Architecture Layers

```
domain/           Pure types, no imports outside stdlib
application/      Ports (interfaces) + use cases (proxy pipeline, audit service)
infrastructure/   PostgreSQL, Docker, WebSocket, config parser
api/              HTTP handlers, middleware, WebSocket handlers
cmd/              Server + CLI entry points
```

Architecture constraints enforced by `archtest/layers_test.go` (AST-based).

## Operation Tiers

| Tier | Action                 | Examples                                           |
| ---- | ---------------------- | -------------------------------------------------- |
| 1    | Allow + log            | SELECT (with WHERE/LIMIT), HTTP GET, EXPLAIN, SHOW |
| 2    | Allow + log            | INSERT, HTTP POST/PUT/PATCH                        |
| 3    | Require approval + log | UPDATE, DELETE, DROP TABLE, unbounded SELECT       |
| 4    | Always block + log     | DROP DATABASE, TRUNCATE, GRANT, REVOKE             |

## Testing

```bash
# Unit + arch tests
go test ./... -short

# E2E tests (requires Docker Compose stack running)
go test ./e2e/... -tags=e2e -v

# Integration tests with testcontainers (auto-starts Postgres)
go test ./internal/infrastructure/postgres/... -v
```

## Design Decisions

| Decision       | Choice                  | Rationale                                                   |
| -------------- | ----------------------- | ----------------------------------------------------------- |
| DB access (v1) | HTTP-only (`POST /sql`) | Postgres wire protocol can't be intercepted by HTTP proxy   |
| SQL parser     | Keyword-based           | Simple, sufficient for v1, upgradeable to vitess/sqlparser  |
| Auth (v1)      | Static tenant ID        | Good enough for single-user; Ory Kratos integration planned |
| Approval store | In-memory with channels | No persistence needed for v1; decisions are ephemeral       |

## License

MIT
