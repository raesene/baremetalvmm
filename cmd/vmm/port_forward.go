package main

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func portForwardCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "port-forward",
		Short: "Manage VM port forwards",
	}

	addCmd := &cobra.Command{
		Use:               "add <name> <host-port>:<guest-port>",
		Short:             "Forward a port from host to VM",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			portSpec := args[1]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			if existingVM.IPAddress == "" {
				return fmt.Errorf("VM '%s' has no IP address (is it running?)", name)
			}

			var hostPort, guestPort int
			if _, err := fmt.Sscanf(portSpec, "%d:%d", &hostPort, &guestPort); err != nil {
				return fmt.Errorf("invalid port spec '%s', expected format: host-port:guest-port", portSpec)
			}
			if hostPort < 1 || hostPort > 65535 {
				return fmt.Errorf("invalid host port %d: must be 1-65535", hostPort)
			}
			if guestPort < 1 || guestPort > 65535 {
				return fmt.Errorf("invalid guest port %d: must be 1-65535", guestPort)
			}

			// Check if this port forward is already recorded on the VM
			for _, pf := range existingVM.PortForwards {
				if pf.HostPort == hostPort && pf.GuestPort == guestPort && pf.Protocol == "tcp" {
					fmt.Printf("Port forward already exists: %d -> %s:%d\n", hostPort, existingVM.IPAddress, guestPort)
					return nil
				}
			}

			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			if err := netMgr.AddPortForward(hostPort, guestPort, existingVM.IPAddress, "tcp"); err != nil {
				return fmt.Errorf("failed to add port forward: %w", err)
			}

			existingVM.PortForwards = append(existingVM.PortForwards, vm.PortForward{
				HostPort:  hostPort,
				GuestPort: guestPort,
				Protocol:  "tcp",
			})
			existingVM.Save(paths.VMs)

			fmt.Printf("Port forward added: %d -> %s:%d\n", hostPort, existingVM.IPAddress, guestPort)
			return nil
		},
	}

	listCmd := &cobra.Command{
		Use:               "list <name>",
		Short:             "List port forwards for a VM",
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

			if len(existingVM.PortForwards) == 0 {
				fmt.Printf("VM '%s' has no port forwards configured\n", name)
				return nil
			}

			fmt.Printf("Port forwards for VM '%s':\n", name)
			fmt.Printf("  %-12s %-12s %s\n", "HOST PORT", "GUEST PORT", "PROTOCOL")
			for _, pf := range existingVM.PortForwards {
				fmt.Printf("  %-12d %-12d %s\n", pf.HostPort, pf.GuestPort, pf.Protocol)
			}
			return nil
		},
	}

	removeCmd := &cobra.Command{
		Use:               "remove <name> <host-port>:<guest-port>",
		Short:             "Remove a port forward from a VM",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.VMName(name); err != nil {
				return err
			}
			portSpec := args[1]
			paths := cfg.GetPaths()

			existingVM, err := vm.Load(paths.VMs, name)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", name)
			}

			var hostPort, guestPort int
			if _, err := fmt.Sscanf(portSpec, "%d:%d", &hostPort, &guestPort); err != nil {
				return fmt.Errorf("invalid port spec '%s', expected format: host-port:guest-port", portSpec)
			}

			found := -1
			for i, pf := range existingVM.PortForwards {
				if pf.HostPort == hostPort && pf.GuestPort == guestPort {
					found = i
					break
				}
			}
			if found == -1 {
				return fmt.Errorf("port forward %d:%d not found on VM '%s'", hostPort, guestPort, name)
			}

			if existingVM.IPAddress != "" {
				netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
				protocol := existingVM.PortForwards[found].Protocol
				if err := netMgr.RemovePortForward(hostPort, guestPort, existingVM.IPAddress, protocol); err != nil {
					fmt.Printf("Warning: failed to remove iptables rule: %v\n", err)
				}
			}

			existingVM.PortForwards = append(existingVM.PortForwards[:found], existingVM.PortForwards[found+1:]...)
			existingVM.Save(paths.VMs)

			fmt.Printf("Port forward removed: %d:%d\n", hostPort, guestPort)
			return nil
		},
	}

	cmd.AddCommand(addCmd, listCmd, removeCmd)
	return cmd
}
