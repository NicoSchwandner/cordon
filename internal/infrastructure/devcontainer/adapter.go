package devcontainer

import (
	"context"

	"github.com/NicoSchwandner/cordon/internal/application/ports"
)

// ImageBuilderAdapter wraps a devcontainer.Builder to satisfy ports.ImageBuilder.
type ImageBuilderAdapter struct {
	builder *Builder
}

// NewImageBuilderAdapter creates an adapter from a concrete Builder.
func NewImageBuilderAdapter(b *Builder) *ImageBuilderAdapter {
	return &ImageBuilderAdapter{builder: b}
}

func (a *ImageBuilderAdapter) Build(ctx context.Context, repo, branch, token, devcontainerPath string) (*ports.BuildResult, error) {
	result, err := a.builder.Build(ctx, repo, branch, token, devcontainerPath)
	if err != nil {
		return nil, err
	}
	return &ports.BuildResult{
		ImageName:         result.ImageName,
		PostCreateCommand: result.PostCreateCommand,
		RemoteUser:        result.RemoteUser,
		WorkspaceFolder:   result.WorkspaceFolder,
		Env:               result.Env,
		Cached:            result.Cached,
	}, nil
}

// Compile-time check.
var _ ports.ImageBuilder = (*ImageBuilderAdapter)(nil)
