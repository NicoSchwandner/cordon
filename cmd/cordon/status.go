package main

import (
	"fmt"
	"net/http"

	"github.com/spf13/cobra"
)

func statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Check Cordon server status",
		RunE: func(cmd *cobra.Command, args []string) error {
			resp, err := http.Get(serverURL + "/health")
			if err != nil {
				fmt.Printf("Server unreachable at %s: %v\n", serverURL, err)
				return nil
			}
			defer resp.Body.Close()

			if resp.StatusCode == 200 {
				fmt.Printf("Cordon server is running at %s\n", serverURL)
			} else {
				fmt.Printf("Cordon server returned status %d\n", resp.StatusCode)
			}

			resp2, err := http.Get(serverURL + "/ready")
			if err == nil {
				defer resp2.Body.Close()
				if resp2.StatusCode == 200 {
					fmt.Println("Database: connected")
				} else {
					fmt.Println("Database: not ready")
				}
			}

			return nil
		},
	}
}
