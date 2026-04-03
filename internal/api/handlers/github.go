package handlers

import (
	"encoding/json"
	"net/http"
)

// GitHubHandler proxies GitHub API queries for the frontend.
type GitHubHandler struct {
	resolveDefaultBranch func(repo string) string
}

func NewGitHubHandler(resolver func(repo string) string) *GitHubHandler {
	return &GitHubHandler{resolveDefaultBranch: resolver}
}

func (h *GitHubHandler) DefaultBranch(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		http.Error(w, "repo query parameter required", http.StatusBadRequest)
		return
	}

	branch := "main"
	if h.resolveDefaultBranch != nil {
		branch = h.resolveDefaultBranch(repo)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"default_branch": branch})
}
