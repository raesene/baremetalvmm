package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/portforward"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func portForwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port-forward",
		Short: "Manage VM port forwards",
		Long: `Forward ports on the host to a VM.

Forwards can be added and removed while a VM is running and take effect
immediately. They are saved on the VM, removed from the host's firewall
while it is stopped, and put back when it starts.`,
	}

	var addUDP bool
	addCmd := &cobra.Command{
		Use:   "add <name> <host-port>:<guest-port>[/tcp|udp]",
		Short: "Forward a host port to a VM (works on running VMs)",
		Example: `  sudo vmm port-forward add myvm 8080:80
  sudo vmm port-forward add myvm 5353:53/udp`,
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			pf, err := portforward.Parse(args[1], defaultProtocol(addUDP))
			if err != nil {
				return err
			}
			paths := cfg.GetPaths()

			all, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}
			existingVM := findVM(all, name)
			if existingVM == nil {
				return fmt.Errorf("VM '%s' not found", name)
			}
			firecracker.NewClient().UpdateVMState(existingVM)

			live, err := portforward.Add(newNetManager(), all, existingVM, pf)
			if errors.Is(err, portforward.ErrExists) {
				fmt.Printf("Port forward %s already exists on VM '%s'\n", portforward.Format(pf), name)
				return nil
			}
			if err != nil {
				return fmt.Errorf("failed to add port forward: %w", err)
			}
			if err := existingVM.Save(paths.VMs); err != nil {
				if live {
					_ = newNetManager().RemovePortForward(pf.HostPort, pf.GuestPort, existingVM.IPAddress, pf.Protocol)
				}
				return fmt.Errorf("failed to save VM: %w", err)
			}

			if live {
				fmt.Printf("Port forward added: %s -> %s:%d (%s)\n", hostAddr(pf.HostPort), existingVM.IPAddress, pf.GuestPort, pf.Protocol)
			} else {
				fmt.Printf("Port forward %s saved; it will be applied when VM '%s' starts\n", portforward.Format(pf), name)
			}
			return nil
		},
	}
	addCmd.Flags().BoolVar(&addUDP, "udp", false, "Forward UDP instead of TCP")

	listCmd := &cobra.Command{
		Use:               "list [name]",
		Short:             "List port forwards for one VM, or every VM",
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()
			all, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}
			if len(args) == 1 {
				if err := validate.VMName(args[0]); err != nil {
					return err
				}
				v := findVM(all, args[0])
				if v == nil {
					return fmt.Errorf("VM '%s' not found", args[0])
				}
				all = []*vm.VM{v}
			}
			fcClient := firecracker.NewClient()
			for _, v := range all {
				fcClient.UpdateVMState(v)
			}

			entries := portforward.List(newNetManager(), all)
			if len(entries) == 0 {
				if len(args) == 1 {
					fmt.Printf("VM '%s' has no port forwards configured\n", args[0])
				} else {
					fmt.Println("No port forwards configured")
				}
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "HOST\tPROTO\tVM\tGUEST\tSTATUS")
			missing := false
			for _, e := range entries {
				guest := fmt.Sprintf("%s:%d", e.GuestIP, e.GuestPort)
				if e.GuestIP == "" {
					guest = fmt.Sprintf(":%d", e.GuestPort)
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", hostAddr(e.HostPort), e.Protocol, e.VMName, guest, e.Status)
				missing = missing || e.Status == portforward.StatusMissing || e.Status == portforward.StatusOutdated
			}
			w.Flush()
			if missing {
				fmt.Println("\n'missing' forwards aren't in the firewall; 'outdated' ones use an older vmm's rule.")
				fmt.Println("Fix either with 'sudo vmm port-forward apply <vm>' or by restarting the VM.")
			}
			return nil
		},
	}

	var removeUDP bool
	removeCmd := &cobra.Command{
		Use:               "remove <name> <host-port>:<guest-port>[/tcp|udp]",
		Short:             "Remove a port forward from a VM (works on running VMs)",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			// Without /proto or --udp, match either protocol so that
			// "remove myvm 5353:53" works for a lone udp forward.
			proto := ""
			if removeUDP {
				proto = "udp"
			}
			pf, err := portforward.Parse(args[1], "tcp")
			if err != nil {
				return err
			}
			if hasProtocolSuffix(args[1]) {
				proto = pf.Protocol
			}
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			i, err := portforward.Find(existingVM, pf.HostPort, pf.GuestPort, proto)
			if errors.Is(err, portforward.ErrNotFound) {
				return fmt.Errorf("port forward %d:%d not found on VM '%s'", pf.HostPort, pf.GuestPort, name)
			}
			if err != nil {
				return err
			}
			removed, err := portforward.Remove(newNetManager(), existingVM, i)
			if err != nil {
				return fmt.Errorf("failed to remove firewall rules: %w", err)
			}
			if err := existingVM.Save(paths.VMs); err != nil {
				return fmt.Errorf("firewall rules removed, but failed to save VM: %w", err)
			}

			fmt.Printf("Port forward removed: %s\n", portforward.Format(removed))
			return nil
		},
	}
	removeCmd.Flags().BoolVar(&removeUDP, "udp", false, "Remove a UDP forward")

	applyCmd := &cobra.Command{
		Use:               "apply <name>",
		Short:             "Re-install a running VM's port forwards in the firewall",
		Long:              "Re-install a running VM's saved port forwards. Use it for forwards 'list' shows as missing or outdated.",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			existingVM, err := vm.Load(cfg.GetPaths().VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}
			firecracker.NewClient().UpdateVMState(existingVM)
			if existingVM.State != vm.StateRunning {
				return fmt.Errorf("VM '%s' is not running; its forwards are applied when it starts", name)
			}
			if len(existingVM.PortForwards) == 0 {
				fmt.Printf("VM '%s' has no port forwards configured\n", name)
				return nil
			}
			if err := portforward.Apply(newNetManager(), existingVM); err != nil {
				return fmt.Errorf("failed to apply port forwards: %w", err)
			}
			fmt.Printf("Applied %d port forward(s) for VM '%s'\n", len(existingVM.PortForwards), name)
			return nil
		},
	}

	cmd.AddCommand(addCmd, listCmd, removeCmd, applyCmd)
	return cmd
}

