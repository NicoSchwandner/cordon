package github

import "github.com/NicoSchwandner/cordon/internal/application/ports"

// GitHostAdapter wraps a github.Client to satisfy ports.GitHostClient.
type GitHostAdapter struct {
	client *Client
}

// NewGitHostAdapter creates an adapter from a concrete GitHub client.
func NewGitHostAdapter(c *Client) *GitHostAdapter {
	return &GitHostAdapter{client: c}
}

func (a *GitHostAdapter) HasToken() bool {
	return a.client.HasToken()
}

func (a *GitHostAdapter) ResolveDefaultBranch(repoURL string) string {
	return a.client.ResolveDefaultBranch(repoURL)
}

func (a *GitHostAdapter) FetchUser() (*ports.GitIdentity, error) {
	id, err := a.client.FetchUser()
	if err != nil {
		return nil, err
	}
	return &ports.GitIdentity{Name: id.Name, Email: id.Email}, nil
}

func (a *GitHostAdapter) ListOrgRepos(org string) ([]ports.OrgRepo, error) {
	repos, err := a.client.ListOrgRepos(org)
	if err != nil {
		return nil, err
	}
	result := make([]ports.OrgRepo, len(repos))
	for i, r := range repos {
		result[i] = ports.OrgRepo{
			Name:     r.Name,
			FullName: r.FullName,
			Archived: r.Archived,
		}
	}
	return result, nil
}

// Compile-time check.
var _ ports.GitHostClient = (*GitHostAdapter)(nil)
