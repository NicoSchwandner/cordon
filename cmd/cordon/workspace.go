package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage workspaces",
	}
	cmd.AddCommand(wsCreateCmd(), wsListCmd(), wsDestroyCmd(), wsExecCmd())
	return cmd
}

// progressEvent mirrors the server's progress.Event struct.
type progressEvent struct {
	Step          string `json:"step"`
	Message       string `json:"message"`
	Done          bool   `json:"done"`
	Error         string `json:"error,omitempty"`
	EstimatedSecs int    `json:"estimated_secs,omitempty"`
}

// parseRepoFlag parses "url@branch" into (url, branch).
// If no "@" is present, branch is empty.
func parseRepoFlag(value string) (string, string) {
	// Find the last "@" to handle URLs that might contain "@"
	idx := strings.LastIndex(value, "@")
	if idx == -1 || idx == 0 {
		return value, ""
	}
	// Make sure we're not splitting a URL like git@github.com
	url := value[:idx]
	branch := value[idx+1:]
	// If url doesn't look like a repo URL (no dots/slashes), treat as plain URL
	if !strings.Contains(url, "/") && !strings.Contains(url, ".") {
		return value, ""
	}
	return url, branch
}

type repoConfigJSON struct {
	URL              string `json:"url"`
	Branch           string `json:"branch,omitempty"`
	Primary          bool   `json:"primary,omitempty"`
	ServiceContainer bool   `json:"service_container,omitempty"`
}

