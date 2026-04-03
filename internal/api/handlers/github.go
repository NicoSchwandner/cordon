package handlers

import (
	"encoding/json"
	"net/http"

	gh "github.com/nicobistolfi/cordon/internal/infrastructure/github"
)

// GitHubHandler proxies GitHub API queries for the frontend.
type GitHubHandler struct {
	client *gh.Client
}

func NewGitHubHandler(client *gh.Client) *GitHubHandler {
	return &GitHubHandler{client: client}
}

func (h *GitHubHandler) DefaultBranch(w http.ResponseWriter, r *http.Request) {
	repo := r.URL.Query().Get("repo")
	if repo == "" {
		http.Error(w, "repo query parameter required", http.StatusBadRequest)
		return
	}

	branch := "main"
	if h.client != nil {
		branch = h.client.ResolveDefaultBranch(repo)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"default_branch": branch})
}

// OrgRepos returns the list of repositories for a GitHub organization.
func (h *GitHubHandler) OrgRepos(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("org")
	if org == "" {
		http.Error(w, "org path parameter required", http.StatusBadRequest)
		return
	}

	if h.client == nil || !h.client.HasToken() {
		http.Error(w, "GitHub token not configured", http.StatusServiceUnavailable)
		return
	}

	repos, err := h.client.ListOrgRepos(org)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(repos)
}

// RepoBranches returns the list of branches for a GitHub repository.
func (h *GitHubHandler) RepoBranches(w http.ResponseWriter, r *http.Request) {
	owner := r.PathValue("owner")
	repo := r.PathValue("repo")
	if owner == "" || repo == "" {
		http.Error(w, "owner and repo path parameters required", http.StatusBadRequest)
		return
	}

	if h.client == nil || !h.client.HasToken() {
		http.Error(w, "GitHub token not configured", http.StatusServiceUnavailable)
		return
	}

	branches, err := h.client.ListBranches(owner, repo)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(branches)
}
