package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/NicoSchwandner/cordon/internal/api/middleware"
	"github.com/NicoSchwandner/cordon/internal/application/ports"
	"github.com/NicoSchwandner/cordon/internal/domain"
)

type SecretHandler struct {
	vault ports.SecretVault
}

func NewSecretHandler(vault ports.SecretVault) *SecretHandler {
	return &SecretHandler{vault: vault}
}

type SecretRefResponse struct {
	Name        string `json:"name"`
	Placeholder string `json:"placeholder"`
	MaskedValue string `json:"masked_value"`
}

type SetSecretRequest struct {
	Name        string `json:"name"`
	Placeholder string `json:"placeholder"`
	Value       string `json:"value"`
}

func (h *SecretHandler) List(w http.ResponseWriter, r *http.Request) {
	tenantID := middleware.TenantIDFromContext(r.Context())
	refs, err := h.vault.ListRefs(r.Context(), tenantID)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/secret-list-failed", Title: "List Failed",
			Status: 500, Detail: err.Error(), Code: "secret_list_failed",
		})
		return
	}

	resp := make([]SecretRefResponse, len(refs))
	for i, ref := range refs {
		masked := ""
		if val, err := h.vault.RevealValue(r.Context(), tenantID, ref.Name); err == nil {
			masked = maskSecret(val)
		}
		resp[i] = SecretRefResponse{Name: ref.Name, Placeholder: ref.Placeholder, MaskedValue: masked}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func (h *SecretHandler) Set(w http.ResponseWriter, r *http.Request) {
	var req SetSecretRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	if req.Name == "" || req.Value == "" {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/validation-error", Title: "Validation Error",
			Status: 400, Detail: "name and value are required", Code: "validation_error",
		})
		return
	}

	if req.Placeholder == "" {
		req.Placeholder = "cordon-placeholder-" + strings.ToLower(strings.ReplaceAll(req.Name, "_", "-"))
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	if err := h.vault.SetSecret(r.Context(), tenantID, req.Name, req.Placeholder, req.Value); err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/secret-set-failed", Title: "Set Failed",
			Status: 500, Detail: err.Error(), Code: "secret_set_failed",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(SecretRefResponse{Name: req.Name, Placeholder: req.Placeholder, MaskedValue: maskSecret(req.Value)})
}

func maskSecret(value string) string {
	if len(value) <= 8 {
		return strings.Repeat("•", len(value))
	}
	return value[:4] + strings.Repeat("•", min(len(value)-8, 16)) + value[len(value)-4:]
}

func (h *SecretHandler) Reveal(w http.ResponseWriter, r *http.Request) {
	// /api/secrets/{name}/value
	path := strings.TrimPrefix(r.URL.Path, "/api/secrets/")
	name := strings.TrimSuffix(path, "/value")
	if name == "" {
		http.Error(w, "missing secret name", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	value, err := h.vault.RevealValue(r.Context(), tenantID, name)
	if err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/secret-not-found", Title: "Not Found",
			Status: 404, Detail: err.Error(), Code: "secret_not_found",
		})
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"value": value})
}

func (h *SecretHandler) Delete(w http.ResponseWriter, r *http.Request) {
	// /api/secrets/{name}
	name := strings.TrimPrefix(r.URL.Path, "/api/secrets/")
	if name == "" {
		http.Error(w, "missing secret name", http.StatusBadRequest)
		return
	}

	tenantID := middleware.TenantIDFromContext(r.Context())
	if err := h.vault.DeleteSecret(r.Context(), tenantID, name); err != nil {
		middleware.WriteProblem(w, domain.ProblemDetails{
			Type: "https://cordon.dev/problems/secret-not-found", Title: "Not Found",
			Status: 404, Detail: err.Error(), Code: "secret_not_found",
		})
		return
	}

	w.WriteHeader(http.StatusNoContent)
}
