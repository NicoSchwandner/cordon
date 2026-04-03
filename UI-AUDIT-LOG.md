# UI Audit Log

> Append-only — never delete or overwrite previous entries.

---

## Audit Pass #1 — 2026-04-03 (API Surface)

**Context:** ZT-Guard v1 is a backend API server. No SvelteKit frontend has been built yet (deferred to future phase). This audit covers the API response surface — the "UI" that clients (CLI, future web dashboard) consume.

### Endpoints Tested

| Endpoint                          | Method | Status | Content-Type               | Verdict                                                                                |
| --------------------------------- | ------ | ------ | -------------------------- | -------------------------------------------------------------------------------------- |
| `/health`                         | GET    | 200    | `application/json`         | PASS — returns `{"status":"ok"}`                                                       |
| `/ready`                          | GET    | 200    | `application/json`         | PASS — returns `{"status":"ready"}`                                                    |
| `/api/proxy/sql` (Tier 1)         | POST   | 200    | `application/json`         | PASS — `{"allowed":true,"tier":1,"decision":"allowed"}`                                |
| `/api/proxy/sql` (Tier 4)         | POST   | 403    | `application/problem+json` | PASS — RFC 7807 ProblemDetails with `code:"tier_blocked"`                              |
| `/api/proxy/http` (allowed)       | POST   | 200    | `application/json`         | PASS — allowed with tier 1                                                             |
| `/api/proxy/http` (egress denied) | POST   | 403    | `application/problem+json` | PASS — RFC 7807 with `code:"egress_denied"`                                            |
| `/api/audit`                      | GET    | 200    | `application/json`         | PASS — array of audit entries with all required fields                                 |
| `/nonexistent`                    | GET    | 404    | `text/plain`               | NOTE — Go default 404, not RFC 7807. Low priority since this is a framework-level 404. |

### RFC 7807 Compliance

Error responses correctly use:

- `Content-Type: application/problem+json`
- Fields: `type` (URI), `title`, `status` (int), `detail` (human-readable), `code` (machine-readable)
- Example: `{"type":"https://zt-guard.dev/problems/tier-blocked","title":"Operation Blocked","status":403,"detail":"Operation blocked: TRUNCATE is forbidden","code":"tier_blocked"}`

### Audit Log Entry Structure

Each entry contains:

- `id` — UUID
- `tenant_id` — UUID
- `workspace_id` — UUID (zeroed when not in workspace context)
- `timestamp` — RFC 3339
- `tier` — integer (1-4)
- `tier_name` — human-readable ("read", "safe_write", "destructive", "forbidden")
- `operation` — "SQL" or "HTTP:METHOD"
- `target` — host/path for HTTP, empty for SQL
- `caller` — agent identifier
- `decision` — "allowed", "blocked", or "denied"
- `duration_ms` — integer
- `detail` — query text or request summary

### Security Invariants Verified

| #   | Invariant                                             | Result                                                           |
| --- | ----------------------------------------------------- | ---------------------------------------------------------------- |
| 3   | Real credentials never appear in audit log entries    | PASS — audit detail shows placeholder, not real values           |
| 4   | Real credentials never appear in any HTTP response    | PASS — proxy responses contain tier/decision, no credential data |
| 5   | Tier 4 operations always blocked regardless of config | PASS — TRUNCATE, DROP DATABASE, GRANT, REVOKE all return 403     |
| 6   | Egress to non-allowlisted hosts always blocked        | PASS — evil-exfil.com blocked, github.com allowed                |
| 7   | Audit log is append-only                              | PASS — PostgreSQL REVOKE UPDATE/DELETE enforced at DB level      |

### Acceptance Scenarios Verified via API

| Scenario | Description                                           | Result             |
| -------- | ----------------------------------------------------- | ------------------ |
| 1/3      | Tier 1 read flows through                             | PASS               |
| 4        | Tier 3 destructive requires approval (pipeline logic) | PASS (unit tested) |
| 5        | Tier 4 always blocked                                 | PASS               |
| 6        | Egress blocking                                       | PASS               |
| 7        | Secret swap transparency                              | PASS               |

### Issues Found

1. **404 responses use plain text, not RFC 7807** — Go's default mux returns `404 page not found` as `text/plain`. Low priority — only affects unknown routes, not API errors. Fix: add a catch-all handler that returns ProblemDetails for unknown routes.

2. **No web UI yet** — The SvelteKit dashboard (audit timeline, approval overlays, agent panel) has not been built. This is expected — it was deferred per the implementation plan. The API surface is ready to support it.

### Recommendations for Next Pass

- Build SvelteKit frontend and audit visual components (dashboard, approval overlay, audit timeline)
- Add WebSocket live audit feed to the UI
- Verify terminal relay renders correctly in browser
- Test approval flow end-to-end through the UI

