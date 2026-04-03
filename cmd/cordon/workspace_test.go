package main

import "testing"

func TestParseRepoFlag(t *testing.T) {
	tests := []struct {
		input      string
		wantURL    string
		wantBranch string
	}{
		{"github.com/org/repo", "github.com/org/repo", ""},
		{"github.com/org/repo@main", "github.com/org/repo", "main"},
		{"github.com/org/repo@DEV-123-feature", "github.com/org/repo", "DEV-123-feature"},
		{"https://github.com/org/repo.git@develop", "https://github.com/org/repo.git", "develop"},
		{"simple-repo", "simple-repo", ""},
		// git@ URLs should not split on the user@ part
		{"git@github.com:org/repo", "git@github.com:org/repo", ""},
	}

	for _, tt := range tests {
		url, branch := parseRepoFlag(tt.input)
		if url != tt.wantURL || branch != tt.wantBranch {
			t.Errorf("parseRepoFlag(%q) = (%q, %q), want (%q, %q)",
				tt.input, url, branch, tt.wantURL, tt.wantBranch)
		}
	}
}
