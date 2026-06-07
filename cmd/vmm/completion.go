package main

import (
	"os"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func completeVMNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	paths := cfg.GetPaths()
	vms, err := vm.List(paths.VMs)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, v := range vms {
		names = append(names, v.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeClusterNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	paths := cfg.GetPaths()
	clusters, err := cluster.List(paths.Clusters)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, cl := range clusters {
		names = append(names, cl.Name)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func completeKernelNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	paths := cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	kernels, err := imgMgr.ListKernels()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	return kernels, cobra.ShellCompDirectiveNoFileComp
}

func completeImageNames(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) != 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	paths := cfg.GetPaths()
	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	rootfs, err := imgMgr.ListRootfs()
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	var names []string
	for _, r := range rootfs {
		if len(r) > 5 && r[len(r)-5:] == ".ext4" {
			r = r[:len(r)-5]
		}
		names = append(names, r)
	}
	return names, cobra.ShellCompDirectiveNoFileComp
}

func usedVMIPs(vmsDir string) []string {
	vms, _ := vm.List(vmsDir)
	var ips []string
	for _, v := range vms {
		if v.IPAddress != "" {
			ips = append(ips, v.IPAddress)
		}
	}
	return ips
}

func expandHomePath(path string) string {
	if len(path) > 0 && path[0] == '~' {
		home, _ := os.UserHomeDir()
		if sudoUser := os.Getenv("SUDO_USER"); sudoUser != "" && sudoUser != "root" {
			home = "/home/" + sudoUser
		}
		return home + path[1:]
	}
	return path
}
