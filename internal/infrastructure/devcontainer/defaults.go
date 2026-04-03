package devcontainer

import (
	"os"
	"path/filepath"
)

// RepoType detected from repository contents.
type RepoType string

const (
	RepoTypeDotnet  RepoType = "dotnet"
	RepoTypeNode    RepoType = "node"
	RepoTypeGo      RepoType = "go"
	RepoTypeUnknown RepoType = "unknown"
)

// DetectRepoType looks at files in the workspace root to guess the project type.
func DetectRepoType(workspaceDir string) RepoType {
	checks := []struct {
		pattern  string
		repoType RepoType
	}{
		{"*.sln", RepoTypeDotnet},
		{"*.csproj", RepoTypeDotnet},
		{"package.json", RepoTypeNode},
		{"go.mod", RepoTypeGo},
	}
	for _, c := range checks {
		matches, _ := filepath.Glob(filepath.Join(workspaceDir, c.pattern))
		if len(matches) > 0 {
			return c.repoType
		}
	}
	return RepoTypeUnknown
}

// GenerateDefault creates a devcontainer.json in the workspace's .devcontainer/ directory.
// Returns the path to the generated file, or empty string if generation was skipped.
func GenerateDefault(workspaceDir string) (string, error) {
	repoType := DetectRepoType(workspaceDir)
	template := defaultTemplates[repoType]
	if template == "" {
		template = defaultTemplates[RepoTypeUnknown]
	}

	dir := filepath.Join(workspaceDir, ".devcontainer")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}

	path := filepath.Join(dir, "devcontainer.json")
	if err := os.WriteFile(path, []byte(template), 0644); err != nil {
		return "", err
	}
	return path, nil
}

var defaultTemplates = map[RepoType]string{
	RepoTypeDotnet: `{
	"name": "Cordon .NET Workspace",
	"image": "mcr.microsoft.com/devcontainers/dotnet:8.0",
	"features": {
		"ghcr.io/devcontainers/features/dotnet:2": {
			"additionalVersions": "9.0,10.0"
		},
		"ghcr.io/devcontainers/features/github-cli:1": {},
		"ghcr.io/devcontainers/features/git:1": {}
	},
	"postCreateCommand": "dotnet tool install --global dotnet-ef && dotnet restore",
	"remoteEnv": {
		"DOTNET_CLI_TELEMETRY_OPTOUT": "1",
		"PATH": "${containerEnv:PATH}:${containerEnv:HOME}/.dotnet/tools"
	}
}`,

	RepoTypeNode: `{
	"name": "Cordon Node Workspace",
	"image": "mcr.microsoft.com/devcontainers/typescript-node:20",
	"features": {
		"ghcr.io/devcontainers/features/github-cli:1": {},
		"ghcr.io/devcontainers/features/git:1": {}
	},
	"postCreateCommand": "npm install"
}`,

	RepoTypeGo: `{
	"name": "Cordon Go Workspace",
	"image": "mcr.microsoft.com/devcontainers/go:1.22",
	"features": {
		"ghcr.io/devcontainers/features/github-cli:1": {},
		"ghcr.io/devcontainers/features/git:1": {}
	},
	"postCreateCommand": "go mod download"
}`,

	RepoTypeUnknown: `{
	"name": "Cordon Workspace",
	"image": "mcr.microsoft.com/devcontainers/base:ubuntu",
	"features": {
		"ghcr.io/devcontainers/features/github-cli:1": {},
		"ghcr.io/devcontainers/features/git:1": {}
	}
}`,
}
