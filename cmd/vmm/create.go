package main

import (
	"fmt"
	"os"

	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

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
			if err := validate.VMName(name); err != nil {
				return err
			}

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

			// Validate resource bounds
			if err := validate.CPUs(cpus); err != nil {
				return err
			}
			if err := validate.MemoryMB(memory); err != nil {
				return err
			}
			if err := validate.DiskSizeMB(disk); err != nil {
				return err
			}
			for _, dns := range dnsServers {
				if err := validate.DNSServer(dns); err != nil {
					return err
				}
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
				sshKeyPath = expandHomePath(sshKeyPath)
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
	cmd.RegisterFlagCompletionFunc("kernel", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeKernelNames(cmd, nil, toComplete)
	})
	cmd.RegisterFlagCompletionFunc("image", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeImageNames(cmd, nil, toComplete)
	})

	return cmd
}
