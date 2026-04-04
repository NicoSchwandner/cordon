package ports

import "context"

// BuildResult holds the output of a devcontainer image build.
type BuildResult struct {
	ImageName         string
	PostCreateCommand []string
	RemoteUser        string
	WorkspaceFolder   string
	Env               map[string]string
	Cached            bool
}

// ImageBuilder builds OCI images from repository devcontainer configurations.
type ImageBuilder interface {
	Build(ctx context.Context, repo, branch, token, devcontainerPath string) (*BuildResult, error)
}
