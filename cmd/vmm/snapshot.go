package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/snapshot"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func snapshotCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "snapshot",
		Short:   "Manage point-in-time VM snapshots",
		Long:    "Create, list, restore, and delete full snapshots (memory + disk) of microVMs.",
		Aliases: []string{"snap"},
	}

	cmd.AddCommand(
		snapshotCreateCmd(),
		snapshotListCmd(),
		snapshotRestoreCmd(),
		snapshotShowCmd(),
		snapshotDeleteCmd(),
	)

	return cmd
}

func snapshotCreateCmd() *cobra.Command {
	var stop bool

	cmd := &cobra.Command{
		Use:               "create <vm> <snapshot-name>",
		Short:             "Create a snapshot of a running VM",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName, snapName := args[0], args[1]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			if err := validate.SnapshotName(snapName); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			v, err := vm.Load(paths.VMs, vmName)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", vmName)
			}

			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(v)
			if v.State != vm.StateRunning {
				return fmt.Errorf("VM '%s' is not running (state: %s); snapshots capture a live VM's memory. Use 'vmm image snapshot' for a rootfs-only image", vmName, v.State)
			}

			snapMgr := snapshot.NewManager(paths.Snapshots)
			if snapMgr.Exists(vmName, snapName) {
				return fmt.Errorf("snapshot '%s' already exists for VM '%s'", snapName, vmName)
			}

			fmt.Printf("Creating snapshot '%s' of VM '%s' (this pauses the VM briefly)...\n", snapName, vmName)
			ctx := context.Background()
			meta, err := snapMgr.Create(ctx, fcClient, v, snapName, !stop)
			if err != nil {
				return fmt.Errorf("failed to create snapshot: %w", err)
			}

			if stop {
				// The VM was left paused by Create; fully stop it now.
				fmt.Printf("Stopping VM '%s'...\n", vmName)
				v.State = vm.StateStopping
				v.Save(paths.VMs)
				if err := fcClient.Terminate(ctx, v); err != nil {
					v.State = vm.StateError
					v.Save(paths.VMs)
					return fmt.Errorf("snapshot created, but failed to stop VM '%s': %w", vmName, err)
				}
				netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
				if v.TapDevice != "" && netMgr.TapExists(v.TapDevice) {
					if err := netMgr.DeleteTap(v.TapDevice); err != nil {
						fmt.Printf("Warning: failed to delete TAP device: %v\n", err)
					}
				}
				for _, pf := range v.PortForwards {
					if v.IPAddress != "" {
						if err := netMgr.RemovePortForward(pf.HostPort, pf.GuestPort, v.IPAddress, pf.Protocol); err != nil {
							fmt.Printf("Warning: failed to remove port forward %d:%d: %v\n", pf.HostPort, pf.GuestPort, err)
						}
					}
				}
				v.State = vm.StateStopped
				v.Save(paths.VMs)
			}

			fmt.Printf("Snapshot '%s' created (%.1f MB)\n", snapName, float64(meta.SizeBytes)/(1024*1024))
			return nil
		},
	}

	cmd.Flags().BoolVar(&stop, "stop", false, "Stop the VM after taking the snapshot instead of resuming it")

	return cmd
}

func snapshotListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "list [vm]",
		Short:             "List snapshots for a VM (or all VMs)",
		Aliases:           []string{"ls"},
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()
			vmName := ""
			if len(args) == 1 {
				vmName = args[0]
				if err := validate.VMName(vmName); err != nil {
					return err
				}
			}

			snapMgr := snapshot.NewManager(paths.Snapshots)
			snaps, err := snapMgr.List(vmName)
			if err != nil {
				return fmt.Errorf("failed to list snapshots: %w", err)
			}
			if len(snaps) == 0 {
				fmt.Println("No snapshots found. Create one with: vmm snapshot create <vm> <name>")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintln(w, "VM\tSNAPSHOT\tCREATED\tSIZE\tMEMORY")
			for _, s := range snaps {
				fmt.Fprintf(w, "%s\t%s\t%s\t%.1f MB\t%d MB\n",
					s.VMName, s.Name, s.CreatedAt.Format("2006-01-02 15:04:05"),
					float64(s.SizeBytes)/(1024*1024), s.MemoryMB)
			}
			w.Flush()
			return nil
		},
	}
	return cmd
}

