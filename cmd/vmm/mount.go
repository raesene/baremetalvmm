package main

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func mountCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mount",
		Short: "Manage VM directory mounts",
	}

	syncCmd := &cobra.Command{
		Use:               "sync <vm-name> <tag>",
		Short:             "Sync a mount image from host directory",
		ValidArgsFunction: completeVMNames,
		Long: `Refresh a mount image with the current contents of the host directory.

This command updates the ext4 image used for the mount with the latest
files from the host directory. The VM should be stopped when syncing.

Example:
  vmm mount sync myvm code`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			tag := args[1]
			if err := validate.MountTag(tag); err != nil {
				return err
			}
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
		Use:               "list <vm-name>",
		Short:             "List mounts for a VM",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName := args[0]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
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
