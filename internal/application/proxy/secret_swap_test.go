package proxy

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/infrastructure/sops"
)

func setupSwapper() (*SecretSwapper, uuid.UUID) {
	vault := sops.NewMemoryVault()
	tenantID := uuid.New()
	vault.AddSecret(tenantID, "DATABASE_URL", "cordon-placeholder-database-url", "postgresql://real:pass@host/db")
	vault.AddSecret(tenantID, "API_KEY", "cordon-placeholder-api-key", "sk-real-api-key-12345")
	return NewSecretSwapper(vault), tenantID
}

func TestSecretSwapBody(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	req := &ProxyRequest{
		Body:     []byte(`{"connection": "cordon-placeholder-database-url"}`),
		Headers:  map[string]string{},
		TenantID: tenantID,
	}

	modified, swapped, err := swapper.Swap(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	body := string(modified.Body)
	if !strings.Contains(body, "postgresql://real:pass@host/db") {
		t.Errorf("body should contain real credential, got: %s", body)
	}
	if strings.Contains(body, "cordon-placeholder-database-url") {
		t.Error("body should not contain placeholder after swap")
	}
	if len(swapped) != 1 || swapped[0] != "DATABASE_URL" {
		t.Errorf("swapped = %v, want [DATABASE_URL]", swapped)
	}
}

func TestSecretSwapMultiplePlaceholders(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	req := &ProxyRequest{
		Body:    []byte(`{"db": "cordon-placeholder-database-url", "key": "cordon-placeholder-api-key"}`),
		Headers: map[string]string{},
	}

	modified, swapped, err := swapper.Swap(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	body := string(modified.Body)
	if strings.Contains(body, "zt-placeholder") {
		t.Errorf("body still contains placeholders: %s", body)
	}
	if len(swapped) != 2 {
		t.Errorf("expected 2 swapped secrets, got %d: %v", len(swapped), swapped)
	}
}

func TestSecretSwapNoPlaceholders(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	req := &ProxyRequest{
		Body:    []byte(`{"query": "SELECT 1"}`),
		Headers: map[string]string{},
	}

	modified, swapped, err := swapper.Swap(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	if string(modified.Body) != string(req.Body) {
		t.Error("body should be unchanged when no placeholders")
	}
	if len(swapped) != 0 {
		t.Errorf("expected 0 swapped secrets, got %v", swapped)
	}
}

func TestSecretSwapHeader(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	req := &ProxyRequest{
		Body:    []byte{},
		Headers: map[string]string{"Authorization": "Bearer cordon-placeholder-api-key"},
	}

	modified, _, err := swapper.Swap(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	if modified.Headers["Authorization"] != "Bearer sk-real-api-key-12345" {
		t.Errorf("header not swapped: %s", modified.Headers["Authorization"])
	}
}

func TestSecretSwapPath(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	req := &ProxyRequest{
		Path:    "/api/v1/connect?key=cordon-placeholder-api-key",
		Body:    []byte{},
		Headers: map[string]string{},
	}

	modified, _, err := swapper.Swap(ctx, tenantID, req)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	if strings.Contains(modified.Path, "zt-placeholder") {
		t.Errorf("path still contains placeholder: %s", modified.Path)
	}
}

func TestSecretRedact(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	text := "Connected to postgresql://real:pass@host/db using sk-real-api-key-12345"
	redacted, err := swapper.RedactSecrets(ctx, tenantID, text)
	if err != nil {
		t.Fatalf("RedactSecrets: %v", err)
	}

	if strings.Contains(redacted, "real:pass") {
		t.Errorf("redacted text still contains real credential: %s", redacted)
	}
	if strings.Contains(redacted, "sk-real-api-key") {
		t.Errorf("redacted text still contains API key: %s", redacted)
	}
	if !strings.Contains(redacted, "[REDACTED:DATABASE_URL]") {
		t.Errorf("redacted text should contain REDACTED marker: %s", redacted)
	}
}

func TestSecretSwapDoesNotMutateOriginal(t *testing.T) {
	swapper, tenantID := setupSwapper()
	ctx := context.Background()

	original := &ProxyRequest{
		Body:    []byte(`{"db": "cordon-placeholder-database-url"}`),
		Headers: map[string]string{"X-Key": "cordon-placeholder-api-key"},
	}
	origBody := string(original.Body)
	origHeader := original.Headers["X-Key"]

	_, _, err := swapper.Swap(ctx, tenantID, original)
	if err != nil {
		t.Fatalf("Swap: %v", err)
	}

	if string(original.Body) != origBody {
		t.Error("Swap mutated the original request body")
	}
	if original.Headers["X-Key"] != origHeader {
		t.Error("Swap mutated the original request headers")
	}
}
