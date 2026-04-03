package domain

import "testing"

func TestRepoShortName(t *testing.T) {
	tests := []struct {
		url  string
		want string
	}{
		{"github.com/org/backend", "backend"},
		{"github.com/org/backend.git", "backend"},
		{"https://github.com/org/my-repo.git", "my-repo"},
		{"gitlab.com/group/subgroup/project", "project"},
		{"simple-name", "simple-name"},
		{"", ""},
	}

	for _, tt := range tests {
		got := RepoShortName(tt.url)
		if got != tt.want {
			t.Errorf("RepoShortName(%q) = %q, want %q", tt.url, got, tt.want)
		}
	}
}

func TestPrimaryRepo(t *testing.T) {
	t.Run("explicit primary", func(t *testing.T) {
		config := WorkspaceConfig{
			Repos: []RepoConfig{
				{URL: "github.com/org/frontend"},
				{URL: "github.com/org/backend", Primary: true},
			},
		}
		p := config.PrimaryRepo()
		if p == nil || p.URL != "github.com/org/backend" {
			t.Errorf("expected backend as primary, got %v", p)
		}
	})

	t.Run("first is default primary", func(t *testing.T) {
		config := WorkspaceConfig{
			Repos: []RepoConfig{
				{URL: "github.com/org/first"},
				{URL: "github.com/org/second"},
			},
		}
		p := config.PrimaryRepo()
		if p == nil || p.URL != "github.com/org/first" {
			t.Errorf("expected first repo as primary, got %v", p)
		}
	})

	t.Run("empty repos returns nil", func(t *testing.T) {
		config := WorkspaceConfig{}
		if p := config.PrimaryRepo(); p != nil {
			t.Errorf("expected nil, got %v", p)
		}
	})
}

func TestHasRepos(t *testing.T) {
	if (WorkspaceConfig{}).HasRepos() {
		t.Error("expected false for empty repos")
	}
	if !(WorkspaceConfig{Repos: []RepoConfig{{URL: "a"}}}).HasRepos() {
		t.Error("expected true for non-empty repos")
	}
}
