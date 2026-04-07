CREATE TABLE IF NOT EXISTS secrets (
    id           UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID NOT NULL,
    name         TEXT NOT NULL,
    placeholder  TEXT NOT NULL,
    encrypted_value BYTEA NOT NULL,    -- AES-256-GCM encrypted real value
    nonce        BYTEA NOT NULL,       -- GCM nonce (12 bytes)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),

    UNIQUE (tenant_id, name),
    UNIQUE (tenant_id, placeholder)
);

CREATE INDEX idx_secrets_tenant_placeholder ON secrets (tenant_id, placeholder);
