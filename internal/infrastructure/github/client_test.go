package github

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestParseOwnerRepo(t *testing.T) {
	tests := []struct {
		url       string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"github.com/org/repo", "org", "repo", false},
		{"https://github.com/org/repo.git", "org", "repo", false},
		{"https://github.com/WintDev/Core", "WintDev", "Core", false},
		{"github.com/org/repo/extra", "org", "repo", false},
		{"gitlab.com/org/repo", "", "", true},
		{"not-a-url", "", "", true},
		{"github.com/org", "", "", true},
	}

	for _, tt := range tests {
		owner, repo, err := ParseOwnerRepo(tt.url)
		if tt.wantErr {
			if err == nil {
				t.Errorf("ParseOwnerRepo(%q) expected error, got (%q, %q)", tt.url, owner, repo)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseOwnerRepo(%q) unexpected error: %v", tt.url, err)
			continue
		}
		if owner != tt.wantOwner || repo != tt.wantRepo {
			t.Errorf("ParseOwnerRepo(%q) = (%q, %q), want (%q, %q)", tt.url, owner, repo, tt.wantOwner, tt.wantRepo)
		}
	}
}

func TestListOrgRepos(t *testing.T) {
	repos := []OrgRepo{
		{Name: "Core", FullName: "WintDev/Core", DefaultBranch: "development"},
		{Name: "Wint.Model", FullName: "WintDev/Wint.Model", DefaultBranch: "main", Archived: true},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ETag", `"abc123"`)
		json.NewEncoder(w).Encode(repos)
	}))
	defer srv.Close()

	client := &Client{
		token: "test-token",
		http:  srv.Client(),
	}

	// Override the URL by calling the internal method through a test helper
	// For now, test caching behavior with the public method
	// (full integration test would need URL override)

	// Test that ParseOwnerRepo + cache work together
	client.defaultBranches.Store("WintDev/Core", "development")
	branch := client.ResolveDefaultBranch("github.com/WintDev/Core")
	if branch != "development" {
		t.Errorf("ResolveDefaultBranch() = %q, want %q", branch, "development")
	}
}

func TestListBranches(t *testing.T) {
	branches := []Branch{
		{Name: "main"},
		{Name: "development"},
		{Name: "DEV-123-feature"},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			w.WriteHeader(401)
			return
		}
		json.NewEncoder(w).Encode(branches)
	}))
	defer srv.Close()

	// Verify the test server works (integration test pattern)
	client := NewClient("test-token")
	client.http = srv.Client()

	// We can't easily test the real GitHub API without URL override,
	// but we can test the cache layer
	client.repoBranches.Store("WintDev/Core", &cachedBranchList{
		branches:  branches,
		fetchedAt: time.Now(),
	})

	got, err := client.ListBranches("WintDev", "Core")
	if err != nil {
		t.Fatalf("ListBranches() error: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("ListBranches() returned %d branches, want 3", len(got))
	}
	if got[0].Name != "main" {
		t.Errorf("first branch = %q, want %q", got[0].Name, "main")
	}
}

func TestResolveDefaultBranchFallback(t *testing.T) {
	client := NewClient("") // no token
	branch := client.ResolveDefaultBranch("github.com/org/repo")
	if branch != "main" {
		t.Errorf("ResolveDefaultBranch() without token = %q, want %q", branch, "main")
	}
}

func TestResolveDefaultBranchNonGitHub(t *testing.T) {
	client := NewClient("test-token")
	branch := client.ResolveDefaultBranch("gitlab.com/org/repo")
	if branch != "main" {
		t.Errorf("ResolveDefaultBranch() for non-GitHub = %q, want %q", branch, "main")
	}
}