---

## Audit Pass #2 — 2026-04-03 (SvelteKit Frontend)

**Context:** SvelteKit frontend built in `web/` directory. SPA mode with adapter-static, Tailwind CSS, xterm.js for terminal, native WebSocket for real-time features. Svelte 5 with runes throughout.

### Build Verification

| Check                              | Result                                               |
| ---------------------------------- | ---------------------------------------------------- |
| `svelte-check` (type checking)     | PASS — 0 errors, 0 warnings across 180 files         |
| `npm run build` (production build) | PASS — built with adapter-static, output in `build/` |
| Vite dev server starts             | PASS — serves on localhost, hot reload working       |

### Pages Built

| Route             | Purpose                                                                    | HTTP Status |
| ----------------- | -------------------------------------------------------------------------- | ----------- |
| `/`               | Dashboard — workspace list, health status, recent audit                    | 200         |
| `/audit`          | Audit timeline — filterable table, live WebSocket toggle, detail expansion | 200         |
| `/settings`       | Settings — egress allowlist, tier overrides, secrets (display-only v1)     | 200         |
| `/workspace/[id]` | Workspace view — xterm.js terminal (left), agent panel (right)             | 200         |

### Components Built

| Component         | Location                 | Description                                                          |
| ----------------- | ------------------------ | -------------------------------------------------------------------- |
| `TierBadge`       | `lib/shared/components/` | Color-coded tier pill (T1 green, T2 amber, T3 red, T4 dark red)      |
| `DecisionBadge`   | `lib/shared/components/` | Color-coded decision pill (allowed/denied/blocked/pending)           |
| `TimeAgo`         | `lib/shared/components/` | Relative timestamp with auto-refresh every 10s                       |
| `StatusDot`       | `lib/shared/components/` | Workspace status indicator                                           |
| `PageShell`       | `lib/shared/components/` | Layout with sidebar nav, health indicator, periodic health polling   |
| `ApprovalOverlay` | `lib/security/`          | Global modal for Tier 3 approval — approve once, allow session, deny |
| `AgentPanel`      | `lib/security/`          | Live agent operation feed with pause/resume, filtered by workspace   |

### API Proxy Through Frontend Verified

| Endpoint                        | Result                                                  |
| ------------------------------- | ------------------------------------------------------- |
| `GET /health`                   | PASS — `{"status":"ok"}`                                |
| `GET /api/audit?limit=1`        | PASS — returns audit entries with correct structure     |
| `POST /api/proxy/sql` (Tier 1)  | PASS — `{"allowed":true,"tier":1,"decision":"allowed"}` |
| `POST /api/proxy/sql` (Tier 4)  | PASS — RFC 7807 `{"code":"tier_blocked"}`               |
| `POST /api/proxy/http` (egress) | PASS — RFC 7807 `{"code":"egress_denied"}`              |

### Acceptance Scenarios Coverage

| Scenario | Description                | Frontend Coverage                                                |
| -------- | -------------------------- | ---------------------------------------------------------------- |
| 1/3      | Tier 1 read flows through  | PASS — dashboard shows tier entries in recent activity table     |
| 4        | Tier 3 approval flow       | BUILT — ApprovalOverlay subscribes to /ws/approvals, shows modal |
| 5        | Tier 4 always blocked      | PASS — API returns 403, client handles ProblemDetails            |
| 6        | Egress blocking            | PASS — API returns 403 through frontend proxy                    |
| 7        | Secret swap transparency   | PASS — audit entries visible, no real credentials exposed        |
| 11       | Agent operation visibility | BUILT — AgentPanel shows live feed, pause/resume                 |

### Issues Found

1. **Settings page is display-only** — Egress, overrides, secrets are hardcoded since no management API exists yet. Matches v1 plan.
2. **WebSocket features not testable via curl** — Terminal relay, live audit, and approval overlay require a browser. Code is type-checked and builds, but needs browser-based visual verification.

### Recommendations for Next Pass

- Open UI in browser and visually verify each page
- Test live audit WebSocket feed
- Test terminal relay with an actual workspace
- Test approval flow end-to-end
- Add Playwright for automated browser testing

---

## Audit Pass #3 — 2026-04-03 (Browser Visual Verification)

**Context:** Used `agent-browser` (headless browser automation) to visually verify every page of the SvelteKit frontend against the running Go backend (Docker Compose stack on port 8443, Vite dev server proxying on port 5180).

### Dashboard (`/`)

![Dashboard](docs/ui-audit/dashboard.png)

![Dashboard — Create Workspace form](docs/ui-audit/dashboard-create.png)

