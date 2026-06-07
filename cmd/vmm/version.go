package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func versionCmd() *cobra.Command {
	var jsonOutput bool

	cmd := &cobra.Command{
		Use:   "version",
		Short: "Print version information",
		RunE: func(cmd *cobra.Command, args []string) error {
			if jsonOutput {
				info := struct {
					Version string `json:"version"`
					Commit  string `json:"commit"`
					Date    string `json:"date"`
				}{
					Version: version,
					Commit:  commit,
					Date:    date,
				}
				enc := json.NewEncoder(os.Stdout)
				return enc.Encode(info)
			}
			fmt.Printf("vmm version %s\n", version)
			fmt.Printf("commit: %s\n", commit)
			fmt.Printf("built: %s\n", date)
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output version as JSON")

	return cmd
}
