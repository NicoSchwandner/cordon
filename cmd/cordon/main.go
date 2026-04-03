package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var serverURL string

func main() {
	rootCmd := &cobra.Command{
		Use:   "cordon",
		Short: "Cordon CLI — zero-trust developer environment",
	}

	rootCmd.PersistentFlags().StringVar(&serverURL, "server", envOr("CORDON_SERVER", "http://localhost:8443"), "Cordon server URL")

	rootCmd.AddCommand(
		connectCmd(),
		proxyCmd(),
		auditCmd(),
		workspaceCmd(),
		statusCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func exitErr(msg string, args ...any) {
	fmt.Fprintf(os.Stderr, msg+"\n", args...)
	os.Exit(1)
}
