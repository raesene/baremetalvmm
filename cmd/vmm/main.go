package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

var cfg *config.Config

func main() {
	var err error
	cfg, err = config.Load(config.ConfigPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: failed to load config: %v\n", err)
		cfg = config.DefaultConfig()
	}

	rootCmd := &cobra.Command{
		Use:   "vmm",
		Short: "Bare Metal MicroVM Manager",
		Long:  "A CLI tool to manage lightweight Firecracker microVMs for development environments",
	}

	rootCmd.AddCommand(
		createCmd(),
		deleteCmd(),
		listCmd(),
		startCmd(),
		stopCmd(),
		sshCmd(),
		configCmd(),
		imageCmd(),
		portForwardCmd(),
		autostartCmd(),
		autostopCmd(),
	)

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func createCmd() *cobra.Command {
	var cpus int
	var memory int
	var disk int
	var sshKeyPath string
	var dnsServers []string
	var imageName string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a new microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			// Ensure directories exist
			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			paths := cfg.GetPaths()

			// Check if VM already exists
			if vm.Exists(paths.VMs, name) {
				return fmt.Errorf("VM '%s' already exists", name)
			}

			// Validate image exists if specified
			if imageName != "" {
				imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
				if !imgMgr.ImageExists(imageName) {
					return fmt.Errorf("image '%s' not found. Use 'vmm image list' to see available images", imageName)
				}
			}

			// Create new VM
			newVM := vm.NewVM(name)
			newVM.CPUs = cpus
			newVM.MemoryMB = memory
			newVM.DiskSizeMB = disk
			newVM.Image = imageName
			newVM.MacAddress = newVM.GenerateMacAddress()
			newVM.TapDevice = network.GenerateTapName(newVM.ID)
			newVM.DNSServers = dnsServers

			// Set paths
			newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, name)

			// Read SSH public key if provided
			if sshKeyPath != "" {
				keyData, err := os.ReadFile(sshKeyPath)
				if err != nil {
					return fmt.Errorf("failed to read SSH public key from %s: %w", sshKeyPath, err)
				}
				newVM.SSHPublicKey = string(keyData)
			}

			// Save VM config
			if err := newVM.Save(paths.VMs); err != nil {
				return fmt.Errorf("failed to save VM config: %w", err)
			}

			fmt.Printf("Created VM '%s' (ID: %s)\n", name, newVM.ID)
			fmt.Printf("  CPUs: %d, Memory: %d MB, Disk: %d MB\n", newVM.CPUs, newVM.MemoryMB, newVM.DiskSizeMB)
			if newVM.Image != "" {
				fmt.Printf("  Image: %s\n", newVM.Image)
			}
			fmt.Printf("  TAP device: %s, MAC: %s\n", newVM.TapDevice, newVM.MacAddress)
			if newVM.SSHPublicKey != "" {
				fmt.Printf("  SSH key: configured\n")
			}
			if len(newVM.DNSServers) > 0 {
				fmt.Printf("  DNS servers: %v\n", newVM.DNSServers)
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&cpus, "cpus", 1, "Number of vCPUs")
	cmd.Flags().IntVar(&memory, "memory", 512, "Memory in MB")
	cmd.Flags().IntVar(&disk, "disk", 1024, "Disk size in MB")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to SSH public key file for root access")
	cmd.Flags().StringSliceVar(&dnsServers, "dns", nil, "Custom DNS servers (can be specified multiple times)")
	cmd.Flags().StringVar(&imageName, "image", "", "Name of rootfs image to use (from 'vmm image import')")

	return cmd
}

func deleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()

			// Load VM to check state
			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			// Update state based on actual running status
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			// Check if running
			if existingVM.State == vm.StateRunning {
				if !force {
					return fmt.Errorf("VM '%s' is running. Use --force to delete anyway", name)
				}
				// Stop VM if force
				fmt.Printf("Stopping VM '%s'...\n", name)
				ctx := context.Background()
				if err := fcClient.StopVM(ctx, existingVM.SocketPath); err != nil {
					fmt.Printf("Warning: failed to stop VM gracefully: %v\n", err)
				}
			}

			// Cleanup network resources
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
				if err := netMgr.DeleteTap(existingVM.TapDevice); err != nil {
					fmt.Printf("Warning: failed to delete TAP device: %v\n", err)
				}
			}

			// Delete VM rootfs
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			if err := imgMgr.DeleteVMRootfs(name, paths.VMs); err != nil {
				fmt.Printf("Warning: failed to delete VM rootfs: %v\n", err)
			}

			// Delete socket file
			os.Remove(existingVM.SocketPath)

			// Delete VM config
			if err := vm.Delete(paths.VMs, name); err != nil {
				return fmt.Errorf("failed to delete VM: %w", err)
			}

			fmt.Printf("Deleted VM '%s'\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force delete even if running")

	return cmd
}

func listCmd() *cobra.Command {
	var all bool

	cmd := &cobra.Command{
		Use:     "list",
		Short:   "List all microVMs",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			vms, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			if len(vms) == 0 {
				fmt.Println("No VMs found. Create one with: vmm create <name>")
				return nil
			}

			// Update state for each VM
			fcClient := firecracker.NewClient()
			for _, v := range vms {
				fcClient.UpdateVMState(v)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tID\tSTATE\tCPUs\tMEMORY\tIP ADDRESS")
			for _, v := range vms {
				if !all && v.State == vm.StateStopped {
					continue
				}
				ip := v.IPAddress
				if ip == "" {
					ip = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d MB\t%s\n",
					v.Name, v.ID, v.State, v.CPUs, v.MemoryMB, ip)
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", true, "Show all VMs including stopped")

	return cmd
}

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "start <name>",
		Short: "Start a microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			// Update state
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			if existingVM.State == vm.StateRunning {
				return fmt.Errorf("VM '%s' is already running", name)
			}

			fmt.Printf("Starting VM '%s'...\n", name)

			// Ensure images are available
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			if err := imgMgr.EnsureDefaultImages(); err != nil {
				return fmt.Errorf("failed to ensure images: %w", err)
			}

			// Create VM-specific rootfs if needed
			vmRootfs, err := imgMgr.CreateVMRootfs(name, paths.VMs, existingVM.DiskSizeMB, existingVM.Image)
			if err != nil {
				return fmt.Errorf("failed to create VM rootfs: %w", err)
			}
			existingVM.RootfsPath = vmRootfs
			existingVM.KernelPath = imgMgr.GetDefaultKernelPath()

			// Inject SSH key if configured
			if existingVM.SSHPublicKey != "" {
				fmt.Println("Injecting SSH public key...")
				if err := image.InjectSSHKey(existingVM.RootfsPath, existingVM.SSHPublicKey); err != nil {
					return fmt.Errorf("failed to inject SSH key: %w", err)
				}
			}

			// Inject DNS configuration
			fmt.Println("Configuring DNS...")
			if err := image.InjectDNSConfig(existingVM.RootfsPath, existingVM.DNSServers); err != nil {
				return fmt.Errorf("failed to inject DNS config: %w", err)
			}

			// Setup networking
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)

			// Ensure bridge exists
			if err := netMgr.EnsureBridge(); err != nil {
				return fmt.Errorf("failed to setup bridge: %w", err)
			}

			// Create TAP device if it doesn't exist
			if !netMgr.TapExists(existingVM.TapDevice) {
				if err := netMgr.CreateTap(existingVM.TapDevice); err != nil {
					return fmt.Errorf("failed to create TAP device: %w", err)
				}
			}

			// Allocate IP (use VM index based on creation order for simplicity)
			vms, _ := vm.List(paths.VMs)
			vmIndex := 0
			for i, v := range vms {
				if v.Name == name {
					vmIndex = i
					break
				}
			}
			ip, err := netMgr.AllocateIP(vmIndex)
			if err != nil {
				return fmt.Errorf("failed to allocate IP: %w", err)
			}
			existingVM.IPAddress = ip

			// Update state to starting
			existingVM.State = vm.StateStarting
			existingVM.Save(paths.VMs)

			// Start Firecracker
			ctx := context.Background()
			vmCfg := &firecracker.VMConfig{
				SocketPath: existingVM.SocketPath,
				KernelPath: existingVM.KernelPath,
				RootfsPath: existingVM.RootfsPath,
				CPUs:       existingVM.CPUs,
				MemoryMB:   existingVM.MemoryMB,
				TapDevice:  existingVM.TapDevice,
				MacAddress: existingVM.MacAddress,
				LogPath:    fmt.Sprintf("%s/%s.log", paths.Logs, name),
				IPAddress:  existingVM.IPAddress,
				Gateway:    cfg.Gateway,
			}

			machine, err := fcClient.StartVM(ctx, vmCfg)
			if err != nil {
				existingVM.State = vm.StateError
				existingVM.Save(paths.VMs)
				return fmt.Errorf("failed to start VM: %w", err)
			}

			// Update VM state
			existingVM.State = vm.StateRunning
			existingVM.PID = fcClient.GetVMPID(machine)
			existingVM.StartedAt = time.Now()
			existingVM.Save(paths.VMs)

			fmt.Printf("VM '%s' started successfully\n", name)
			fmt.Printf("  IP Address: %s\n", existingVM.IPAddress)
			fmt.Printf("  PID: %d\n", existingVM.PID)
			fmt.Printf("  Socket: %s\n", existingVM.SocketPath)

			return nil
		},
	}
}

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop <name>",
		Short: "Stop a microVM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			// Update state
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			if existingVM.State != vm.StateRunning {
				return fmt.Errorf("VM '%s' is not running (state: %s)", name, existingVM.State)
			}

			fmt.Printf("Stopping VM '%s'...\n", name)

			existingVM.State = vm.StateStopping
			existingVM.Save(paths.VMs)

			ctx := context.Background()
			if err := fcClient.StopVM(ctx, existingVM.SocketPath); err != nil {
				// Try to kill by PID as fallback
				if existingVM.PID > 0 {
					if proc, err := os.FindProcess(existingVM.PID); err == nil {
						proc.Signal(syscall.SIGKILL)
					}
				}
			}

			// Wait briefly for process to exit
			time.Sleep(500 * time.Millisecond)

			// Clean up TAP device so it can be reused on next start
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
				if err := netMgr.DeleteTap(existingVM.TapDevice); err != nil {
					fmt.Printf("Warning: failed to delete TAP device: %v\n", err)
				}
			}

			// Cleanup
			existingVM.State = vm.StateStopped
			existingVM.PID = 0
			existingVM.Save(paths.VMs)

			// Remove socket
			os.Remove(existingVM.SocketPath)

			fmt.Printf("VM '%s' stopped\n", name)
			return nil
		},
	}
}

