CREATE TABLE IF NOT EXISTS approval_grants (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    workspace_id UUID NOT NULL,
    operation    TEXT NOT NULL,
    target       TEXT NOT NULL,
    scope        TEXT NOT NULL,         -- 'one_time', 'session', 'pattern'
    pattern      TEXT,                  -- regex pattern for pattern-scoped grants
    granted_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    expires_at   TIMESTAMPTZ NOT NULL,
    consumed     BOOLEAN NOT NULL DEFAULT FALSE
);

CREATE INDEX idx_approval_grants_lookup ON approval_grants (tenant_id, workspace_id, operation, target) WHERE NOT consumed AND expires_at > NOW();
