package ports

// GitIdentity represents a git user identity.
type GitIdentity struct {
	Name  string
	Email string
}

// OrgRepo represents a repository within a GitHub organization.
type OrgRepo struct {
	Name     string
	FullName string
	Archived bool
}

// GitHostClient provides operations against a git hosting platform (GitHub, etc.).
type GitHostClient interface {
	HasToken() bool
	ResolveDefaultBranch(repoURL string) string
	FetchUser() (*GitIdentity, error)
	ListOrgRepos(org string) ([]OrgRepo, error)
}
