package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/web"
)

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	listenAddr := flag.String("listen", "127.0.0.1:8080", "Address to listen on")
	showVersion := flag.Bool("version", false, "Show version information")
	flag.Parse()

	if *showVersion {
		fmt.Printf("vmm-web version %s\ncommit: %s\nbuilt: %s\n", version, commit, date)
		os.Exit(0)
	}

	password := os.Getenv("VMM_WEB_PASSWORD")
	if password == "" {
		fmt.Fprintln(os.Stderr, "Error: VMM_WEB_PASSWORD environment variable is required")
		fmt.Fprintln(os.Stderr, "Usage: VMM_WEB_PASSWORD=<password> vmm-web [--listen <addr>]")
		os.Exit(1)
	}

	cfg, err := config.Load(config.ConfigPath())
	if err != nil {
		log.Printf("Warning: failed to load config: %v", err)
		cfg = config.DefaultConfig()
	}

	server, err := web.NewServer(cfg, password, *listenAddr)
	if err != nil {
		log.Fatalf("Failed to create server: %v", err)
	}

	if err := server.Run(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
