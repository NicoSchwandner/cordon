package middleware

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

func generateTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return key
}

func signToken(t *testing.T, key *ecdsa.PrivateKey, claims jwt.Claims, extra map[string]interface{}) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: key},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	builder := jwt.Signed(signer).Claims(claims).Claims(extra)
	raw, err := builder.Serialize()
	if err != nil {
		t.Fatalf("serialize token: %v", err)
	}
	return raw
}

func newJWTRequest(token string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/api/test", nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return req
}

func TestJWTAuth_ValidToken(t *testing.T) {
	key := generateTestKey(t)
	tenantID := uuid.New()

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "user-123",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{
		"tenant_id": tenantID.String(),
	})

	tid, uid, err := auth.Validate(newJWTRequest(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tid != tenantID {
		t.Errorf("tenant: got %s, want %s", tid, tenantID)
	}
	if uid != "user-123" {
		t.Errorf("user: got %s, want user-123", uid)
	}
}

func TestJWTAuth_ExpiredToken(t *testing.T) {
	key := generateTestKey(t)

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "user-123",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(-1 * time.Hour)),
	}, map[string]interface{}{
		"tenant_id": uuid.New().String(),
	})

	_, _, err = auth.Validate(newJWTRequest(token))
	if err == nil {
		t.Fatal("expected error for expired token")
	}
}

func TestJWTAuth_WrongSigningKey(t *testing.T) {
	signingKey := generateTestKey(t)
	wrongKey := generateTestKey(t)

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &wrongKey.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, signingKey, jwt.Claims{
		Subject:  "user-123",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{
		"tenant_id": uuid.New().String(),
	})

	_, _, err = auth.Validate(newJWTRequest(token))
	if err == nil {
		t.Fatal("expected error for wrong signing key")
	}
}

func TestJWTAuth_MissingBearer(t *testing.T) {
	key := generateTestKey(t)

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	_, _, err = auth.Validate(newJWTRequest(""))
	if err == nil {
		t.Fatal("expected error for missing bearer token")
	}
}

func TestJWTAuth_WrongAudience(t *testing.T) {
	key := generateTestKey(t)

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "user-123",
		Audience: jwt.Audience{"wrong-audience"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{
		"tenant_id": uuid.New().String(),
	})

	_, _, err = auth.Validate(newJWTRequest(token))
	if err == nil {
		t.Fatal("expected error for wrong audience")
	}
}

func TestJWTAuth_MissingTenantClaim(t *testing.T) {
	key := generateTestKey(t)

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "user-123",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{})

	_, _, err = auth.Validate(newJWTRequest(token))
	if err == nil {
		t.Fatal("expected error for missing tenant claim")
	}
}

func TestJWTAuth_CustomTenantClaim(t *testing.T) {
	key := generateTestKey(t)
	tenantID := uuid.New()

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		StaticKey:   &key.PublicKey,
		Audience:    "cordon",
		TenantClaim: "tid",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "alice@corp.com",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{
		"tid": tenantID.String(),
	})

	tid, uid, err := auth.Validate(newJWTRequest(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tid != tenantID {
		t.Errorf("tenant: got %s, want %s", tid, tenantID)
	}
	if uid != "alice@corp.com" {
		t.Errorf("user: got %s, want alice@corp.com", uid)
	}
}

func TestJWTAuth_JWKSEndpoint(t *testing.T) {
	key := generateTestKey(t)
	tenantID := uuid.New()

	// Serve a JWKS endpoint.
	jwks := jose.JSONWebKeySet{
		Keys: []jose.JSONWebKey{{
			Key:       &key.PublicKey,
			KeyID:     "test-key-1",
			Algorithm: string(jose.ES256),
			Use:       "sig",
		}},
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	}))
	defer srv.Close()

	auth, err := NewJWTAuth(context.Background(), JWTAuthConfig{
		JWKSURL:     srv.URL,
		Audience:    "cordon",
		TenantClaim: "tenant_id",
	})
	if err != nil {
		t.Fatalf("create jwt auth: %v", err)
	}

	token := signToken(t, key, jwt.Claims{
		Subject:  "user-456",
		Audience: jwt.Audience{"cordon"},
		Expiry:   jwt.NewNumericDate(time.Now().Add(1 * time.Hour)),
	}, map[string]interface{}{
		"tenant_id": tenantID.String(),
	})

	tid, uid, err := auth.Validate(newJWTRequest(token))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tid != tenantID {
		t.Errorf("tenant: got %s, want %s", tid, tenantID)
	}
	if uid != "user-456" {
		t.Errorf("user: got %s, want user-456", uid)
	}
}

func TestJWTAuth_ConfigValidation(t *testing.T) {
	key := generateTestKey(t)

	tests := []struct {
		name string
		cfg  JWTAuthConfig
	}{
		{"no key or url", JWTAuthConfig{Audience: "cordon"}},
		{"no audience", JWTAuthConfig{StaticKey: &key.PublicKey}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewJWTAuth(context.Background(), tt.cfg)
			if err == nil {
				t.Fatal("expected config validation error")
			}
		})
	}
}
