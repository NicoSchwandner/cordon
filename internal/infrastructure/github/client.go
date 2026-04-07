package github

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client provides cached access to the GitHub API.
type Client struct {
	token         string
	http          *http.Client
	repoListTTL   time.Duration
	branchListTTL time.Duration

	// Caches
	defaultBranches sync.Map // "owner/repo" → string
	orgRepos        sync.Map // org → *cachedRepoList
	repoBranches    sync.Map // "owner/repo" → *cachedBranchList
}

type cachedRepoList struct {
	repos     []OrgRepo
	etag      string
	fetchedAt time.Time
}

type cachedBranchList struct {
	branches  []Branch
	etag      string
	fetchedAt time.Time
}

// OrgRepo represents a repository in a GitHub organization.
type OrgRepo struct {
	Name          string `json:"name"`
	FullName      string `json:"full_name"`
	DefaultBranch string `json:"default_branch"`
	Description   string `json:"description"`
	Language      string `json:"language"`
	Archived      bool   `json:"archived"`
	Private       bool   `json:"private"`
	UpdatedAt     string `json:"updated_at"`
	HTMLURL       string `json:"html_url"`
	CloneURL      string `json:"clone_url"`
}

// Branch represents a Git branch.
type Branch struct {
	Name string `json:"name"`
}

// GitIdentity holds the git user.name and user.email for configuring containers.
type GitIdentity struct {
	Name  string
	Email string
}

// ClientConfig holds configurable values for the GitHub API client.
type ClientConfig struct {
	RepoListTTL   time.Duration
	BranchListTTL time.Duration
	HTTPTimeout   time.Duration
}

// NewClient creates a GitHub API client. Token may be empty (public repos only).
func NewClient(token string, cfg ClientConfig) *Client {
	return &Client{
		token:         token,
		repoListTTL:   cfg.RepoListTTL,
		branchListTTL: cfg.BranchListTTL,
		http:          &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

// Token returns whether a token is configured.
func (c *Client) HasToken() bool {
	return c.token != ""
}

// ResolveDefaultBranch returns the default branch for a repo, with caching.
// Falls back to "main" on any error.
func (c *Client) ResolveDefaultBranch(repoURL string) string {
	owner, repo, err := ParseOwnerRepo(repoURL)
	if err != nil {
		return "main"
	}
	key := owner + "/" + repo

	if cached, ok := c.defaultBranches.Load(key); ok {
		return cached.(string)
	}

	branch := "main"
	if c.token != "" {
		if b, err := c.fetchDefaultBranch(owner, repo); err == nil {
			branch = b
		}
	}

	c.defaultBranches.Store(key, branch)
	return branch
}

// UserOrg represents a GitHub organization the authenticated user belongs to.
type UserOrg struct {
	Login       string `json:"login"`
	Description string `json:"description"`
	AvatarURL   string `json:"avatar_url"`
}

// FetchUser queries the GitHub API for the authenticated user's identity.
func (c *Client) FetchUser() (*GitIdentity, error) {
	if c.token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}

	var user struct {
		Name  string `json:"name"`
		Login string `json:"login"`
		Email string `json:"email"`
	}
	if err := c.get("https://api.github.com/user", "", &user, nil); err != nil {
		return nil, err
	}

	name := user.Name
	if name == "" {
		name = user.Login
	}
	email := user.Email
	if email == "" {
		email = user.Login + "@users.noreply.github.com"
	}

	return &GitIdentity{Name: name, Email: email}, nil
}

// FetchUsername returns the authenticated user's login name.
func (c *Client) FetchUsername() (string, error) {
	if c.token == "" {
		return "", fmt.Errorf("no GitHub token configured")
	}
	var user struct {
		Login string `json:"login"`
	}
	if err := c.get("https://api.github.com/user", "", &user, nil); err != nil {
		return "", err
	}
	return user.Login, nil
}

// ListUserOrgs returns organizations the authenticated user belongs to.
func (c *Client) ListUserOrgs() ([]UserOrg, error) {
	if c.token == "" {
		return nil, fmt.Errorf("no GitHub token configured")
	}

	var all []UserOrg
	page := 1
	for {
		url := fmt.Sprintf("https://api.github.com/user/orgs?per_page=100&page=%d", page)
		var orgs []UserOrg
		if err := c.get(url, "", &orgs, nil); err != nil {
			return nil, fmt.Errorf("listing user orgs (page %d): %w", page, err)
		}
		all = append(all, orgs...)
		if len(orgs) < 100 {
			break
		}
		page++
	}
	return all, nil
}

// ListOrgRepos returns all repositories for a GitHub organization, with caching.
func (c *Client) ListOrgRepos(org string) ([]OrgRepo, error) {
	if cached, ok := c.orgRepos.Load(org); ok {
		entry := cached.(*cachedRepoList)
		if time.Since(entry.fetchedAt) < c.repoListTTL {
			return entry.repos, nil
		}
	}

	repos, etag, err := c.fetchOrgRepos(org)
	if err != nil {
		// Return stale cache on error
		if cached, ok := c.orgRepos.Load(org); ok {
			return cached.(*cachedRepoList).repos, nil
		}
		return nil, err
	}

	c.orgRepos.Store(org, &cachedRepoList{
		repos:     repos,
		etag:      etag,
		fetchedAt: time.Now(),
	})
	return repos, nil
}

// ListBranches returns branches for a repository, with caching.
func (c *Client) ListBranches(owner, repo string) ([]Branch, error) {
	key := owner + "/" + repo

	if cached, ok := c.repoBranches.Load(key); ok {
		entry := cached.(*cachedBranchList)
		if time.Since(entry.fetchedAt) < c.branchListTTL {
			return entry.branches, nil
		}
	}

	branches, etag, err := c.fetchBranches(owner, repo)
	if err != nil {
		// Return stale cache on error
		if cached, ok := c.repoBranches.Load(key); ok {
			return cached.(*cachedBranchList).branches, nil
		}
		return nil, err
	}

	c.repoBranches.Store(key, &cachedBranchList{
		branches:  branches,
		etag:      etag,
		fetchedAt: time.Now(),
	})
	return branches, nil
}

// fetchOrgRepos fetches all repos from a GitHub org with pagination.
func (c *Client) fetchOrgRepos(org string) ([]OrgRepo, string, error) {
	var all []OrgRepo
	var lastEtag string
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/orgs/%s/repos?per_page=100&sort=updated&page=%d", org, page)

		var repos []OrgRepo
		var etag string
		if err := c.get(url, "", &repos, &etag); err != nil {
			return nil, "", fmt.Errorf("listing org repos (page %d): %w", page, err)
		}
		if page == 1 {
			lastEtag = etag
		}

		all = append(all, repos...)
		if len(repos) < 100 {
			break
		}
		page++
	}

	return all, lastEtag, nil
}

