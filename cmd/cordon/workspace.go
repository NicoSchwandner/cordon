package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func workspaceCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "workspace",
		Aliases: []string{"ws"},
		Short:   "Manage workspaces",
	}
	cmd.AddCommand(wsCreateCmd(), wsListCmd(), wsDestroyCmd())
	return cmd
}

func wsCreateCmd() *cobra.Command {
	var repo string
	var cpu, memMB int

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new workspace",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := "workspace"
			if len(args) > 0 {
				name = args[0]
			}
			body, _ := json.Marshal(map[string]any{
				"name":      name,
				"repo":      repo,
				"cpu":       cpu,
				"memory_mb": memMB,
			})

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
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			json.Unmarshal(data, &ws)
			fmt.Printf("Workspace created: %s (%s)\n", ws.Name, ws.ID)
			fmt.Printf("Connect: zt connect %s\n", ws.ID)
			return nil
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "Git repository to clone")
	cmd.Flags().IntVar(&cpu, "cpu", 2, "CPU cores")
	cmd.Flags().IntVar(&memMB, "memory", 4096, "Memory in MB")
	return cmd
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
				CreatedAt string `json:"created_at"`
			}
			json.Unmarshal(data, &workspaces)

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "ID\tNAME\tSTATUS\tCREATED")
			for _, ws := range workspaces {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", ws.ID[:8], ws.Name, ws.Status, ws.CreatedAt[:19])
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
			req, _ := http.NewRequest("DELETE", serverURL+"/api/workspaces/"+args[0], nil)
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
