package proxy

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
)

// SecretSwapper replaces placeholder tokens with real credentials.
type SecretSwapper struct {
	vault ports.SecretVault
}

func NewSecretSwapper(vault ports.SecretVault) *SecretSwapper {
	return &SecretSwapper{vault: vault}
}

// Swap replaces all placeholder tokens in the request with real values.
// Returns the modified request and the list of secret names that were swapped.
func (s *SecretSwapper) Swap(ctx context.Context, tenantID uuid.UUID, req *ProxyRequest) (*ProxyRequest, []string, error) {
	refs, err := s.vault.ListRefs(ctx, tenantID)
	if err != nil {
		return nil, nil, err
	}

	if len(refs) == 0 {
		return req, nil, nil
	}

	// Build placeholder→real map
	replacements := make(map[string]string)
	var swapped []string
	for _, ref := range refs {
		real, err := s.vault.Resolve(ctx, tenantID, ref.Placeholder)
		if err != nil {
			continue // skip unresolvable secrets
		}
		replacements[ref.Placeholder] = real
	}

	modified := *req
	modified.Headers = make(map[string]string, len(req.Headers))
	for k, v := range req.Headers {
		modified.Headers[k] = v
	}

	// Replace in body
	bodyStr := string(req.Body)
	for placeholder, real := range replacements {
		if strings.Contains(bodyStr, placeholder) {
			bodyStr = strings.ReplaceAll(bodyStr, placeholder, real)
			swapped = append(swapped, placeholderToName(refs, placeholder))
		}
	}
	modified.Body = []byte(bodyStr)

	// Replace in SQL query
	sqlStr := req.SQLQuery
	for placeholder, real := range replacements {
		if strings.Contains(sqlStr, placeholder) {
			sqlStr = strings.ReplaceAll(sqlStr, placeholder, real)
			if !contains(swapped, placeholderToName(refs, placeholder)) {
				swapped = append(swapped, placeholderToName(refs, placeholder))
			}
		}
	}
	modified.SQLQuery = sqlStr

	// Replace in path
	pathStr := req.Path
	for placeholder, real := range replacements {
		if strings.Contains(pathStr, placeholder) {
			pathStr = strings.ReplaceAll(pathStr, placeholder, real)
			if !contains(swapped, placeholderToName(refs, placeholder)) {
				swapped = append(swapped, placeholderToName(refs, placeholder))
			}
		}
	}
	modified.Path = pathStr

	// Replace in headers
	for k, v := range modified.Headers {
		for placeholder, real := range replacements {
			if strings.Contains(v, placeholder) {
				modified.Headers[k] = strings.ReplaceAll(v, placeholder, real)
				if !contains(swapped, placeholderToName(refs, placeholder)) {
					swapped = append(swapped, placeholderToName(refs, placeholder))
				}
			}
		}
	}

	return &modified, swapped, nil
}

// RedactSecrets removes real credential values from a string for audit logging.
func (s *SecretSwapper) RedactSecrets(ctx context.Context, tenantID uuid.UUID, text string) (string, error) {
	refs, err := s.vault.ListRefs(ctx, tenantID)
	if err != nil {
		return text, err
	}
	for _, ref := range refs {
		real, err := s.vault.Resolve(ctx, tenantID, ref.Placeholder)
		if err != nil {
			continue
		}
		text = strings.ReplaceAll(text, real, "[REDACTED:"+ref.Name+"]")
	}
	return text, nil
}

func placeholderToName(refs []domain.SecretRef, placeholder string) string {
	for _, r := range refs {
		if r.Placeholder == placeholder {
			return r.Name
		}
	}
	return placeholder
}

func contains(slice []string, item string) bool {
	for _, s := range slice {
		if s == item {
			return true
		}
	}
	return false
}
