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
	"github.com/raesene/baremetalvmm/internal/mount"
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
		kernelCmd(),
		portForwardCmd(),
		mountCmd(),
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
	var kernelName string
	var mounts []string

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

			// Resolve values: CLI flag → config default → hardcoded fallback
			defaults := cfg.GetVMDefaults()

			// CPUs
			if !cmd.Flags().Changed("cpus") {
				if defaults.CPUs > 0 {
					cpus = defaults.CPUs
				} else {
					cpus = 1
				}
			}

			// Memory
			if !cmd.Flags().Changed("memory") {
				if defaults.MemoryMB > 0 {
					memory = defaults.MemoryMB
				} else {
					memory = 512
				}
			}

			// Disk
			if !cmd.Flags().Changed("disk") {
				if defaults.DiskSizeMB > 0 {
					disk = defaults.DiskSizeMB
				} else {
					disk = 1024
				}
			}

			// Image
			if !cmd.Flags().Changed("image") && defaults.Image != "" {
				imageName = defaults.Image
			}

			// Kernel
			if !cmd.Flags().Changed("kernel") && defaults.Kernel != "" {
				kernelName = defaults.Kernel
			}

			// SSH key path
			if !cmd.Flags().Changed("ssh-key") && defaults.SSHKeyPath != "" {
				sshKeyPath = defaults.SSHKeyPath
			}

			// DNS servers
			if !cmd.Flags().Changed("dns") && len(defaults.DNSServers) > 0 {
				dnsServers = defaults.DNSServers
			}

			// Create image manager for validation
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			// Validate image exists if specified
			if imageName != "" {
				if !imgMgr.ImageExists(imageName) {
					return fmt.Errorf("image '%s' not found. Use 'vmm image list' to see available images", imageName)
				}
			}

			// Validate kernel exists if specified
			if kernelName != "" {
				if !imgMgr.KernelExists(kernelName) {
					return fmt.Errorf("kernel '%s' not found. Use 'vmm kernel list' to see available kernels", kernelName)
				}
			}

			// Parse mount specifications
			var vmMounts []vm.Mount
			for _, mountSpec := range mounts {
				parsedMount, err := mount.ParseMountSpec(mountSpec)
				if err != nil {
					return fmt.Errorf("invalid mount specification: %w", err)
				}
				vmMounts = append(vmMounts, *parsedMount)
			}

			// Create new VM
			newVM := vm.NewVM(name)
			newVM.CPUs = cpus
			newVM.MemoryMB = memory
			newVM.DiskSizeMB = disk
			newVM.Image = imageName
			newVM.Kernel = kernelName
			newVM.MacAddress = newVM.GenerateMacAddress()
			newVM.TapDevice = network.GenerateTapName(newVM.ID)
			newVM.DNSServers = dnsServers
			newVM.Mounts = vmMounts

			// Set paths
			newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, name)

			// Read SSH public key if provided
			if sshKeyPath != "" {
				// Expand ~ to home directory
				if len(sshKeyPath) > 0 && sshKeyPath[0] == '~' {
					home, _ := os.UserHomeDir()
					if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
						home = "/home/" + sudoUser
					}
					sshKeyPath = home + sshKeyPath[1:]
				}
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
			if newVM.Kernel != "" {
				fmt.Printf("  Kernel: %s\n", newVM.Kernel)
			}
			fmt.Printf("  TAP device: %s, MAC: %s\n", newVM.TapDevice, newVM.MacAddress)
			if newVM.SSHPublicKey != "" {
				fmt.Printf("  SSH key: configured\n")
			}
			if len(newVM.DNSServers) > 0 {
				fmt.Printf("  DNS servers: %v\n", newVM.DNSServers)
			}
			if len(newVM.Mounts) > 0 {
				fmt.Printf("  Mounts:\n")
				for _, m := range newVM.Mounts {
					mode := "rw"
					if m.ReadOnly {
						mode = "ro"
					}
					fmt.Printf("    - %s -> /mnt/%s (%s)\n", m.HostPath, m.GuestTag, mode)
				}
			}
			return nil
		},
	}

	cmd.Flags().IntVar(&cpus, "cpus", 0, "Number of vCPUs")
	cmd.Flags().IntVar(&memory, "memory", 0, "Memory in MB")
	cmd.Flags().IntVar(&disk, "disk", 0, "Disk size in MB")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to SSH public key file for root access")
	cmd.Flags().StringSliceVar(&dnsServers, "dns", nil, "Custom DNS servers (can be specified multiple times)")
	cmd.Flags().StringVar(&imageName, "image", "", "Name of rootfs image to use (from 'vmm image import')")
	cmd.Flags().StringVar(&kernelName, "kernel", "", "Name of kernel to use (from 'vmm kernel import')")
	cmd.Flags().StringArrayVar(&mounts, "mount", nil, "Mount host directory in VM (format: /host/path:tag[:ro|rw])")

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

			// Delete mount images
			if len(existingVM.Mounts) > 0 {
				mountMgr := mount.NewManager(paths.Mounts)
				if err := mountMgr.DeleteAllMountImages(name, existingVM.Mounts); err != nil {
					fmt.Printf("Warning: failed to delete mount images: %v\n", err)
				}
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

			// Set kernel path based on custom kernel or default
			existingVM.KernelPath = imgMgr.GetKernelPath(existingVM.Kernel)

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

			// Create mount images and configure fstab
			var mountDrives []firecracker.MountDrive
			if len(existingVM.Mounts) > 0 {
				fmt.Println("Creating mount images...")
				mountMgr := mount.NewManager(paths.Mounts)

				// Create mount images and collect drive configs
				var mountEntries []image.MountEntry
				for i := range existingVM.Mounts {
					m := &existingVM.Mounts[i]
					if err := mountMgr.CreateMountImage(m, name); err != nil {
						return fmt.Errorf("failed to create mount image for '%s': %w", m.GuestTag, err)
					}

					// Device names: vdb, vdc, vdd, etc. (vda is rootfs)
					deviceLetter := string(rune('b' + i))
					device := fmt.Sprintf("/dev/vd%s", deviceLetter)
					mountPath := fmt.Sprintf("/mnt/%s", m.GuestTag)

					mountEntries = append(mountEntries, image.MountEntry{
						Device:    device,
						MountPath: mountPath,
						ReadOnly:  m.ReadOnly,
					})

					mountDrives = append(mountDrives, firecracker.MountDrive{
						ImagePath: m.ImagePath,
						Tag:       m.GuestTag,
						ReadOnly:  m.ReadOnly,
					})
				}

				// Inject fstab entries for mounts
				fmt.Println("Configuring mount points in guest...")
				if err := image.InjectMountFstab(existingVM.RootfsPath, mountEntries); err != nil {
					return fmt.Errorf("failed to inject mount fstab: %w", err)
				}

				// Save updated mount image paths
				existingVM.Save(paths.VMs)
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
				SocketPath:  existingVM.SocketPath,
				KernelPath:  existingVM.KernelPath,
				RootfsPath:  existingVM.RootfsPath,
				CPUs:        existingVM.CPUs,
				MemoryMB:    existingVM.MemoryMB,
				TapDevice:   existingVM.TapDevice,
				MacAddress:  existingVM.MacAddress,
				LogPath:     fmt.Sprintf("%s/%s.log", paths.Logs, name),
				IPAddress:   existingVM.IPAddress,
				Gateway:     cfg.Gateway,
				MountDrives: mountDrives,
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

func kernelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "kernel",
		Short: "Manage VM kernels",
	}

	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List available kernels",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			kernels, err := imgMgr.ListKernelsWithInfo()
			if err != nil {
				return fmt.Errorf("failed to list kernels: %w", err)
			}

			if len(kernels) == 0 {
				fmt.Println("No kernels found. Run 'vmm image pull' to download the default kernel.")
				return nil
			}

			fmt.Println("Available kernels:")
			for _, k := range kernels {
				sizeMB := float64(k.Size) / (1024 * 1024)
				defaultMarker := ""
				if k.IsDefault {
					defaultMarker = " (default)"
				}
				fmt.Printf("  - %s%s (%.2f MB)\n", k.Name, defaultMarker, sizeMB)
			}

			return nil
		},
	}

	var forceImport bool
	importCmd := &cobra.Command{
		Use:   "import <path>",
		Short: "Import a custom kernel binary",
		Long: `Import a custom kernel binary (vmlinux format).

The kernel must be an uncompressed vmlinux ELF binary compatible with
Firecracker. The architecture must match the host system.

Examples:
  vmm kernel import /path/to/vmlinux --name my-kernel
  vmm kernel import ./vmlinux-6.1 --name kernel-6.1 --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			srcPath := args[0]
			name, _ := cmd.Flags().GetString("name")

			if name == "" {
				return fmt.Errorf("--name is required")
			}

			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			if err := imgMgr.ImportKernel(srcPath, name, forceImport); err != nil {
				return err
			}

			return nil
		},
	}
	importCmd.Flags().String("name", "", "Name for the imported kernel (required)")
	importCmd.Flags().BoolVarP(&forceImport, "force", "f", false, "Overwrite existing kernel with same name")
	importCmd.MarkFlagRequired("name")

	deleteCmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a custom kernel",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			// Check if any VMs are using this kernel
			vms, _ := vm.List(paths.VMs)
			var usingVMs []string
			for _, v := range vms {
				if v.Kernel == name {
					usingVMs = append(usingVMs, v.Name)
				}
			}
			if len(usingVMs) > 0 {
				fmt.Printf("Warning: The following VMs are using kernel '%s': %v\n", name, usingVMs)
				fmt.Println("These VMs will fail to start if the kernel is deleted.")
			}

			if err := imgMgr.DeleteKernel(name); err != nil {
				return err
			}

			fmt.Printf("Deleted kernel '%s'\n", name)
			return nil
		},
	}

	var buildVersion string
	var buildName string
	buildCmd := &cobra.Command{
		Use:   "build",
		Short: "Build a kernel from source",
		Long: `Build a Firecracker-compatible kernel from source.

This command runs the build-kernel.sh script to compile a Linux kernel
configured for Firecracker. Requires build dependencies to be installed.

Supported kernel versions: 5.10, 6.1, 6.6

Examples:
  vmm kernel build --version 6.1 --name kernel-6.1
  vmm kernel build --version 5.10 --name kernel-lts`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if buildVersion == "" {
				return fmt.Errorf("--version is required (e.g., 5.10, 6.1, 6.6)")
			}
			if buildName == "" {
				return fmt.Errorf("--name is required")
			}

			// Find the build script
			scriptPath := "/usr/local/share/vmm/build-kernel.sh"
			if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
				// Try relative path for development
				scriptPath = "scripts/build-kernel.sh"
				if _, err := os.Stat(scriptPath); os.IsNotExist(err) {
					return fmt.Errorf("build-kernel.sh not found. Please install it to /usr/local/share/vmm/")
				}
			}

			paths := cfg.GetPaths()
			buildArgs := []string{scriptPath, "--version", buildVersion, "--name", buildName, "--output", paths.Kernels}

			fmt.Printf("Building kernel %s as '%s'...\n", buildVersion, buildName)
			fmt.Println("This may take a while depending on your system.")

			buildExec := exec.Command("bash", buildArgs...)
			buildExec.Stdout = os.Stdout
			buildExec.Stderr = os.Stderr
			buildExec.Stdin = os.Stdin

			if err := buildExec.Run(); err != nil {
				return fmt.Errorf("kernel build failed: %w", err)
			}

			fmt.Printf("\nKernel '%s' built successfully.\n", buildName)
			return nil
		},
	}
	buildCmd.Flags().StringVar(&buildVersion, "version", "", "Kernel version to build (e.g., 5.10, 6.1, 6.6)")
	buildCmd.Flags().StringVar(&buildName, "name", "", "Name for the built kernel (required)")
	buildCmd.MarkFlagRequired("version")
	buildCmd.MarkFlagRequired("name")

	cmd.AddCommand(listCmd, importCmd, deleteCmd, buildCmd)
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

func mountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Manage VM directory mounts",
	}

	syncCmd := &cobra.Command{
		Use:   "sync <vm-name> <tag>",
		Short: "Sync a mount image from host directory",
		Long: `Refresh a mount image with the current contents of the host directory.

This command updates the ext4 image used for the mount with the latest
files from the host directory. The VM should be stopped when syncing.

Example:
  vmm mount sync myvm code`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			tag := args[1]
			paths := cfg.GetPaths()

			// Load VM
			existingVM, err := vm.Load(paths.VMs, vmName)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", vmName)
			}

			// Check if VM is running
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)
			if existingVM.State == vm.StateRunning {
				return fmt.Errorf("VM '%s' is running. Stop it before syncing mounts", vmName)
			}

			// Find the mount with the given tag
			var targetMount *vm.Mount
			for i := range existingVM.Mounts {
				if existingVM.Mounts[i].GuestTag == tag {
					targetMount = &existingVM.Mounts[i]
					break
				}
			}

			if targetMount == nil {
				return fmt.Errorf("mount '%s' not found in VM '%s'", tag, vmName)
			}

			// Sync the mount
			fmt.Printf("Syncing mount '%s' for VM '%s'...\n", tag, vmName)
			mountMgr := mount.NewManager(paths.Mounts)
			if err := mountMgr.SyncMountImage(targetMount, vmName); err != nil {
				return fmt.Errorf("failed to sync mount: %w", err)
			}

			// Save updated mount image path
			existingVM.Save(paths.VMs)

			fmt.Printf("Mount '%s' synced successfully\n", tag)
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:   "list <vm-name>",
		Short: "List mounts for a VM",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			paths := cfg.GetPaths()

			// Load VM
			existingVM, err := vm.Load(paths.VMs, vmName)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", vmName)
			}

			if len(existingVM.Mounts) == 0 {
				fmt.Printf("VM '%s' has no mounts configured\n", vmName)
				return nil
			}

			fmt.Printf("Mounts for VM '%s':\n", vmName)
			for i, m := range existingVM.Mounts {
				mode := "rw"
				if m.ReadOnly {
					mode = "ro"
				}
				deviceLetter := string(rune('b' + i))
				fmt.Printf("  %s: %s -> /mnt/%s (%s) [/dev/vd%s]\n",
					m.GuestTag, m.HostPath, m.GuestTag, mode, deviceLetter)
				if m.ImagePath != "" {
					fmt.Printf("       Image: %s\n", m.ImagePath)
				}
			}
			return nil
		},
	}

	cmd.AddCommand(syncCmd, listCmd)
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

				// Set kernel path based on custom kernel or default
				v.KernelPath = imgMgr.GetKernelPath(v.Kernel)

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

				// Create mount images and configure fstab
				var mountDrives []firecracker.MountDrive
				if len(v.Mounts) > 0 {
					mountMgr := mount.NewManager(paths.Mounts)
					var mountEntries []image.MountEntry
					for j := range v.Mounts {
						m := &v.Mounts[j]
						if err := mountMgr.CreateMountImage(m, v.Name); err != nil {
							fmt.Printf("  Warning: failed to create mount image for '%s': %v\n", m.GuestTag, err)
							continue
						}
						deviceLetter := string(rune('b' + j))
						device := fmt.Sprintf("/dev/vd%s", deviceLetter)
						mountPath := fmt.Sprintf("/mnt/%s", m.GuestTag)
						mountEntries = append(mountEntries, image.MountEntry{
							Device:    device,
							MountPath: mountPath,
							ReadOnly:  m.ReadOnly,
						})
						mountDrives = append(mountDrives, firecracker.MountDrive{
							ImagePath: m.ImagePath,
							Tag:       m.GuestTag,
							ReadOnly:  m.ReadOnly,
						})
					}
					if len(mountEntries) > 0 {
						if err := image.InjectMountFstab(v.RootfsPath, mountEntries); err != nil {
							fmt.Printf("  Warning: failed to inject mount fstab: %v\n", err)
						}
					}
					v.Save(paths.VMs)
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
					SocketPath:  v.SocketPath,
					KernelPath:  v.KernelPath,
					RootfsPath:  v.RootfsPath,
					CPUs:        v.CPUs,
					MemoryMB:    v.MemoryMB,
					TapDevice:   v.TapDevice,
					MacAddress:  v.MacAddress,
					LogPath:     fmt.Sprintf("%s/%s.log", paths.Logs, v.Name),
					IPAddress:   v.IPAddress,
					Gateway:     cfg.Gateway,
					MountDrives: mountDrives,
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