func sshCmd() *cobra.Command {
	var user string

	cmd := &cobra.Command{
		Use:   "ssh <name> [-- <ssh-args>]",
		Short: "SSH into a microVM",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			// Update state
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			if existingVM.State != vm.StateRunning {
				return fmt.Errorf("VM '%s' is not running", name)
			}

			if existingVM.IPAddress == "" {
				return fmt.Errorf("VM '%s' has no IP address assigned", name)
			}

			// Build SSH command
			sshArgs := []string{
				"-o", "StrictHostKeyChecking=no",
				"-o", "UserKnownHostsFile=/dev/null",
			}

			// If running under sudo, use the original user's SSH keys
			if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" {
				// Get the original user's home directory
				userHome := fmt.Sprintf("/home/%s", sudoUser)
				if sudoUser == "root" {
					userHome = "/root"
				}

				// Look for common SSH key files
				keyFiles := []string{
					"id_ed25519",
					"id_rsa",
					"id_ecdsa",
					"id_dsa",
				}

				for _, keyFile := range keyFiles {
					keyPath := fmt.Sprintf("%s/.ssh/%s", userHome, keyFile)
					if _, err := os.Stat(keyPath); err == nil {
						sshArgs = append(sshArgs, "-i", keyPath)
						break
					}
				}
			}

			sshArgs = append(sshArgs, fmt.Sprintf("%s@%s", user, existingVM.IPAddress))

			// Append any additional SSH args
			if len(args) > 1 {
				sshArgs = append(sshArgs, args[1:]...)
			}

			// Execute SSH
			sshCmd := exec.Command("ssh", sshArgs...)
			sshCmd.Stdin = os.Stdin
			sshCmd.Stdout = os.Stdout
			sshCmd.Stderr = os.Stderr

			return sshCmd.Run()
		},
	}

	cmd.Flags().StringVarP(&user, "user", "u", "root", "SSH user")

	return cmd
}

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
			return nil
		},
	}

	initCmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize VMM directories and default config",
		RunE: func(cmd *cobra.Command, args []string) error {
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

	cmd.AddCommand(showCmd, initCmd)
	return cmd
}

func imageCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "image",
		Short: "Manage VM images",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available images",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			fmt.Println("Kernels:")
			kernels, _ := imgMgr.ListKernels()
			if len(kernels) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, k := range kernels {
					fmt.Printf("  - %s\n", k)
				}
			}

			fmt.Println("\nRoot filesystems:")
			rootfs, _ := imgMgr.ListRootfs()
			if len(rootfs) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, r := range rootfs {
					// Remove .ext4 extension for display
					name := r
					if len(r) > 5 && r[len(r)-5:] == ".ext4" {
						name = r[:len(r)-5]
					}
					fmt.Printf("  - %s\n", name)
				}
			}

			return nil
		},
	}

	pullCmd := &cobra.Command{
		Use:   "pull",
		Short: "Download default kernel and rootfs images",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			if err := imgMgr.EnsureDefaultImages(); err != nil {
				return fmt.Errorf("failed to download images: %w", err)
			}

			fmt.Println("Images downloaded successfully")
			fmt.Printf("  Kernel: %s\n", imgMgr.GetDefaultKernelPath())
			fmt.Printf("  Rootfs: %s\n", imgMgr.GetDefaultRootfsPath())
			return nil
		},
	}

	var importSize int
	importCmd := &cobra.Command{
		Use:   "import <docker-image> --name <name>",
		Short: "Import a Docker image as a VMM rootfs",
		Long: `Import a Docker image as a VMM rootfs.

The Docker image will be exported, configured with systemd and SSH,
and converted to an ext4 filesystem suitable for Firecracker VMs.

Examples:
  vmm image import ubuntu:22.04 --name ubuntu-base
  vmm image import myregistry/myapp:latest --name myapp --size 4096`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dockerImage := args[0]
			name, _ := cmd.Flags().GetString("name")

			if name == "" {
				return fmt.Errorf("--name is required")
			}

			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			if err := imgMgr.ImportDockerImage(dockerImage, name, importSize); err != nil {
				return err
			}

			return nil
		},
	}
	importCmd.Flags().String("name", "", "Name for the imported image (required)")
	importCmd.Flags().IntVar(&importSize, "size", 2048, "Size of the image in MB")
	importCmd.MarkFlagRequired("name")

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete an imported image",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			if err := imgMgr.DeleteImage(name); err != nil {
				return err
			}

			fmt.Printf("Deleted image '%s'\n", name)
			return nil
		},
	}

	cmd.AddCommand(listCmd, pullCmd, importCmd, deleteCmd)
	return cmd
}

func portForwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port-forward <name> <host-port>:<guest-port>",
		Short: "Forward a port from host to VM",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			portSpec := args[1]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			if existingVM.IPAddress == "" {
				return fmt.Errorf("VM '%s' has no IP address", name)
			}

			// Parse port spec
			var hostPort, guestPort int
			if _, err := fmt.Sscanf(portSpec, "%d:%d", &hostPort, &guestPort); err != nil {
				return fmt.Errorf("invalid port spec '%s', expected format: host-port:guest-port", portSpec)
			}

			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			if err := netMgr.AddPortForward(hostPort, guestPort, existingVM.IPAddress, "tcp"); err != nil {
				return fmt.Errorf("failed to add port forward: %w", err)
			}

			// Save port forward to VM config
			existingVM.PortForwards = append(existingVM.PortForwards, vm.PortForward{
				HostPort:  hostPort,
				GuestPort: guestPort,
				Protocol:  "tcp",
			})
			existingVM.Save(paths.VMs)

			fmt.Printf("Port forward added: %d -> %s:%d\n", hostPort, existingVM.IPAddress, guestPort)
			return nil
		},
	}

	return cmd
}

func autostartCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "autostart",
		Short:  "Start all VMs marked for auto-start (used by systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			vms, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			fcClient := firecracker.NewClient()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)

			// Ensure bridge exists first
			if err := netMgr.EnsureBridge(); err != nil {
				fmt.Printf("Warning: failed to setup bridge: %v\n", err)
			}

			started := 0
			for i, v := range vms {
				// Skip VMs not marked for autostart
				if !v.AutoStart {
					continue
				}

				// Update actual state
				fcClient.UpdateVMState(v)
				if v.State == vm.StateRunning {
					fmt.Printf("VM '%s' is already running\n", v.Name)
					continue
				}

				fmt.Printf("Auto-starting VM '%s'...\n", v.Name)

				// Ensure images
				if err := imgMgr.EnsureDefaultImages(); err != nil {
					fmt.Printf("  Error: failed to ensure images: %v\n", err)
					continue
				}

				// Create rootfs if needed
				vmRootfs, err := imgMgr.CreateVMRootfs(v.Name, paths.VMs, v.DiskSizeMB, v.Image)
				if err != nil {
					fmt.Printf("  Error: failed to create rootfs: %v\n", err)
					continue
				}
				v.RootfsPath = vmRootfs
				v.KernelPath = imgMgr.GetDefaultKernelPath()

				// Inject SSH key if configured
				if v.SSHPublicKey != "" {
					if err := image.InjectSSHKey(v.RootfsPath, v.SSHPublicKey); err != nil {
						fmt.Printf("  Warning: failed to inject SSH key: %v\n", err)
					}
				}

				// Inject DNS configuration
				if err := image.InjectDNSConfig(v.RootfsPath, v.DNSServers); err != nil {
					fmt.Printf("  Warning: failed to inject DNS config: %v\n", err)
				}

				// Create TAP if needed
				if !netMgr.TapExists(v.TapDevice) {
					if err := netMgr.CreateTap(v.TapDevice); err != nil {
						fmt.Printf("  Error: failed to create TAP: %v\n", err)
						continue
					}
				}

				// Allocate IP
				ip, _ := netMgr.AllocateIP(i)
				v.IPAddress = ip

				// Start VM
				ctx := context.Background()
				vmCfg := &firecracker.VMConfig{
					SocketPath: v.SocketPath,
					KernelPath: v.KernelPath,
					RootfsPath: v.RootfsPath,
					CPUs:       v.CPUs,
					MemoryMB:   v.MemoryMB,
					TapDevice:  v.TapDevice,
					MacAddress: v.MacAddress,
					LogPath:    fmt.Sprintf("%s/%s.log", paths.Logs, v.Name),
					IPAddress:  v.IPAddress,
					Gateway:    cfg.Gateway,
				}

				machine, err := fcClient.StartVM(ctx, vmCfg)
				if err != nil {
					fmt.Printf("  Error: failed to start: %v\n", err)
					v.State = vm.StateError
					v.Save(paths.VMs)
					continue
				}

				v.State = vm.StateRunning
				v.PID = fcClient.GetVMPID(machine)
				v.StartedAt = time.Now()
				v.Save(paths.VMs)

				fmt.Printf("  Started (IP: %s, PID: %d)\n", v.IPAddress, v.PID)
				started++
			}

			fmt.Printf("Auto-started %d VMs\n", started)
			return nil
		},
	}
}

func autostopCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "autostop",
		Short:  "Stop all running VMs (used by systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			vms, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			fcClient := firecracker.NewClient()
			stopped := 0

			for _, v := range vms {
				fcClient.UpdateVMState(v)
				if v.State != vm.StateRunning {
					continue
				}

				fmt.Printf("Stopping VM '%s'...\n", v.Name)

				ctx := context.Background()
				if err := fcClient.StopVM(ctx, v.SocketPath); err != nil {
					// Try SIGKILL as fallback
					if v.PID > 0 {
						if proc, err := os.FindProcess(v.PID); err == nil {
							proc.Signal(syscall.SIGKILL)
						}
					}
				}

				v.State = vm.StateStopped
				v.PID = 0
				v.Save(paths.VMs)

				os.Remove(v.SocketPath)
				stopped++
			}

			fmt.Printf("Stopped %d VMs\n", stopped)
			return nil
		},
	}
}
