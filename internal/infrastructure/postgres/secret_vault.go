package postgres

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

// SecretVault is a PostgreSQL-backed SecretVault that encrypts values at rest
// using AES-256-GCM. The master key is provided at construction time — it
// should come from an env var or (later) a KMS unwrap call, never from the DB.
//
// This implementation satisfies ports.SecretVault and can be swapped for
// Azure Key Vault or HashiCorp Vault by changing the constructor in main.go.
type SecretVault struct {
	pool *pgxpool.Pool
	gcm  cipher.AEAD
}

// NewSecretVault creates an encrypted PostgreSQL-backed secret vault.
// masterKey must be exactly 32 bytes (AES-256).
func NewSecretVault(pool *pgxpool.Pool, masterKey []byte) (*SecretVault, error) {
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("master key must be 32 bytes (AES-256), got %d", len(masterKey))
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, fmt.Errorf("creating AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("creating GCM: %w", err)
	}
	return &SecretVault{pool: pool, gcm: gcm}, nil
}

func (v *SecretVault) encrypt(plaintext string) (ciphertext, nonce []byte, err error) {
	nonce = make([]byte, v.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, nil, fmt.Errorf("generating nonce: %w", err)
	}
	ciphertext = v.gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return ciphertext, nonce, nil
}

func (v *SecretVault) decrypt(ciphertext, nonce []byte) (string, error) {
	plaintext, err := v.gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", fmt.Errorf("decrypting secret: %w", err)
	}
	return string(plaintext), nil
}

func (v *SecretVault) SetSecret(ctx context.Context, tenantID uuid.UUID, name, placeholder, realValue string) error {
	ciphertext, nonce, err := v.encrypt(realValue)
	if err != nil {
		return err
	}
	_, err = v.pool.Exec(ctx, `
		INSERT INTO secrets (tenant_id, name, placeholder, encrypted_value, nonce)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (tenant_id, name) DO UPDATE SET
			placeholder = EXCLUDED.placeholder,
			encrypted_value = EXCLUDED.encrypted_value,
			nonce = EXCLUDED.nonce,
			updated_at = NOW()
	`, tenantID, name, placeholder, ciphertext, nonce)
	return err
}

func (v *SecretVault) DeleteSecret(ctx context.Context, tenantID uuid.UUID, name string) error {
	tag, err := v.pool.Exec(ctx, `DELETE FROM secrets WHERE tenant_id = $1 AND name = $2`, tenantID, name)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("secret %q not found", name)
	}
	return nil
}

func (v *SecretVault) Resolve(ctx context.Context, tenantID uuid.UUID, placeholder string) (string, error) {
	var ciphertext, nonce []byte
	err := v.pool.QueryRow(ctx,
		`SELECT encrypted_value, nonce FROM secrets WHERE tenant_id = $1 AND placeholder = $2`,
		tenantID, placeholder,
	).Scan(&ciphertext, &nonce)
	if err != nil {
		return "", fmt.Errorf("secret not found for placeholder %q", placeholder)
	}
	return v.decrypt(ciphertext, nonce)
}

func (v *SecretVault) RevealValue(ctx context.Context, tenantID uuid.UUID, name string) (string, error) {
	var ciphertext, nonce []byte
	err := v.pool.QueryRow(ctx,
		`SELECT encrypted_value, nonce FROM secrets WHERE tenant_id = $1 AND name = $2`,
		tenantID, name,
	).Scan(&ciphertext, &nonce)
	if err != nil {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return v.decrypt(ciphertext, nonce)
}

func (v *SecretVault) ListRefs(ctx context.Context, tenantID uuid.UUID) ([]domain.SecretRef, error) {
	rows, err := v.pool.Query(ctx,
		`SELECT name, placeholder FROM secrets WHERE tenant_id = $1 ORDER BY name`,
		tenantID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var refs []domain.SecretRef
	for rows.Next() {
		var ref domain.SecretRef
		if err := rows.Scan(&ref.Name, &ref.Placeholder); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (v *SecretVault) PlaceholderFor(ctx context.Context, tenantID uuid.UUID, name string) (string, error) {
	var placeholder string
	err := v.pool.QueryRow(ctx,
		`SELECT placeholder FROM secrets WHERE tenant_id = $1 AND name = $2`,
		tenantID, name,
	).Scan(&placeholder)
	if err != nil {
		return "", fmt.Errorf("secret %q not found", name)
	}
	return placeholder, nil
}
