package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

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