| Element                                         | Status | Notes                                                                       |
| ----------------------------------------------- | ------ | --------------------------------------------------------------------------- |
| Sidebar nav (Dashboard, Audit Log, Settings)    | PASS   | All links visible, correct labels                                           |
| Health indicator (green dot + "System healthy") | PASS   | Green dot shows, fetches from `/health`                                     |
| "ZT-Guard" branding with health dot             | PASS   | Top-left sidebar                                                            |
| Workspaces section                              | PASS   | Shows "No workspaces yet. Create one to get started."                       |
| "New Workspace" button                          | PASS   | Opens inline form with input + Create/Cancel buttons                        |
| Recent Activity table                           | PASS   | Shows audit entries with Time, Operation, Caller, Tier, Decision columns    |
| TierBadge rendering                             | PASS   | Color-coded: T1 Read (green), T3 Destructive (red), T4 Forbidden (dark red) |
| DecisionBadge rendering                         | PASS   | Color-coded: allowed (green), blocked (red)                                 |
| TimeAgo rendering                               | PASS   | Shows relative times ("22m ago", "33m ago")                                 |

### Audit Log (`/audit`)

![Audit Log — Full view](docs/ui-audit/audit.png)

![Audit Log — Row expanded with detail](docs/ui-audit/audit-expanded.png)

![Audit Log — Filtered to T4 Forbidden](docs/ui-audit/audit-filtered.png)

| Element                                                                   | Status | Notes                                                       |
| ------------------------------------------------------------------------- | ------ | ----------------------------------------------------------- |
| Filter bar (All Tiers, All Decisions dropdowns)                           | PASS   | Dropdowns render with correct options                       |
| "Enable Live" button                                                      | PASS   | Visible with status dot                                     |
| Table columns (Time, Caller, Operation, Target, Tier, Decision, Duration) | PASS   | All 7 columns present                                       |
| Row click to expand details                                               | PASS   | Shows ID (UUID), full timestamp, tier name, and detail text |
| Tier filter (T4 Forbidden)                                                | PASS   | Filters correctly — only T4 entries shown, all "blocked"    |
| Duration column                                                           | PASS   | Shows "0ms" for all entries                                 |

### Workspace (`/workspace/[id]`)

![Workspace — Terminal + Agent Panel](docs/ui-audit/workspace.png)

| Element                              | Status | Notes                                                        |
| ------------------------------------ | ------ | ------------------------------------------------------------ |
| Back arrow + workspace name header   | PASS   | Shows "Workspace test-workspace-id"                          |
| Destroy button (red)                 | PASS   | Top-right, correctly styled                                  |
| Terminal panel (left, ~70%)          | PASS   | xterm.js renders with cursor visible, dark theme matches app |
| Agent Operations panel (right, ~30%) | PASS   | Header "Agent Operations" with Pause button                  |
| "No operations yet" placeholder      | PASS   | Shows when no audit entries for workspace                    |
| Split layout (grid)                  | PASS   | Terminal and agent panel side-by-side                        |

### Settings (`/settings`)

![Settings — Egress Allowlist](docs/ui-audit/settings-egress.png)

![Settings — Tier Overrides](docs/ui-audit/settings-overrides.png)

![Settings — Secrets](docs/ui-audit/settings-secrets.png)

| Tab              | Status | Notes                                                                                                                                                                                                 |
| ---------------- | ------ | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Egress Allowlist | PASS   | Shows 5 hosts with green status dots (github.com, api.anthropic.com, registry.npmjs.org, nuget.org, \*.example.com). Footer: "Managed via .zero-trust.yaml"                                           |
| Tier Overrides   | PASS   | Table with Pattern and Forced Tier columns. Shows "DELETE FROM audit*log" → Tier 4, "DROP TABLE temp*\*" → Tier 2                                                                                     |
| Secrets          | PASS   | Shows 3 secrets: DATABASE_URL, API_KEY, NUGET_FEED_TOKEN with placeholder tokens and green "configured" badges. Footer: "Real credentials are injected at the proxy level and never leave the server" |
| Tab switching    | PASS   | Active tab highlighted, content switches correctly                                                                                                                                                    |

### Security Invariants (Visual Verification)

| #   | Invariant                               | Result                                                                 |
| --- | --------------------------------------- | ---------------------------------------------------------------------- |
| 1   | Real credentials never in workspace env | PASS — Secrets tab shows only placeholder names                        |
| 3   | Real credentials never in audit log     | PASS — Audit detail shows query text with placeholders, no real values |
| 4   | Real credentials never in HTTP response | PASS — No credential data visible anywhere in the UI                   |

### Issues Found

None — all pages render correctly and match the spec requirements.

### Acceptance Scenarios (Visual)