func defaultProtocol(udp bool) string {
	if udp {
		return "udp"
	}
	return "tcp"
}

func hasProtocolSuffix(spec string) bool {
	return strings.Contains(spec, "/")
}

func findVM(all []*vm.VM, name string) *vm.VM {
	for _, v := range all {
		if v.Name == name {
			return v
		}
	}
	return nil
}

func newNetManager() *network.Manager {
	return network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
}

// hostAddr shows where a forwarded port can be reached from other machines.
func hostAddr(port int) string {
	if ip := network.InterfaceIPv4(cfg.HostInterface); ip != "" {
		return fmt.Sprintf("%s:%d", ip, port)
	}
	return fmt.Sprintf(":%d", port)
}

// applyPortForwards installs a started VM's saved forwards, warning on
// failures: the VM is up, so a broken forward shouldn't fail the start.
func applyPortForwards(netMgr *network.Manager, v *vm.VM) {
	if len(v.PortForwards) == 0 {
		return
	}
	if err := portforward.Apply(netMgr, v); err != nil {
		fmt.Printf("Warning: failed to apply port forwards: %v\n", err)
		return
	}
	for _, pf := range v.PortForwards {
		fmt.Printf("  Port forward: %s -> %s:%d (%s)\n", hostAddr(pf.HostPort), v.IPAddress, pf.GuestPort, pf.Protocol)
	}
}

// clearPortForwards removes a stopping VM's forwards from the firewall.
func clearPortForwards(netMgr *network.Manager, v *vm.VM) {
	if err := portforward.Clear(netMgr, v); err != nil {
		fmt.Printf("Warning: failed to remove port forwards: %v\n", err)
	}
}
