package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/spf13/cobra"
)

func proxyCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proxy",
		Short: "Send operations through the Cordon proxy",
	}

	cmd.AddCommand(proxySQLCmd())
	cmd.AddCommand(proxyHTTPCmd())
	return cmd
}

func proxySQLCmd() *cobra.Command {
	var target, caller string

	cmd := &cobra.Command{
		Use:   "sql <query>",
		Short: "Send a SQL query through the proxy",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			query := args[0]
			body, _ := json.Marshal(map[string]string{
				"query":  query,
				"target": target,
				"caller": caller,
			})

			resp, err := http.Post(serverURL+"/api/proxy/sql", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)

			if resp.StatusCode >= 400 {
				fmt.Fprintf(os.Stderr, "BLOCKED (HTTP %d):\n", resp.StatusCode)
				var pretty bytes.Buffer
				json.Indent(&pretty, data, "", "  ")
				fmt.Fprintln(os.Stderr, pretty.String())
				os.Exit(1)
			}

			var pretty bytes.Buffer
			json.Indent(&pretty, data, "", "  ")
			fmt.Println(pretty.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&target, "target", "", "Target database/service")
	cmd.Flags().StringVar(&caller, "caller", "cli-user", "Caller identity")
	return cmd
}

func proxyHTTPCmd() *cobra.Command {
	var host, caller string

	cmd := &cobra.Command{
		Use:   "http <method> <url>",
		Short: "Send an HTTP request through the proxy",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			method, url := args[0], args[1]
			body, _ := json.Marshal(map[string]string{
				"method": method,
				"url":    url,
				"host":   host,
				"caller": caller,
			})

			resp, err := http.Post(serverURL+"/api/proxy/http", "application/json", bytes.NewReader(body))
			if err != nil {
				return fmt.Errorf("request failed: %w", err)
			}
			defer resp.Body.Close()

			data, _ := io.ReadAll(resp.Body)

			if resp.StatusCode >= 400 {
				fmt.Fprintf(os.Stderr, "BLOCKED (HTTP %d):\n", resp.StatusCode)
				var pretty bytes.Buffer
				json.Indent(&pretty, data, "", "  ")
				fmt.Fprintln(os.Stderr, pretty.String())
				os.Exit(1)
			}

			var pretty bytes.Buffer
			json.Indent(&pretty, data, "", "  ")
			fmt.Println(pretty.String())
			return nil
		},
	}

	cmd.Flags().StringVar(&host, "host", "", "Target host for egress check")
	cmd.Flags().StringVar(&caller, "caller", "cli-user", "Caller identity")
	return cmd
}
