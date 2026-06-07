package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func stopCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "stop <name>",
		Short:             "Stop a microVM",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
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
				if existingVM.PID > 0 && firecracker.IsFirecrackerProcess(existingVM.PID) {
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

			for _, pf := range existingVM.PortForwards {
				if existingVM.IPAddress != "" {
					if err := netMgr.RemovePortForward(pf.HostPort, pf.GuestPort, existingVM.IPAddress, pf.Protocol); err != nil {
						fmt.Printf("Warning: failed to remove port forward %d:%d: %v\n", pf.HostPort, pf.GuestPort, err)
					}
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
