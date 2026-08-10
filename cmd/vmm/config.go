package main

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/spf13/cobra"
)

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Manage VMM configuration",
	}

	showCmd := &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Data directory:    %s\n", cfg.DataDir)
			fmt.Printf("Bridge name:       %s\n", cfg.BridgeName)
			fmt.Printf("Subnet:            %s\n", cfg.Subnet)
			fmt.Printf("Gateway:           %s\n", cfg.Gateway)
			fmt.Printf("Host interface:    %s\n", cfg.HostInterface)
			fmt.Printf("Config file:       %s\n", config.ConfigPath())

			// Display VM defaults
			fmt.Printf("\nVM Defaults:\n")
			defaults := cfg.GetVMDefaults()

			// CPUs
			if defaults.CPUs > 0 {
				fmt.Printf("  cpus:            %d (from config)\n", defaults.CPUs)
			} else {
				fmt.Printf("  cpus:            1 (default)\n")
			}

			// Memory
			if defaults.MemoryMB > 0 {
				fmt.Printf("  memory_mb:       %d (from config)\n", defaults.MemoryMB)
			} else {
				fmt.Printf("  memory_mb:       512 (default)\n")
			}

			// Disk
			if defaults.DiskSizeMB > 0 {
				fmt.Printf("  disk_size_mb:    %d (from config)\n", defaults.DiskSizeMB)
			} else {
				fmt.Printf("  disk_size_mb:    1024 (default)\n")
			}

			// Image
			if defaults.Image != "" {
				fmt.Printf("  image:           %s (from config)\n", defaults.Image)
			} else {
				fmt.Printf("  image:           (default rootfs)\n")
			}

			// Kernel
			if defaults.Kernel != "" {
				fmt.Printf("  kernel:          %s (from config)\n", defaults.Kernel)
			} else {
				fmt.Printf("  kernel:          (default kernel)\n")
			}

			// SSH key path
			if defaults.SSHKeyPath != "" {
				fmt.Printf("  ssh_key_path:    %s (from config)\n", defaults.SSHKeyPath)
			} else {
				fmt.Printf("  ssh_key_path:    (none)\n")
			}

			// DNS servers
			if len(defaults.DNSServers) > 0 {
				fmt.Printf("  dns_servers:     %v (from config)\n", defaults.DNSServers)
			} else {
				fmt.Printf("  dns_servers:     [8.8.8.8, 8.8.4.4, 1.1.1.1] (default)\n")
			}

			return nil
		},
	}

	var initDataDir string
	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize VMM directories and default config",
		RunE: func(cmd *cobra.Command, args []string) error {
			if initDataDir != "" {
				if err := config.ValidateDataDir(initDataDir); err != nil {
					return err
				}
				cfg.DataDir = initDataDir
			}

			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			if err := cfg.Save(config.ConfigPath()); err != nil {
				return fmt.Errorf("failed to save config: %w", err)
			}

			fmt.Println("VMM initialized successfully")
			fmt.Printf("Config saved to: %s\n", config.ConfigPath())
			fmt.Printf("Data directory: %s\n", cfg.DataDir)
			return nil
		},
	}
	initCmd.Flags().StringVar(&initDataDir, "data-dir", "", "Data directory for VM state, images, and logs (default /var/lib/vmm)")

	setCmd := &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a configuration value",
		Long: "Set a configuration value and save it to the config file.\n\n" +
			"Supported keys:\n" +
			"  data_dir  Directory where VMM stores all state, images, and logs.",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]
			switch key {
			case "data_dir":
				if err := config.ValidateDataDir(value); err != nil {
					return err
				}
				oldDir := cfg.DataDir
				cfg.DataDir = value

				if err := cfg.EnsureDirectories(); err != nil {
					return fmt.Errorf("failed to create directories: %w", err)
				}
				if err := cfg.Save(config.ConfigPath()); err != nil {
					return fmt.Errorf("failed to save config: %w", err)
				}

				fmt.Printf("data_dir: %s -> %s\n", oldDir, value)
				fmt.Printf("Config saved to: %s\n", config.ConfigPath())
				if oldDir != value {
					fmt.Printf("\nNote: existing data in %s was not moved.\n", oldDir)
					fmt.Println("Move it manually if you want to keep existing VMs, images, and snapshots.")
				}
				return nil
			default:
				return fmt.Errorf("unknown config key %q (supported: data_dir)", key)
			}
		},
	}

	cmd.AddCommand(showCmd, initCmd, setCmd)
	return cmd
}
