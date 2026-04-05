package proxy

import (
	"context"
	"encoding/base64"
	"strings"

	"github.com/google/uuid"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
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

	seen := make(map[string]bool)
	modified.Body = []byte(replaceSecrets(string(req.Body), replacements, refs, seen))
	modified.SQLQuery = replaceSecrets(req.SQLQuery, replacements, refs, seen)
	modified.Path = replaceSecrets(req.Path, replacements, refs, seen)
	for k, v := range modified.Headers {
		if strings.EqualFold(k, "Authorization") {
			modified.Headers[k] = replaceSecretsInAuth(v, replacements, refs, seen)
		} else {
			modified.Headers[k] = replaceSecrets(v, replacements, refs, seen)
		}
	}

	for name := range seen {
		swapped = append(swapped, name)
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

// replaceSecretsInAuth handles Authorization headers where credentials may be
// base64-encoded (e.g. "Basic base64(user:placeholder)"). It first tries plain
// replacement, then decodes Basic auth, swaps inside the decoded value, and
// re-encodes.
func replaceSecretsInAuth(val string, replacements map[string]string, refs []domain.SecretRef, seen map[string]bool) string {
	// Try plain replacement first (covers Bearer tokens etc.)
	result := replaceSecrets(val, replacements, refs, seen)
	if result != val {
		return result
	}

	// Decode Basic auth and try replacement inside the decoded value
	if after, found := strings.CutPrefix(val, "Basic "); found {
		decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(after))
		if err != nil {
			return val
		}
		swapped := replaceSecrets(string(decoded), replacements, refs, seen)
		if swapped != string(decoded) {
			return "Basic " + base64.StdEncoding.EncodeToString([]byte(swapped))
		}
	}
	return val
}

func replaceSecrets(s string, replacements map[string]string, refs []domain.SecretRef, seen map[string]bool) string {
	for placeholder, real := range replacements {
		if strings.Contains(s, placeholder) {
			s = strings.ReplaceAll(s, placeholder, real)
			seen[placeholderToName(refs, placeholder)] = true
		}
	}
	return s
}
