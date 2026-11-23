package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func listCmd() *cobra.Command {
	var all bool
	var jsonOutput bool

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

			// Update state for each VM
			for _, v := range vms {
				hypervisor.UpdateVMState(v)
			}

			if jsonOutput {
				out := []vm.VM{}
				for _, v := range vms {
					if all || v.State != vm.StateStopped {
						out = append(out, vmJSON(v))
					}
				}
				return writeJSON(os.Stdout, out)
			}

			if len(vms) == 0 {
				fmt.Println("No VMs found. Create one with: vmm create <name>")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tID\tSTATE\tCPUs\tMEMORY\tIP ADDRESS\tBACKEND")
			for _, v := range vms {
				if !all && v.State == vm.StateStopped {
					continue
				}
				ip := v.IPAddress
				if ip == "" {
					ip = "-"
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d MB\t%s\t%s\n",
					v.Name, v.ID, v.State, v.CPUs, v.MemoryMB, ip, backendLabel(v))
			}
			w.Flush()

			return nil
		},
	}

	cmd.Flags().BoolVarP(&all, "all", "a", true, "Show all VMs including stopped")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "Output the VMs as JSON ([] when there are none)")

	return cmd
}

// backendLabel names the hypervisor a VM runs under, without spaces so the
// list stays easy to split into columns.
func backendLabel(v *vm.VM) string {
	if v.Nested {
		return hypervisor.Name(v) + "+nested"
	}
	return hypervisor.Name(v)
}
