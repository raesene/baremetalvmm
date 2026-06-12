package main

import (
	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/spf13/cobra"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

var cfg *config.Config

func newRootCmd() *cobra.Command {
	rootCmd := &cobra.Command{
		Use:           "vmm",
		Short:         "Bare Metal MicroVM Manager",
		Long:          "A CLI tool to manage lightweight Firecracker microVMs for development environments",
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	rootCmd.AddCommand(
		createCmd(),
		deleteCmd(),
		listCmd(),
		startCmd(),
		stopCmd(),
		sshCmd(),
		consoleCmd(),
		configCmd(),
		imageCmd(),
		kernelCmd(),
		portForwardCmd(),
		mountCmd(),
		clusterCmd(),
		versionCmd(),
		autostartCmd(),
		autostopCmd(),
	)

	return rootCmd
}
