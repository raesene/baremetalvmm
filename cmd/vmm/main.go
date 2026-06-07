package main

import (
	"fmt"
	"os"

	"github.com/raesene/baremetalvmm/internal/config"
)

func main() {
	var err error
	cfg, err = config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		cfg = config.DefaultConfig()
	}

	rootCmd := newRootCmd()

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}