func wsCreateCmd() *cobra.Command {
	var repoFlags []string
	var cpu, memMB int

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new workspace",
		Long: `Create a new workspace, optionally from one or more repositories.

Use URL@branch syntax to specify branches per repo:
  cordon ws create my-feature --repo github.com/org/backend@DEV-123 --repo github.com/org/frontend@DEV-123

The first --repo is the primary (devcontainer source). Omit @branch to auto-detect.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "workspace"
			if len(args) > 0 {
				name = args[0]
			}

			reqBody := map[string]any{
				"name":      name,
				"cpu":       cpu,
				"memory_mb": memMB,
			}

			if len(repoFlags) > 0 {
				repos := make([]repoConfigJSON, len(repoFlags))
				for i, flag := range repoFlags {
					// Check for :service suffix
					svc := false
					if strings.HasSuffix(flag, ":service") {
						svc = true
						flag = strings.TrimSuffix(flag, ":service")
					}
					url, branch := parseRepoFlag(flag)
					repos[i] = repoConfigJSON{
						URL:              url,
						Branch:           branch,
						Primary:          i == 0,
						ServiceContainer: svc,
					}
				}
				reqBody["repos"] = repos
			}

			body, _ := json.Marshal(reqBody)
			resp, err := http.Post(serverURL+"/api/workspaces", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				fmt.Fprintf(os.Stderr, "Error: %s\n", string(data))
				os.Exit(1)
			}

			var ws struct {
				ID     string `json:"id"`
				Name   string `json:"name"`
				Status string `json:"status"`
			}
			json.Unmarshal(data, &ws)

			// Async creation (repo-based) — stream progress
			if ws.Status == "creating" {
				fmt.Printf("Creating workspace %s (%s)...\n", ws.Name, ws.ID[:8])
				if err := streamProgress(ws.ID); err != nil {
					return err
				}
			} else {
				fmt.Printf("Workspace created: %s (%s)\n", ws.Name, ws.ID[:8])
			}

			fmt.Printf("Connect: cordon connect %s\n", ws.ID[:8])
			return nil
		},
	}

	cmd.Flags().StringArrayVar(&repoFlags, "repo", nil, "Repository URL (use url@branch for branch, repeatable)")
	cmd.Flags().IntVar(&cpu, "cpu", 2, "CPU cores")
	cmd.Flags().IntVar(&memMB, "memory", 4096, "Memory in MB")
	return cmd
}

// streamProgress connects to the SSE endpoint and prints creation progress.
func streamProgress(workspaceID string) error {
	resp, err := http.Get(serverURL + "/api/workspaces/" + workspaceID + "/logs")
	if err != nil {
		return fmt.Errorf("connecting to progress stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("progress stream returned %d", resp.StatusCode)
	}

	var estimatedSecs int
	start := time.Now()
	completed := []string{}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		var evt progressEvent
		if err := json.Unmarshal([]byte(line[6:]), &evt); err != nil {
			continue
		}

		if evt.EstimatedSecs > 0 && estimatedSecs == 0 {
			estimatedSecs = evt.EstimatedSecs
		}

		if evt.Done {
			// Print all completed steps
			for _, msg := range completed {
				fmt.Printf("  \033[32m✓\033[0m %s\n", msg)
			}
			if evt.Error != "" {
				fmt.Printf("  \033[31m✗ %s\033[0m\n", evt.Error)
				return fmt.Errorf("creation failed: %s", evt.Error)
			}
			fmt.Printf("  \033[32m✓\033[0m Ready! (%.0fs)\n", time.Since(start).Seconds())
			return nil
		}

		// Clear current line and print previous step as completed
		fmt.Printf("\033[2K\r")
		if len(completed) > 0 {
			fmt.Printf("  \033[32m✓\033[0m %s\n", completed[len(completed)-1])
		}
		completed = append(completed, evt.Message)

		// Print current step with time estimate
		remaining := ""
		if estimatedSecs > 0 {
			left := float64(estimatedSecs) - time.Since(start).Seconds()
			if left > 0 {
				remaining = fmt.Sprintf("  ~%.0fs remaining", left)
			}
		}
		fmt.Printf("  ● %s%s", evt.Message, remaining)
	}

	return scanner.Err()
}

func wsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(serverURL + "/api/workspaces")
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)
			var workspaces []struct {
				ID        string `json:"id"`
				Name      string `json:"name"`
				Status    string `json:"status"`
				Repos     []struct {
					URL     string `json:"url"`
					Branch  string `json:"branch"`
					Primary bool   `json:"primary"`
				} `json:"repos"`
				CreatedAt string `json:"created_at"`
			}
			json.Unmarshal(data, &workspaces)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSTATUS\tREPOS\tCREATED")
			for _, ws := range workspaces {
				repoSummary := "-"
				if len(ws.Repos) > 0 {
					primary := ws.Repos[0]
					for _, r := range ws.Repos {
						if r.Primary {
							primary = r
							break
						}
					}
					parts := strings.Split(primary.URL, "/")
					repoSummary = parts[len(parts)-1]
					if len(ws.Repos) > 1 {
						repoSummary += fmt.Sprintf(" +%d", len(ws.Repos)-1)
					}
				}
				created := ws.CreatedAt
				if len(created) > 19 {
					created = created[:19]
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", ws.ID[:8], ws.Name, ws.Status, repoSummary, created)
			}
			w.Flush()
			return nil
		},
	}
}

func wsDestroyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy <workspace-id>",
		Short: "Destroy a workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fullID, err := resolveWorkspaceID(args[0])
			if err != nil {
				return err
			}
			req, _ := http.NewRequest("DELETE", serverURL+"/api/workspaces/"+fullID, nil)
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == 204 {
				fmt.Println("Workspace destroyed.")
			} else {
				data, _ := io.ReadAll(resp.Body)
				fmt.Fprintf(os.Stderr, "Error: %s\n", string(data))
			}
			return nil
		},
	}
}

func wsExecCmd() *cobra.Command {
	var repo string

	cmd := &cobra.Command{
		Use:   "exec <workspace-id> -- <command...>",
		Short: "Execute a command in a workspace container",
		Long: `Execute a command in a workspace container. Use --repo to target
a specific repo's service container (Phase 2).

Examples:
  cordon ws exec abc12345 -- ls /workspace
  cordon ws exec abc12345 --repo frontend -- npm run build`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			fullID, err := resolveWorkspaceID(args[0])
			if err != nil {
				return err
			}

			// Everything after "--" is the command
			cmdArgs := cmd.Flags().Args()
			if len(cmdArgs) < 2 {
				return fmt.Errorf("no command specified (use -- before the command)")
			}
			execCmd := cmdArgs[1:]

			reqBody := map[string]any{
				"cmd": execCmd,
			}
			if repo != "" {
				reqBody["repo"] = repo
			}

			body, _ := json.Marshal(reqBody)
			resp, err := http.Post(serverURL+"/api/workspaces/"+fullID+"/exec", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)
			if resp.StatusCode >= 400 {
				fmt.Fprintf(os.Stderr, "Error: %s\n", string(data))
				os.Exit(1)
			}

			var result struct {
				ExitCode int    `json:"exit_code"`
				Output   string `json:"output"`
			}
			json.Unmarshal(data, &result)
			fmt.Print(result.Output)
			if result.ExitCode != 0 {
				os.Exit(result.ExitCode)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Target repo's service container")
	return cmd
}

// resolveWorkspaceID resolves a short workspace ID prefix to a full UUID
// by querying the workspace list API. If the input is already a full UUID,
// it is returned as-is.
func resolveWorkspaceID(idOrPrefix string) (string, error) {
	// If it looks like a full UUID, return as-is
	if len(idOrPrefix) == 36 && strings.Count(idOrPrefix, "-") == 4 {
		return idOrPrefix, nil
	}

	resp, err := http.Get(serverURL + "/api/workspaces")
	if err != nil {
		return "", fmt.Errorf("listing workspaces: %w", err)
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	var workspaces []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &workspaces); err != nil {
		return "", fmt.Errorf("parsing workspace list: %w", err)
	}

	prefix := strings.ToLower(idOrPrefix)
	var matches []string
	for _, ws := range workspaces {
		if strings.HasPrefix(strings.ToLower(ws.ID), prefix) {
			matches = append(matches, ws.ID)
		}
	}

	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no workspace found matching %q", idOrPrefix)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("ambiguous workspace ID %q matches %d workspaces", idOrPrefix, len(matches))
	}
}
