package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func deleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "delete <name>",
		Short:             "Delete a microVM",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
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
					// Try to kill by PID as fallback
					if existingVM.PID > 0 && firecracker.IsFirecrackerProcess(existingVM.PID) {
						if proc, err := os.FindProcess(existingVM.PID); err == nil {
							proc.Signal(syscall.SIGKILL)
						}
					}
				}

				// Wait for process to exit
				time.Sleep(500 * time.Millisecond)

				// Verify process is gone, force kill if still running
				if existingVM.PID > 0 {
					if proc, err := os.FindProcess(existingVM.PID); err == nil {
						if err := proc.Signal(syscall.Signal(0)); err == nil {
							if firecracker.IsFirecrackerProcess(existingVM.PID) {
								fmt.Printf("Warning: process %d still running, sending SIGKILL...\n", existingVM.PID)
								proc.Signal(syscall.SIGKILL)
								time.Sleep(500 * time.Millisecond)
							}
						}
					}
				}
			}

			// Cleanup network resources
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
				if err := netMgr.DeleteTap(existingVM.TapDevice); err != nil {
					fmt.Printf("Warning: failed to delete TAP device: %v\n", err)
				}
			}

			for _, pf := range existingVM.PortForwards {
				if existingVM.IPAddress != "" {
					if err := netMgr.RemovePortForward(pf.HostPort, pf.GuestPort, existingVM.IPAddress, pf.Protocol); err != nil {
						fmt.Printf("Warning: failed to remove port forward %d:%d: %v\n", pf.HostPort, pf.GuestPort, err)
					}
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
