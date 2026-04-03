package middleware

import (
	"context"
	"net/http"

	"github.com/google/uuid"
)

type contextKey string

const (
	tenantIDKey contextKey = "tenant_id"
	userIDKey   contextKey = "user_id"
)

// TenantIDFromContext returns the authenticated tenant ID.
func TenantIDFromContext(ctx context.Context) uuid.UUID {
	if id, ok := ctx.Value(tenantIDKey).(uuid.UUID); ok {
		return id
	}
	return uuid.Nil
}

// UserIDFromContext returns the authenticated user ID.
func UserIDFromContext(ctx context.Context) string {
	if id, ok := ctx.Value(userIDKey).(string); ok {
		return id
	}
	return ""
}

// AuthValidator checks authentication tokens. Implementations include Ory Kratos and mock.
type AuthValidator interface {
	Validate(r *http.Request) (tenantID uuid.UUID, userID string, err error)
}

// Auth middleware enforces authentication on all routes.
func Auth(validator AuthValidator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tenantID, userID, err := validator.Validate(r)
			if err != nil {
				http.Error(w, `{"type":"https://cordon.dev/problems/unauthorized","title":"Unauthorized","status":401,"code":"unauthorized"}`, http.StatusUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), tenantIDKey, tenantID)
			ctx = context.WithValue(ctx, userIDKey, userID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// StaticAuth is a simple validator for development/testing.
type StaticAuth struct {
	TenantID uuid.UUID
	UserID   string
}

func (s *StaticAuth) Validate(_ *http.Request) (uuid.UUID, string, error) {
	return s.TenantID, s.UserID, nil
}

// TokenAuth validates Bearer tokens against a map (for simple deployments).
type TokenAuth struct {
	Tokens map[string]TokenInfo
}

type TokenInfo struct {
	TenantID uuid.UUID
	UserID   string
}

func (t *TokenAuth) Validate(r *http.Request) (uuid.UUID, string, error) {
	token := r.Header.Get("Authorization")
	if len(token) > 7 && token[:7] == "Bearer " {
		token = token[7:]
	}
	if info, ok := t.Tokens[token]; ok {
		return info.TenantID, info.UserID, nil
	}
	return uuid.Nil, "", http.ErrNoCookie // any error triggers 401
}
