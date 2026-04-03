CREATE TABLE audit_entries (
    id          UUID PRIMARY KEY,
    tenant_id   UUID NOT NULL,
    workspace_id UUID NOT NULL,
    timestamp   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    tier        INT NOT NULL,
    operation   TEXT NOT NULL,
    target      TEXT NOT NULL,
    caller      TEXT NOT NULL,
    decision    TEXT NOT NULL,
    duration_ms BIGINT NOT NULL DEFAULT 0,
    detail      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX idx_audit_tenant_ts ON audit_entries (tenant_id, timestamp DESC);
CREATE INDEX idx_audit_workspace ON audit_entries (workspace_id, timestamp DESC);

-- Append-only: create a restricted role for the application
DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'zt_app') THEN
        CREATE ROLE zt_app WITH LOGIN PASSWORD 'zt_app';
    END IF;
END
$$;

GRANT SELECT, INSERT ON audit_entries TO zt_app;
-- Explicitly no UPDATE or DELETE
