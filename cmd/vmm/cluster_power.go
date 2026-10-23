package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/lifecycle"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func clusterStartCmd() *cobra.Command {
	var noWait bool
	var timeout time.Duration

	cmd := &cobra.Command{
		Use:               "start <name>",
		Short:             "Start a cluster's VMs (control plane first) and wait for its nodes",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClusterNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.ClusterName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			cl, err := cluster.Load(paths.Clusters, name)
			if err != nil {
				return fmt.Errorf("cluster '%s' not found", name)
			}
			if cl.State == cluster.StateCreating || cl.ControlPlaneIP == "" {
				return fmt.Errorf("cluster '%s' was never provisioned (state: %s)", name, cl.State)
			}

			// Control plane first, so the kubelets find the API server.
			started := map[string]time.Time{}
			for _, vmName := range cl.AllVMs() {
				v, err := vm.Load(paths.VMs, vmName)
				if err != nil {
					return fmt.Errorf("VM '%s' of cluster '%s' not found", vmName, name)
				}
				hypervisor.UpdateVMState(v)
				if v.State == vm.StateRunning {
					fmt.Printf("VM '%s' is already running\n", vmName)
					continue
				}
				fmt.Printf("Starting VM '%s'...\n", vmName)
				started[vmName] = time.Now()
				if err := lifecycle.Start(cfg, v, lifecycle.Options{
					Warn: func(msg string) { fmt.Printf("  Warning: %s\n", msg) },
				}); err != nil {
					return fmt.Errorf("failed to start VM '%s': %w", vmName, err)
				}
			}

			cl.State = cluster.StateRunning
			if err := cl.Save(paths.Clusters); err != nil {
				fmt.Printf("Warning: failed to save cluster state: %v\n", err)
			}

			if noWait || len(started) == 0 {
				fmt.Printf("Cluster '%s' started\n", name)
				return nil
			}

			fmt.Println("Waiting for nodes to report Ready...")
			client, err := cluster.WaitForSSH(cl.ControlPlaneIP, clusterSSHKeys(cl, paths.SSH), 2*time.Minute)
			if err != nil {
				return fmt.Errorf("failed to connect to control plane: %w", err)
			}
			defer client.Close()
			if err := cluster.WaitForNodesAfterStart(cl, client, started, timeout); err != nil {
				return err
			}

			fmt.Printf("Cluster '%s' started, all nodes Ready\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVar(&noWait, "no-wait", false, "Don't wait for the nodes to report Ready")
	cmd.Flags().DurationVar(&timeout, "timeout", 5*time.Minute, "How long to wait for the nodes to report Ready")
	return cmd
}

func clusterStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "stop <name>",
		Short:             "Stop a cluster's VMs (workers first, then the control plane)",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: completeClusterNames,
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.ClusterName(name); err != nil {
				return err
			}
			paths := cfg.GetPaths()

			cl, err := cluster.Load(paths.Clusters, name)
			if err != nil {
				return fmt.Errorf("cluster '%s' not found", name)
			}

			// Workers (and the admin workstation) before the control plane,
			// so the kubelets don't spend their shutdown failing to reach it.
			first := cl.WorkerVMs
			if cl.AdminVM != "" {
				first = append(append([]string{}, first...), cl.AdminVM)
			}
			var failed []string
			for _, group := range [][]string{first, {cl.ControlPlaneVM}} {
				failed = append(failed, stopVMsParallel(paths.VMs, group)...)
			}
			if len(failed) > 0 {
				return fmt.Errorf("could not stop VMs %v of cluster '%s'", failed, name)
			}

			cl.State = cluster.StateStopped
			if err := cl.Save(paths.Clusters); err != nil {
				fmt.Printf("Warning: failed to save cluster state: %v\n", err)
			}
			fmt.Printf("Cluster '%s' stopped\n", name)
			return nil
		},
	}
	return cmd
}

// stopVMsParallel stops the running VMs among names in parallel, each with
// the usual clean shutdown before signals, and returns those it couldn't stop.
func stopVMsParallel(vmsDir string, names []string) []string {
	var running []*vm.VM
	for _, vmName := range names {
		v, err := vm.Load(vmsDir, vmName)
		if err != nil {
			fmt.Printf("Warning: VM '%s' not found, skipping\n", vmName)
			continue
		}
		hypervisor.UpdateVMState(v)
		if v.State != vm.StateRunning {
			continue
		}
		fmt.Printf("Stopping VM '%s'...\n", vmName)
		v.State = vm.StateStopping
		if err := v.Save(vmsDir); err != nil {
			fmt.Printf("Warning: failed to save VM state: %v\n", err)
		}
		running = append(running, v)
	}

	errs := make([]error, len(running))
	var wg sync.WaitGroup
	for i, v := range running {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = hypervisor.Terminate(context.Background(), v)
		}()
	}
	wg.Wait()

	// TAP and port forward cleanup one VM at a time: iptables calls can't
	// safely overlap.
	netMgr := newNetManager()
	var failed []string
	for i, v := range running {
		if errs[i] != nil {
			fmt.Printf("Warning: failed to stop VM '%s': %v\n", v.Name, errs[i])
			v.State = vm.StateError
			if err := v.Save(vmsDir); err != nil {
				fmt.Printf("Warning: failed to save VM state: %v\n", err)
			}
			failed = append(failed, v.Name)
			continue
		}
		finishStop(netMgr, v)
		fmt.Printf("VM '%s' stopped\n", v.Name)
	}
	return failed
}

// clusterSSHKeys returns the private keys for SSH to a cluster's VMs: the key
// that provisioned it, then vmm's managed key (see cluster.SSHKeys).
func clusterSSHKeys(cl *cluster.Cluster, sshDir string) []string {
	return cluster.SSHKeys(expandHomePath(strings.TrimSuffix(cl.SSHKeyPath, ".pub")), sshDir)
}
