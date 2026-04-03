package devcontainer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Config represents the fields we extract from a devcontainer.json.
type Config struct {
	Image             string            `json:"image"`
	Build             *BuildConfig      `json:"build"`
	PostCreateCommand json.RawMessage   `json:"postCreateCommand"`
	RemoteUser        string            `json:"remoteUser"`
	WorkspaceFolder   string            `json:"workspaceFolder"`
	RemoteEnv         map[string]string `json:"remoteEnv"`
	ContainerEnv      map[string]string `json:"containerEnv"`
	Features          json.RawMessage   `json:"features"`
}

// BuildConfig represents the build section of devcontainer.json.
type BuildConfig struct {
	Dockerfile string            `json:"dockerfile"`
	Context    string            `json:"context"`
	Args       map[string]string `json:"args"`
}

// PostCreateCommands returns the postCreateCommand normalized to a string slice.
// devcontainer.json allows string, []string, or map[string]string.
func (c *Config) PostCreateCommands() []string {
	if c.PostCreateCommand == nil {
		return nil
	}

	// Try string
	var s string
	if json.Unmarshal(c.PostCreateCommand, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}

	// Try []string
	var arr []string
	if json.Unmarshal(c.PostCreateCommand, &arr) == nil {
		return arr
	}

	// Try map[string]string (parallel commands — run sequentially for simplicity)
	var m map[string]string
	if json.Unmarshal(c.PostCreateCommand, &m) == nil {
		cmds := make([]string, 0, len(m))
		for _, cmd := range m {
			cmds = append(cmds, cmd)
		}
		return cmds
	}

	return nil
}

// ParseFile reads and parses a devcontainer.json file (supports JSONC with comments).
func ParseFile(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading devcontainer.json: %w", err)
	}
	return Parse(data)
}

// Parse parses devcontainer.json content (supports JSONC with comments).
func Parse(data []byte) (*Config, error) {
	cleaned := stripJSONC(string(data))
	var cfg Config
	if err := json.Unmarshal([]byte(cleaned), &cfg); err != nil {
		return nil, fmt.Errorf("parsing devcontainer.json: %w", err)
	}
	if cfg.RemoteUser == "" {
		cfg.RemoteUser = "vscode"
	}
	if cfg.WorkspaceFolder == "" {
		cfg.WorkspaceFolder = "/workspace"
	}
	return &cfg, nil
}

// FindConfig locates devcontainer.json in a workspace directory.
// Checks: .devcontainer/devcontainer.json, .devcontainer.json, then custom path.
func FindConfig(workspaceDir, customPath string) string {
	if customPath != "" {
		p := filepath.Join(workspaceDir, customPath)
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	candidates := []string{
		filepath.Join(workspaceDir, ".devcontainer", "devcontainer.json"),
		filepath.Join(workspaceDir, ".devcontainer.json"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	return ""
}

// stripJSONC removes single-line (//) and multi-line (/* */) comments from JSONC.
// Respects string literals — comments inside strings are preserved.
func stripJSONC(input string) string {
	var out strings.Builder
	out.Grow(len(input))

	i := 0
	for i < len(input) {
		// String literal — pass through unchanged
		if input[i] == '"' {
			out.WriteByte('"')
			i++
			for i < len(input) {
				out.WriteByte(input[i])
				if input[i] == '\\' {
					i++
					if i < len(input) {
						out.WriteByte(input[i])
					}
				} else if input[i] == '"' {
					break
				}
				i++
			}
			i++
			continue
		}

		// Single-line comment
		if i+1 < len(input) && input[i] == '/' && input[i+1] == '/' {
			for i < len(input) && input[i] != '\n' {
				i++
			}
			continue
		}

		// Multi-line comment
		if i+1 < len(input) && input[i] == '/' && input[i+1] == '*' {
			i += 2
			for i+1 < len(input) {
				if input[i] == '*' && input[i+1] == '/' {
					i += 2
					break
				}
				i++
			}
			continue
		}

		out.WriteByte(input[i])
		i++
	}

	return out.String()
}
