package devcontainer

import (
	"testing"
)

func TestStripJSONC(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no comments", `{"a": 1}`, `{"a": 1}`},
		{"single-line comment", "{\"a\": 1} // comment\n", "{\"a\": 1} \n"},
		{"multi-line comment", `{"a": /* comment */ 1}`, `{"a":  1}`},
		{"comment in string preserved", `{"a": "// not a comment"}`, `{"a": "// not a comment"}`},
		{"escaped quote in string", `{"a": "say \"hi\" // here"}`, `{"a": "say \"hi\" // here"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := stripJSONC(tt.input)
			if got != tt.want {
				t.Errorf("stripJSONC(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParse(t *testing.T) {
	input := `{
		// This is a comment
		"image": "mcr.microsoft.com/devcontainers/dotnet:8.0",
		"remoteUser": "developer",
		"workspaceFolder": "/src",
		"postCreateCommand": "dotnet restore",
		"remoteEnv": {
			"DOTNET_CLI_TELEMETRY_OPTOUT": "1"
		},
		"features": {
			"ghcr.io/devcontainers/features/github-cli:1": {}
		}
	}`

	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.Image != "mcr.microsoft.com/devcontainers/dotnet:8.0" {
		t.Errorf("Image = %q", cfg.Image)
	}
	if cfg.RemoteUser != "developer" {
		t.Errorf("RemoteUser = %q", cfg.RemoteUser)
	}
	if cfg.WorkspaceFolder != "/src" {
		t.Errorf("WorkspaceFolder = %q", cfg.WorkspaceFolder)
	}
	cmds := cfg.PostCreateCommands()
	if len(cmds) != 1 || cmds[0] != "dotnet restore" {
		t.Errorf("PostCreateCommands = %v", cmds)
	}
	if cfg.RemoteEnv["DOTNET_CLI_TELEMETRY_OPTOUT"] != "1" {
		t.Errorf("RemoteEnv = %v", cfg.RemoteEnv)
	}
}

func TestParseDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`{"image": "ubuntu"}`))
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	if cfg.RemoteUser != "vscode" {
		t.Errorf("default RemoteUser = %q, want vscode", cfg.RemoteUser)
	}
	if cfg.WorkspaceFolder != "/workspace" {
		t.Errorf("default WorkspaceFolder = %q, want /workspace", cfg.WorkspaceFolder)
	}
}

func TestPostCreateCommandVariants(t *testing.T) {
	// String
	cfg, _ := Parse([]byte(`{"image":"x","postCreateCommand":"echo hi"}`))
	if cmds := cfg.PostCreateCommands(); len(cmds) != 1 || cmds[0] != "echo hi" {
		t.Errorf("string variant: %v", cmds)
	}

	// Array
	cfg, _ = Parse([]byte(`{"image":"x","postCreateCommand":["echo","hi"]}`))
	if cmds := cfg.PostCreateCommands(); len(cmds) != 2 {
		t.Errorf("array variant: %v", cmds)
	}

	// Map
	cfg, _ = Parse([]byte(`{"image":"x","postCreateCommand":{"a":"echo a","b":"echo b"}}`))
	if cmds := cfg.PostCreateCommands(); len(cmds) != 2 {
		t.Errorf("map variant: %v", cmds)
	}

	// Null
	cfg, _ = Parse([]byte(`{"image":"x"}`))
	if cmds := cfg.PostCreateCommands(); cmds != nil {
		t.Errorf("nil variant: %v", cmds)
	}
}
