package domain

import "github.com/google/uuid"

// SecretRef maps a placeholder token to a secret name.
type SecretRef struct {
	Name        string
	Placeholder string
}

// SecretEntry is a secret registered for a tenant.
// The actual encrypted value lives in SOPS, never in this struct.
type SecretEntry struct {
	TenantID    uuid.UUID
	Name        string
	Placeholder string
}