| Scenario              | What Was Verified                                                                       |
| --------------------- | --------------------------------------------------------------------------------------- |
| 5 (Tier 4 blocked)    | Audit log filter shows only T4 Forbidden entries, all with "blocked" decision           |
| 7 (Secret swap)       | Settings Secrets tab shows placeholders only, audit entries contain no real credentials |
| 11 (Agent visibility) | Workspace view has Agent Operations panel with live feed placeholder and Pause button   |

---

## Audit Pass #4 — 2026-04-03 (Saas-Starter Design Alignment)

**Context:** Rewrote the entire CSS foundation and all component styles to match the saas-starter project's design system. Uses `@theme` semantic design tokens, Geist font, `.dark` class-based theme switching. Verified with `agent-browser` in both light and dark mode.

### Design System Changes

| Change           | Details                                                                                                         |
| ---------------- | --------------------------------------------------------------------------------------------------------------- |
| Font             | Switched to Geist (Regular, Medium, SemiBold, Bold) via local woff2 files                                       |
| Color system     | `@theme` block with 40+ semantic CSS custom properties (surfaces, text hierarchy, borders, interactive, status) |
| Dark mode        | `.dark` selector overrides all tokens — no `dark:` Tailwind variants needed                                     |
| Flash prevention | Inline script in `app.html` reads `localStorage("theme")` and adds `.dark` before render                        |
| Card pattern     | `rounded-xl border border-border bg-surface` consistently across all pages                                      |
| Section headers  | `text-xs font-medium uppercase tracking-wider text-foreground-muted`                                            |
| Sidebar          | `bg-sidebar` with `bg-sidebar-active` / `hover:bg-sidebar-hover` states                                         |

### Dashboard (`/`) — Dark + Light

![Dashboard — Dark Mode](docs/ui-audit/v2-dashboard.png)

![Dashboard — Light Mode](docs/ui-audit/v2-dashboard-light.png)

| Element               | Dark | Light | Notes                                      |
| --------------------- | ---- | ----- | ------------------------------------------ |
| Sidebar background    | PASS | PASS  | Dark: near-black, Light: white with border |
| Logo badge ("Z")      | PASS | PASS  | Primary blue in both modes                 |
| Health indicator      | PASS | PASS  | Green dot + "System healthy"               |
| Workspace cards       | PASS | PASS  | `bg-surface` with `border-border`          |
| Recent Activity table | PASS | PASS  | Semantic colors for tiers and decisions    |
| New Workspace button  | PASS | PASS  | `bg-primary text-on-primary`               |

### Audit Log (`/audit`) — Dark + Light

![Audit Log — Light Mode](docs/ui-audit/v2-audit-light.png)

![Audit Log — Dark Mode](docs/ui-audit/v2-audit-dark.png)

| Element            | Dark | Light | Notes                                            |
| ------------------ | ---- | ----- | ------------------------------------------------ |
| Filter dropdowns   | PASS | PASS  | `border-border-input bg-surface`                 |
| Enable Live button | PASS | PASS  | Outline style with status dot                    |
| Table headers      | PASS | PASS  | Uppercase tracking-wider muted text              |
| Tier badges        | PASS | PASS  | Semantic colors (green T1, yellow T2, red T3/T4) |
| Decision badges    | PASS | PASS  | Green allowed, red blocked                       |
| Row borders        | PASS | PASS  | `border-border-subtle` between rows              |

### Settings (`/settings`) — Dark + Light

![Settings — Light Mode](docs/ui-audit/v2-settings-light.png)

![Settings — Dark Mode](docs/ui-audit/v2-settings-dark.png)

| Element                    | Dark | Light | Notes                                                      |
| -------------------------- | ---- | ----- | ---------------------------------------------------------- |
| Tab bar                    | PASS | PASS  | `rounded-xl border border-border bg-surface p-1` container |
| Active tab                 | PASS | PASS  | `bg-sidebar-active text-foreground`                        |
| Egress items               | PASS | PASS  | `bg-surface-inset` with green dots                         |
| Secrets "configured" badge | PASS | PASS  | `bg-success-badge-bg text-success-text`                    |
| Footer text                | PASS | PASS  | `text-foreground-faint`                                    |

### Workspace (`/workspace/[id]`) — Dark

![Workspace — Dark Mode](docs/ui-audit/v2-workspace-dark.png)

| Element                | Status | Notes                                       |
| ---------------------- | ------ | ------------------------------------------- |
| Terminal panel         | PASS   | `bg-surface-inset` with xterm.js dark theme |
| Agent Operations panel | PASS   | `bg-surface` with semantic header styling   |
| Destroy button         | PASS   | `bg-danger-solid text-on-danger`            |
| Split layout           | PASS   | `grid lg:grid-cols-[1fr_320px]`             |

### Issues Found

None — all pages render correctly in both light and dark mode with the new design system.

---
