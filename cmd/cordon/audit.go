package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "View audit log",
	}
	cmd.AddCommand(auditShowCmd())
	return cmd
}

func auditShowCmd() *cobra.Command {
	var tierMin int
	var decision, workspaceID, format string
	var limit int

	cmd := &cobra.Command{
		Use:   "show",
		Short: "Show audit entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			params := url.Values{}
			if tierMin > 0 {
				params.Set("tier_min", fmt.Sprintf("%d", tierMin))
			}
			if decision != "" {
				params.Set("decision", decision)
			}
			if workspaceID != "" {
				params.Set("workspace_id", workspaceID)
			}
			if limit > 0 {
				params.Set("limit", fmt.Sprintf("%d", limit))
			}

			reqURL := serverURL + "/api/audit"
			if len(params) > 0 {
				reqURL += "?" + params.Encode()
			}

			resp, err := http.Get(reqURL)
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)

			if format == "json" {
				fmt.Println(string(data))
				return nil
			}

			var entries []struct {
				Timestamp string `json:"timestamp"`
				Tier      int    `json:"tier"`
				TierName  string `json:"tier_name"`
				Operation string `json:"operation"`
				Target    string `json:"target"`
				Caller    string `json:"caller"`
				Decision  string `json:"decision"`
				Detail    string `json:"detail"`
			}
			if err := json.Unmarshal(data, &entries); err != nil {
				return fmt.Errorf("parsing response: %w", err)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "TIMESTAMP\tTIER\tOPERATION\tTARGET\tDECISION\tCALLER")
			for _, e := range entries {
				ts := e.Timestamp
				if len(ts) > 19 {
					ts = ts[:19]
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\n",
					ts, e.TierName, e.Operation, truncate(e.Target, 30), e.Decision, e.Caller)
			}
			w.Flush()
			return nil
		},
	}

	cmd.Flags().IntVar(&tierMin, "tier", 0, "Minimum tier level")
	cmd.Flags().StringVar(&decision, "decision", "", "Filter by decision (allowed/blocked/denied)")
	cmd.Flags().StringVar(&workspaceID, "workspace", "", "Filter by workspace ID")
	cmd.Flags().IntVar(&limit, "limit", 50, "Number of entries to show")
	cmd.Flags().StringVar(&format, "format", "table", "Output format (table/json)")
	return cmd
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
