package main

import (
	"fmt"
	"os"

	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/lifecycle"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func setCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <name> <key> <value>",
		Short: "Change a setting on an existing microVM",
		Long: `Change a setting on an existing microVM.

Keys:
  autostart on|off                 Start the VM at boot (via vmm.service). New VMs default to on.
  restrict-local-networks on|off   Block the VM from the host, local/private networks and
                                   other VMs, leaving internet access. Applies immediately
                                   to a running VM. Needs nftables.`,
		Example: `  vmm set myvm autostart off
  vmm set myvm restrict-local-networks on`,
		Args:              cobra.ExactArgs(3),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name, key, value := args[0], args[1], args[2]
			if err := validate.VMName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			switch key {
			case "autostart":
				on, err := parseOnOff(value)
				if err != nil {
					return err
				}
				existingVM.AutoStart = on
			case "restrict-local-networks":
				on, err := parseOnOff(value)
				if err != nil {
					return err
				}
				if on {
					if err := lifecycle.CheckRestrictedDNS(existingVM.DNSServers); err != nil {
						return err
					}
				}
				existingVM.RestrictLocalNetworks = on
				if on && !network.NftAvailable() {
					fmt.Fprintln(os.Stderr, nftMissingWarning)
				}
				// A running VM changes now. Otherwise its next start applies it.
				hypervisor.UpdateVMState(existingVM)
				if existingVM.State == vm.StateRunning {
					if err := lifecycle.ApplyRestriction(newNetManager(), existingVM); err != nil {
						return fmt.Errorf("failed to update local network restriction: %w", err)
					}
				}
			default:
				return fmt.Errorf("unknown setting %q (supported: autostart, restrict-local-networks)", key)
			}

			if err := existingVM.Save(paths.VMs); err != nil {
				return fmt.Errorf("failed to save VM: %w", err)
			}
			fmt.Printf("VM '%s': %s set to %s\n", name, key, value)
			return nil
		},
	}
}

// parseOnOff accepts on/off, true/false, yes/no and 1/0.
func parseOnOff(s string) (bool, error) {
	switch s {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, fmt.Errorf("invalid value %q: use on or off", s)
}
