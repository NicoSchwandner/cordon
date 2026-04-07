package middleware

import (
	"context"
	"crypto"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/google/uuid"
)

// JWTAuth validates JWT Bearer tokens against a JWKS key set.
// It implements AuthValidator and supports both static keys and
// remote JWKS endpoints with automatic refresh.
type JWTAuth struct {
	audience    string
	tenantClaim string

	mu     sync.RWMutex
	keySet jose.JSONWebKeySet

	// For JWKS URL mode: periodic refresh.
	jwksURL    string
	httpClient *http.Client
}

// JWTAuthConfig configures the JWT validator.
type JWTAuthConfig struct {
	// JWKSURL is the remote JWKS endpoint (e.g. https://login.microsoftonline.com/{tenant}/discovery/v2.0/keys).
	// Mutually exclusive with StaticKey.
	JWKSURL string

	// StaticKey is a pre-loaded public key for environments without a JWKS endpoint
	// (e.g. self-signed tokens, Pomerium). Mutually exclusive with JWKSURL.
	StaticKey crypto.PublicKey

	// Audience is the expected "aud" claim. Required.
	Audience string

	// TenantClaim is the JWT claim name containing the tenant ID.
	// Defaults to "tenant_id" if empty.
	TenantClaim string
}

// NewJWTAuth creates a JWT validator. It fetches the JWKS immediately if a URL
// is configured, and starts a background refresh goroutine.
func NewJWTAuth(ctx context.Context, cfg JWTAuthConfig) (*JWTAuth, error) {
	if cfg.JWKSURL == "" && cfg.StaticKey == nil {
		return nil, fmt.Errorf("jwt auth: JWKS URL or static key required")
	}
	if cfg.Audience == "" {
		return nil, fmt.Errorf("jwt auth: audience required")
	}
	tenantClaim := cfg.TenantClaim
	if tenantClaim == "" {
		tenantClaim = "tenant_id"
	}

	auth := &JWTAuth{
		audience:    cfg.Audience,
		tenantClaim: tenantClaim,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}

	if cfg.StaticKey != nil {
		auth.keySet = jose.JSONWebKeySet{
			Keys: []jose.JSONWebKey{{Key: cfg.StaticKey, Use: "sig"}},
		}
		return auth, nil
	}

	// JWKS URL mode: fetch immediately, then refresh periodically.
	auth.jwksURL = cfg.JWKSURL
	if err := auth.refreshJWKS(ctx); err != nil {
		return nil, fmt.Errorf("jwt auth: initial JWKS fetch: %w", err)
	}
	go auth.refreshLoop(ctx)

	return auth, nil
}

// Validate implements AuthValidator. It extracts and verifies the Bearer token.
func (j *JWTAuth) Validate(r *http.Request) (uuid.UUID, string, error) {
	raw := extractBearer(r)
	if raw == "" {
		return uuid.Nil, "", fmt.Errorf("missing Bearer token")
	}

	tok, err := jwt.ParseSigned(raw, supportedAlgorithms())
	if err != nil {
		return uuid.Nil, "", fmt.Errorf("invalid JWT: %w", err)
	}

	j.mu.RLock()
	keys := j.keySet
	j.mu.RUnlock()

	// Try verification against each key (go-jose matches by kid header).
	var claims jwt.Claims
	var extra map[string]json.RawMessage
	verified := false
	for _, key := range keys.Keys {
		if err := tok.Claims(key.Key, &claims, &extra); err == nil {
			verified = true
			break
		}
	}
	if !verified {
		return uuid.Nil, "", fmt.Errorf("JWT signature verification failed")
	}

	// Validate standard claims (exp, nbf, aud).
	expected := jwt.Expected{
		Time:        time.Now(),
		AnyAudience: jwt.Audience{j.audience},
	}
	if err := claims.Validate(expected); err != nil {
		return uuid.Nil, "", fmt.Errorf("JWT claims invalid: %w", err)
	}

	// Extract tenant from configurable claim.
	tenantID, err := j.extractTenant(extra)
	if err != nil {
		return uuid.Nil, "", err
	}

	return tenantID, claims.Subject, nil
}

func (j *JWTAuth) extractTenant(extra map[string]json.RawMessage) (uuid.UUID, error) {
	raw, ok := extra[j.tenantClaim]
	if !ok {
		return uuid.Nil, fmt.Errorf("JWT missing tenant claim %q", j.tenantClaim)
	}
	var tid string
	if err := json.Unmarshal(raw, &tid); err != nil {
		return uuid.Nil, fmt.Errorf("JWT tenant claim %q is not a string", j.tenantClaim)
	}
	parsed, err := uuid.Parse(tid)
	if err != nil {
		return uuid.Nil, fmt.Errorf("JWT tenant claim %q is not a valid UUID: %w", j.tenantClaim, err)
	}
	return parsed, nil
}

func (j *JWTAuth) refreshJWKS(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.jwksURL, nil)
	if err != nil {
		return err
	}
	resp, err := j.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("JWKS fetch returned %d", resp.StatusCode)
	}
	var keySet jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&keySet); err != nil {
		return fmt.Errorf("JWKS decode: %w", err)
	}

	j.mu.Lock()
	j.keySet = keySet
	j.mu.Unlock()
	return nil
}

func (j *JWTAuth) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := j.refreshJWKS(ctx); err != nil {
				slog.Warn("JWKS refresh failed", "url", j.jwksURL, "error", err)
			}
		}
	}
}

func extractBearer(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if len(auth) > 7 && strings.EqualFold(auth[:7], "Bearer ") {
		return auth[7:]
	}
	return ""
}

func supportedAlgorithms() []jose.SignatureAlgorithm {
	return []jose.SignatureAlgorithm{
		jose.RS256, jose.RS384, jose.RS512,
		jose.ES256, jose.ES384, jose.ES512,
		jose.PS256, jose.PS384, jose.PS512,
		jose.EdDSA,
	}
}
