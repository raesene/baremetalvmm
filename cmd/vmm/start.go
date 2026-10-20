package main

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/lifecycle"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func startCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "start <name>",
		Short:             "Start a microVM",
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
			hypervisor.UpdateVMState(existingVM)

			if existingVM.State == vm.StateRunning {
				return fmt.Errorf("VM '%s' is already running", name)
			}

			fmt.Printf("Starting VM '%s'...\n", name)

			// The only warning a strict start can give once the VM is up is a
			// port forward that wouldn't go in, so don't list forwards then.
			warned := false
			err = lifecycle.Start(cfg, existingVM, lifecycle.Options{
				Progress: func(msg string) { fmt.Println(msg) },
				Warn: func(msg string) {
					warned = true
					fmt.Printf("Warning: %s\n", msg)
				},
			})
			if err != nil {
				return err
			}

			fmt.Printf("VM '%s' started successfully\n", name)
			fmt.Printf("  IP Address: %s\n", existingVM.IPAddress)
			fmt.Printf("  PID: %d\n", existingVM.PID)
			if existingVM.RestrictLocalNetworks {
				fmt.Println("  Local networks: restricted (internet only)")
			}
			if !warned {
				printPortForwards(existingVM)
			}
			fmt.Printf("  Socket: %s\n", existingVM.SocketPath)

			return nil
		},
	}
}
