package handlers

import (
	"encoding/json"
	"net/http"

	gh "github.com/NicoSchwandner/cordon/internal/infrastructure/github"
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

// UserOrgs returns organizations the authenticated user belongs to, plus
// the user's own login as a pseudo-org for personal repos.
func (h *GitHubHandler) UserOrgs(w http.ResponseWriter, r *http.Request) {
	if h.client == nil || !h.client.HasToken() {
		http.Error(w, "GitHub token not configured", http.StatusServiceUnavailable)
		return
	}

	username, err := h.client.FetchUsername()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	orgs, err := h.client.ListUserOrgs()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}

	type orgEntry struct {
		Login       string `json:"login"`
		Description string `json:"description"`
		Personal    bool   `json:"personal"`
	}

	result := make([]orgEntry, 0, len(orgs)+1)
	result = append(result, orgEntry{Login: username, Personal: true})
	for _, o := range orgs {
		result = append(result, orgEntry{Login: o.Login, Description: o.Description})
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
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
