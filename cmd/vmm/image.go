package main

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

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
			kernels, _ := imgMgr.ListKernelsWithInfo()
			if len(kernels) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, k := range kernels {
					sizeMB := float64(k.Size) / (1024 * 1024)
					defaultMarker := ""
					if k.IsDefault {
						defaultMarker = " (default)"
					}
					fmt.Printf("  - %-20s %6.1f MB  %s%s\n", k.Name, sizeMB, k.Description, defaultMarker)
				}
			}

			fmt.Println("\nRoot filesystems:")
			rootfs, _ := imgMgr.ListRootfsWithInfo()
			if len(rootfs) == 0 {
				fmt.Println("  (none)")
			} else {
				for _, r := range rootfs {
					sizeMB := float64(r.Size) / (1024 * 1024)
					defaultMarker := ""
					if r.IsDefault {
						defaultMarker = " (default)"
					}
					fmt.Printf("  - %-20s %6.1f MB  %s%s\n", r.Name, sizeMB, r.Description, defaultMarker)
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
			if err := validate.ImageName(name); err != nil {
				return err
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
		Use:               "delete <name>",
		Short:             "Delete an imported image",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeImageNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.ImageName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			if err := imgMgr.DeleteImage(name); err != nil {
				return err
			}

			fmt.Printf("Deleted image '%s'\n", name)
			return nil
		},
	}

	snapshotCmd := &cobra.Command{
		Use:   "snapshot <vm-name> --name <image-name>",
		Short: "Snapshot a stopped VM's rootfs as a reusable base image",
		Long: `Snapshot a VM's root filesystem and save it as a new base image.

The VM must be stopped. The snapshot is shrunk to minimum size to save
disk space. New VMs created from this image will be resized to their
configured disk size at start time.

Examples:
  vmm image snapshot myvm --name my-template
  vmm create newvm --image my-template --ssh-key ~/.ssh/id_ed25519.pub`,
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			imageName, _ := cmd.Flags().GetString("name")

			if imageName == "" {
				return fmt.Errorf("--name is required")
			}
			if err := validate.ImageName(imageName); err != nil {
				return err
			}

			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, vmName)
			if err != nil {
				return fmt.Errorf("VM '%s' not found: %w", vmName, err)
			}

			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)
			if existingVM.State == vm.StateRunning {
				return fmt.Errorf("VM '%s' is running. Stop it first before taking a snapshot", vmName)
			}

			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			return imgMgr.SnapshotVMRootfs(vmName, paths.VMs, imageName)
		},
	}
	snapshotCmd.Flags().String("name", "", "Name for the snapshot image (required)")
	snapshotCmd.MarkFlagRequired("name")

	cmd.AddCommand(listCmd, pullCmd, importCmd, deleteCmd, snapshotCmd)
	return cmd
}