// fetchBranches fetches all branches for a repo with pagination.
func (c *Client) fetchBranches(owner, repo string) ([]Branch, string, error) {
	var all []Branch
	var lastEtag string
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/%s/branches?per_page=100&page=%d", owner, repo, page)

		var branches []Branch
		var etag string
		if err := c.get(url, "", &branches, &etag); err != nil {
			return nil, "", fmt.Errorf("listing branches (page %d): %w", page, err)
		}
		if page == 1 {
			lastEtag = etag
		}

		all = append(all, branches...)
		if len(branches) < 100 {
			break
		}
		page++
	}

	return all, lastEtag, nil
}

func (c *Client) fetchDefaultBranch(owner, repo string) (string, error) {
	var result struct {
		DefaultBranch string `json:"default_branch"`
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/%s", owner, repo)
	if err := c.get(url, "", &result, nil); err != nil {
		return "", err
	}
	if result.DefaultBranch == "" {
		return "main", nil
	}
	return result.DefaultBranch, nil
}

// get performs an authenticated GET request with optional ETag support.
func (c *Client) get(url, ifNoneMatch string, target interface{}, etagOut *string) error {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if etagOut != nil {
		*etagOut = resp.Header.Get("ETag")
	}

	if resp.StatusCode == 304 {
		return nil // not modified, cache still valid
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API returned %d for %s", resp.StatusCode, url)
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

// ParseOwnerRepo extracts owner and repo from a GitHub URL.
// Handles: "github.com/org/repo", "https://github.com/org/repo.git", etc.
func ParseOwnerRepo(repoURL string) (string, string, error) {
	raw := repoURL
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", err
	}
	if !strings.Contains(u.Host, "github.com") {
		return "", "", fmt.Errorf("not a GitHub URL: %s", repoURL)
	}
	path := strings.Trim(u.Path, "/")
	path = strings.TrimSuffix(path, ".git")
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("cannot parse owner/repo from %s", repoURL)
	}
	return parts[0], parts[1], nil
}
