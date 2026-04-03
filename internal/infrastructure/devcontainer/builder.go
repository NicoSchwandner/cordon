package devcontainer

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
)

// Builder orchestrates devcontainer image builds from repository sources.
type Builder struct {
	cliPath string // path to devcontainer CLI binary
}

// BuildResult contains everything needed to create a container from the built image.
type BuildResult struct {
	ImageName         string
	PostCreateCommand []string
	RemoteUser        string
	WorkspaceFolder   string
	Env               map[string]string
	Cached            bool // true if an existing image was reused
}

// NewBuilder creates a Builder, verifying that the devcontainer CLI is available.
func NewBuilder() (*Builder, error) {
	path, err := exec.LookPath("devcontainer")
	if err != nil {
		return nil, fmt.Errorf("devcontainer CLI not found in PATH: %w", err)
	}
	log.Printf("[devcontainer] using CLI at %s", path)
	return &Builder{cliPath: path}, nil
}

// Build clones a repo, builds the devcontainer image, and returns metadata.
func (b *Builder) Build(ctx context.Context, repo, branch, token, devcontainerPath string) (*BuildResult, error) {
	// Create temp directory for the clone
	tmpDir, err := os.MkdirTemp("", "cordon-build-*")
	if err != nil {
		return nil, fmt.Errorf("creating temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone the repo
	cloneURL := injectToken(repo, token)
	log.Printf("[devcontainer] cloning %s (branch=%s)", repo, branch)

	args := []string{"clone", "--depth", "1"}
	if branch != "" {
		args = append(args, "--branch", branch)
	}
	args = append(args, cloneURL, tmpDir)

	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git clone failed: %w\n%s", err, out)
	}

	// Find or generate devcontainer.json
	configPath := FindConfig(tmpDir, devcontainerPath)
	if configPath == "" {
		log.Printf("[devcontainer] no devcontainer.json found, generating default")
		generated, err := GenerateDefault(tmpDir)
		if err != nil {
			return nil, fmt.Errorf("generating default devcontainer.json: %w", err)
		}
		configPath = generated
	}

	// Read raw config content for cache-busting hash
	configContent, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("reading devcontainer config: %w", err)
	}

	// Parse the config
	cfg, err := Parse(configContent)
	if err != nil {
		return nil, fmt.Errorf("parsing devcontainer config: %w", err)
	}

	// Build the image (or reuse cached)
	imageTag := fmt.Sprintf("cordon-ws-%s", imageHash(repo, branch, configContent))
	cached := imageExists(ctx, imageTag)
	if cached {
		log.Printf("[devcontainer] using cached image %s", imageTag)
	} else {
		log.Printf("[devcontainer] building image %s", imageTag)
		buildCmd := exec.CommandContext(ctx, b.cliPath, "build",
			"--workspace-folder", tmpDir,
			"--image-name", imageTag,
		)
		buildCmd.Env = os.Environ()
		if out, err := buildCmd.CombinedOutput(); err != nil {
			return nil, fmt.Errorf("devcontainer build failed: %w\n%s", err, out)
		}
		log.Printf("[devcontainer] image %s built successfully", imageTag)
	}

	// Merge env maps
	env := make(map[string]string)
	for k, v := range cfg.ContainerEnv {
		env[k] = v
	}
	for k, v := range cfg.RemoteEnv {
		env[k] = v
	}

	return &BuildResult{
		ImageName:         imageTag,
		PostCreateCommand: cfg.PostCreateCommands(),
		RemoteUser:        cfg.RemoteUser,
		WorkspaceFolder:   cfg.WorkspaceFolder,
		Env:               env,
		Cached:            cached,
	}, nil
}

// injectToken adds a GitHub token to an HTTPS git URL for authenticated cloning.
func injectToken(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	// Handle github.com/org/repo shorthand
	if !strings.Contains(repoURL, "://") {
		repoURL = "https://" + repoURL
	}
	u, err := url.Parse(repoURL)
	if err != nil {
		return repoURL
	}
	if !strings.HasSuffix(u.Path, ".git") {
		u.Path += ".git"
	}
	u.User = url.UserPassword("x-access-token", token)
	return u.String()
}

// imageExists checks if a Docker image with the given tag exists locally.
func imageExists(ctx context.Context, tag string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", tag)
	return cmd.Run() == nil
}

// imageHash creates a short deterministic hash for image tagging.
// Includes config content so devcontainer.json changes invalidate the cache.
func imageHash(repo, branch string, configContent []byte) string {
	h := sha256.New()
	h.Write([]byte(repo + ":" + branch + ":"))
	h.Write(configContent)
	return fmt.Sprintf("%x", h.Sum(nil)[:6])
}
