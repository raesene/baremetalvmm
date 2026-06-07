package main

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

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
				fmt.Printf("  - %-20s %6.1f MB  %s%s\n", k.Name, sizeMB, k.Description, defaultMarker)
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
			if err := validate.KernelName(name); err != nil {
				return err
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
		Use:               "delete <name>",
		Short:             "Delete a custom kernel",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeKernelNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.KernelName(name); err != nil {
				return err
			}
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
			if err := validate.KernelName(buildName); err != nil {
				return err
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
