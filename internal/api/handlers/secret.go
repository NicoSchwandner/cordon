package handlers

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/nicobistolfi/cordon/internal/api/middleware"
	"github.com/nicobistolfi/cordon/internal/application/ports"
	"github.com/nicobistolfi/cordon/internal/domain"
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
		resp[i] = SecretRefResponse{Name: ref.Name, Placeholder: ref.Placeholder}
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
	json.NewEncoder(w).Encode(SecretRefResponse{Name: req.Name, Placeholder: req.Placeholder})
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
