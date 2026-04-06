CREATE TABLE workspaces (
    id               UUID PRIMARY KEY,
    tenant_id        UUID NOT NULL,
    name             TEXT NOT NULL,
    status           TEXT NOT NULL DEFAULT 'creating',
    mode             TEXT NOT NULL DEFAULT 'dev',
    config           JSONB NOT NULL DEFAULT '{}',
    spawned_from     UUID,
    created_at       TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at       TIMESTAMPTZ NOT NULL,
    suspended_at     TIMESTAMPTZ,
    destroyed_at     TIMESTAMPTZ,
    last_activity_at TIMESTAMPTZ
);

CREATE INDEX idx_workspaces_tenant ON workspaces (tenant_id, status);
CREATE INDEX idx_workspaces_expires ON workspaces (expires_at) WHERE status NOT IN ('destroyed');
CREATE INDEX idx_workspaces_spawned ON workspaces (spawned_from) WHERE spawned_from IS NOT NULL;

GRANT SELECT, INSERT, UPDATE ON workspaces TO zt_app;
