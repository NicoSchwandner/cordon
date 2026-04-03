package main

import "testing"

func TestFindRepoForDir(t *testing.T) {
	repoMap := map[string]repoMapEntry{
		"backend":  {Container: "cordon-ws-abc-backend-svc", Path: "/workspace/backend"},
		"frontend": {Container: "cordon-ws-abc-frontend-svc", Path: "/workspace/frontend"},
	}

	tests := []struct {
		pwd      string
		wantRepo string
		wantHit  bool
	}{
		{"/workspace/backend", "backend", true},
		{"/workspace/backend/src/main.go", "backend", true},
		{"/workspace/frontend", "frontend", true},
		{"/workspace/frontend/src/App.svelte", "frontend", true},
		{"/workspace", "", false},
		{"/home/user", "", false},
		{"/workspace/other-repo", "", false},
	}

	for _, tt := range tests {
		name, entry := findRepoForDir(tt.pwd, repoMap)
		if tt.wantHit {
			if name != tt.wantRepo || entry == nil {
				t.Errorf("findRepoForDir(%q) = (%q, %v), want (%q, non-nil)", tt.pwd, name, entry, tt.wantRepo)
			}
		} else {
			if name != "" || entry != nil {
				t.Errorf("findRepoForDir(%q) = (%q, %v), want ('', nil)", tt.pwd, name, entry)
			}
		}
	}
}

func TestRemoveFromPath(t *testing.T) {
	path := "/workspace/.cordon/bin:/usr/local/bin:/usr/bin"
	got := removeFromPath(path, "/workspace/.cordon/bin")
	want := "/usr/local/bin:/usr/bin"
	if got != want {
		t.Errorf("removeFromPath() = %q, want %q", got, want)
	}
}