func snapshotRestoreCmd() *cobra.Command {
	var force bool
	var noStart bool

	cmd := &cobra.Command{
		Use:               "restore <vm> <snapshot-name>",
		Short:             "Restore a VM in place to one of its snapshots",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName, snapName := args[0], args[1]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			if err := validate.SnapshotName(snapName); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			v, err := vm.Load(paths.VMs, vmName)
			if err != nil {
				return fmt.Errorf("VM '%s' not found", vmName)
			}

			snapMgr := snapshot.NewManager(paths.Snapshots)
			meta, err := snapMgr.Get(vmName, snapName)
			if err != nil {
				return err
			}

			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(v)

			ctx := context.Background()
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)

			// The VM must be stopped before its disks are overwritten.
			if v.State == vm.StateRunning {
				if !force {
					return fmt.Errorf("VM '%s' is running; stop it first or use --force to stop and restore", vmName)
				}
				fmt.Printf("Stopping VM '%s'...\n", vmName)
				v.State = vm.StateStopping
				v.Save(paths.VMs)
				if err := fcClient.Terminate(ctx, v); err != nil {
					v.State = vm.StateError
					v.Save(paths.VMs)
					return fmt.Errorf("failed to stop VM '%s' before restore: %w", vmName, err)
				}
				if v.TapDevice != "" && netMgr.TapExists(v.TapDevice) {
					if err := netMgr.DeleteTap(v.TapDevice); err != nil {
						fmt.Printf("Warning: failed to delete TAP device: %v\n", err)
					}
				}
				v.State = vm.StateStopped
				v.Save(paths.VMs)
			}

			if fcVer := fcClient.Version(); fcVer != "" && meta.FCVersion != "" && fcVer != meta.FCVersion {
				fmt.Printf("Warning: snapshot was taken with Firecracker %s but %s is installed; restore may fail.\n", meta.FCVersion, fcVer)
			}

			// Restore uses the snapshot's resource configuration.
			v.CPUs = meta.CPUs
			v.MemoryMB = meta.MemoryMB

			fmt.Printf("Restoring VM '%s' from snapshot '%s'...\n", vmName, snapName)
			logPath := fmt.Sprintf("%s/%s.log", paths.Logs, vmName)
			if _, err := snapMgr.Restore(ctx, fcClient, netMgr, v, snapName, logPath, !noStart); err != nil {
				v.State = vm.StateError
				v.Save(paths.VMs)
				return fmt.Errorf("failed to restore snapshot: %w", err)
			}

			if noStart {
				v.State = vm.StateStopped
				v.Save(paths.VMs)
				fmt.Printf("VM '%s' disks restored from snapshot '%s' (VM left stopped)\n", vmName, snapName)
				return nil
			}

			v.Save(paths.VMs)
			fmt.Printf("VM '%s' restored from snapshot '%s' and resumed\n", vmName, snapName)
			fmt.Printf("  IP Address: %s\n", v.IPAddress)
			fmt.Printf("  PID: %d\n", v.PID)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Stop the VM first if it is running")
	cmd.Flags().BoolVar(&noStart, "no-start", false, "Restore disks only; do not start the VM from the snapshot")

	return cmd
}

func snapshotShowCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "show <vm> <snapshot-name>",
		Short:             "Show details of a snapshot",
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName, snapName := args[0], args[1]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			if err := validate.SnapshotName(snapName); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			snapMgr := snapshot.NewManager(paths.Snapshots)
			s, err := snapMgr.Get(vmName, snapName)
			if err != nil {
				return err
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
			fmt.Fprintf(w, "Name:\t%s\n", s.Name)
			fmt.Fprintf(w, "VM:\t%s (%s)\n", s.VMName, s.VMID)
			fmt.Fprintf(w, "Created:\t%s\n", s.CreatedAt.Format("2006-01-02 15:04:05"))
			fmt.Fprintf(w, "Firecracker:\t%s\n", s.FCVersion)
			fmt.Fprintf(w, "CPUs:\t%d\n", s.CPUs)
			fmt.Fprintf(w, "Memory:\t%d MB\n", s.MemoryMB)
			fmt.Fprintf(w, "IP Address:\t%s\n", s.IPAddress)
			fmt.Fprintf(w, "Size:\t%.1f MB\n", float64(s.SizeBytes)/(1024*1024))
			if len(s.Mounts) > 0 {
				fmt.Fprintf(w, "Mounts:\t%d\n", len(s.Mounts))
			}
			w.Flush()
			return nil
		},
	}
	return cmd
}

func snapshotDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "delete <vm> <snapshot-name>",
		Short:             "Delete a snapshot",
		Aliases:           []string{"rm"},
		Args:              cobra.ExactArgs(2),
		ValidArgsFunction: completeVMNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			vmName, snapName := args[0], args[1]
			if err := validate.VMName(vmName); err != nil {
				return err
			}
			if err := validate.SnapshotName(snapName); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			snapMgr := snapshot.NewManager(paths.Snapshots)
			if err := snapMgr.Delete(vmName, snapName); err != nil {
				return err
			}
			fmt.Printf("Deleted snapshot '%s' for VM '%s'\n", snapName, vmName)
			return nil
		},
	}
	return cmd
}
