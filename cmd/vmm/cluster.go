package main

import (
	"context"
	"fmt"
	"os"
	"text/tabwriter"
	"strings"
	"time"

	"github.com/raesene/baremetalvmm/internal/cluster"
	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/validate"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func clusterCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cluster",
		Short: "Manage Kubernetes clusters",
	}
	cmd.AddCommand(
		clusterCreateCmd(),
		clusterDeleteCmd(),
		clusterListCmd(),
		clusterKubeconfigCmd(),
	)
	return cmd
}

func clusterCreateCmd() *cobra.Command {
	var workers int
	var cpus int
	var memory int
	var disk int
	var k8sVersion string
	var sshKeyPath string
	var imageName string
	var kernelName string
	var adminWorkstation bool
	var distro string
	var openshiftVersion string

	cmd := &cobra.Command{
		Use:   "create <name>",
		Short: "Create a Kubernetes or OpenShift cluster from microVMs",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := validate.ClusterName(name); err != nil {
				return err
			}

			// Normalize distro selection (accept friendly aliases).
			switch strings.ToLower(distro) {
			case "", "kubeadm", "kubernetes", "k8s":
				distro = cluster.DistroKubeadm
			case "openshift", "microshift", "ocp", "okd":
				distro = cluster.DistroOpenShift
			default:
				return fmt.Errorf("invalid --type %q: must be 'kubeadm' or 'openshift'", distro)
			}
			isOpenShift := distro == cluster.DistroOpenShift

			if err := cfg.EnsureDirectories(); err != nil {
				return fmt.Errorf("failed to create directories: %w", err)
			}

			paths := cfg.GetPaths()

			if cluster.Exists(paths.Clusters, name) {
				return fmt.Errorf("cluster '%s' already exists", name)
			}

			// Resolve SSH key path
			defaults := cfg.GetVMDefaults()
			if !cmd.Flags().Changed("ssh-key") && defaults.SSHKeyPath != "" {
				sshKeyPath = defaults.SSHKeyPath
			}

			var sshPrivateKeyPath string
			var useVMMKey bool
			if sshKeyPath == "" {
				if err := sshkey.EnsureKeyPair(paths.SSH); err != nil {
					return fmt.Errorf("failed to ensure vmm SSH key: %w", err)
				}
				sshPrivateKeyPath = sshkey.PrivateKeyPath(paths.SSH)
				useVMMKey = true
				fmt.Println("Using vmm-managed SSH key for cluster provisioning")
			} else {
				sshKeyPath = expandHomePath(sshKeyPath)
				sshPrivateKeyPath = sshKeyPath
				if len(sshKeyPath) > 4 && sshKeyPath[len(sshKeyPath)-4:] == ".pub" {
					sshPrivateKeyPath = sshKeyPath[:len(sshKeyPath)-4]
				}
				if _, err := os.Stat(sshPrivateKeyPath); err != nil {
					return fmt.Errorf("SSH private key not found at %s: %w", sshPrivateKeyPath, err)
				}
			}

			// OpenShift (MicroShift) is single-node and needs a heavier control plane.
			if isOpenShift {
				if workers > 0 {
					fmt.Println("Note: OpenShift (MicroShift) is single-node; ignoring --workers")
					workers = 0
				}
				if !cmd.Flags().Changed("cpus") && cpus < 4 {
					cpus = 4
				}
				if !cmd.Flags().Changed("memory") && memory < 8192 {
					memory = 8192
				}
				if !cmd.Flags().Changed("disk") && disk < 20480 {
					disk = 20480
				}
			}

			// Validate resource bounds
			if err := validate.CPUs(cpus); err != nil {
				return err
			}
			if err := validate.MemoryMB(memory); err != nil {
				return err
			}
			if err := validate.DiskSizeMB(disk); err != nil {
				return err
			}
			if isOpenShift {
				if err := validate.OpenShiftVersion(openshiftVersion); err != nil {
					return err
				}
				if cpus < 2 {
					return fmt.Errorf("OpenShift requires at least 2 CPUs (got %d)", cpus)
				}
				if memory < 4096 {
					return fmt.Errorf("OpenShift requires at least 4096 MB memory (got %d)", memory)
				}
				if disk < 10240 {
					return fmt.Errorf("OpenShift requires at least 10240 MB disk (got %d)", disk)
				}
			} else {
				if err := validate.K8sVersion(k8sVersion); err != nil {
					return err
				}
				if cpus < 2 {
					return fmt.Errorf("Kubernetes requires at least 2 CPUs (got %d)", cpus)
				}
				if memory < 2048 {
					return fmt.Errorf("Kubernetes requires at least 2048 MB memory (got %d)", memory)
				}
			}

			// Create cluster config
			cl := cluster.NewCluster(name, workers, k8sVersion, distro)
			cl.CPUs = cpus
			cl.MemoryMB = memory
			cl.DiskSizeMB = disk
			cl.SSHKeyPath = sshPrivateKeyPath
			cl.Image = imageName
			cl.Kernel = kernelName
			if isOpenShift {
				cl.OpenShiftVer = openshiftVersion
				cl.K8sVersion = ""
			}

			// Read SSH public key (if user provided one)
			var sshPubKey string
			if !useVMMKey {
				keyData, err := os.ReadFile(sshKeyPath)
				if err != nil {
					return fmt.Errorf("failed to read SSH public key from %s: %w", sshKeyPath, err)
				}
				sshPubKey = string(keyData)
			}

			// Validate image/kernel exist if specified
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			if imageName != "" && !imgMgr.ImageExists(imageName) {
				return fmt.Errorf("image '%s' not found", imageName)
			}
			if kernelName != "" && !imgMgr.KernelExists(kernelName) {
				return fmt.Errorf("kernel '%s' not found", kernelName)
			}

			if isOpenShift {
				// MicroShift needs broad kernel module coverage for CRI-O + kindnet;
				// prefer security-kernel (6.12 LTS), falling back to k8s-kernel.
				if !cmd.Flags().Changed("kernel") {
					if imgMgr.KernelExists("security-kernel") {
						kernelName = "security-kernel"
						cl.Kernel = kernelName
						fmt.Println("Using security-kernel (default for OpenShift clusters)")
					} else if imgMgr.KernelExists("k8s-kernel") {
						kernelName = "k8s-kernel"
						cl.Kernel = kernelName
						fmt.Println("Using k8s-kernel (default for OpenShift clusters)")
					}
				}
				// MicroShift is installed on provision onto the base Ubuntu rootfs;
				// leaving the image empty uses the default rootfs.
			} else {
				// Default to k8s-kernel for clusters (requires 6.6+ for Cilium)
				if !cmd.Flags().Changed("kernel") && imgMgr.KernelExists("k8s-kernel") {
					kernelName = "k8s-kernel"
					cl.Kernel = kernelName
					fmt.Println("Using k8s-kernel (default for clusters)")
				}

				// Auto-detect k8s rootfs if no image specified
				if imageName == "" {
					if found := imgMgr.FindK8sRootfs(k8sVersion); found != "" {
						fmt.Printf("Using pre-built Kubernetes rootfs: %s\n", found)
						imageName = found
						cl.Image = imageName
					} else {
						downloaded, err := imgMgr.DownloadK8sRootfs(k8sVersion)
						if err == nil && downloaded != "" {
							fmt.Printf("Using downloaded Kubernetes rootfs: %s\n", downloaded)
							imageName = downloaded
							cl.Image = imageName
						}
					}
				}
			}

			// Set up admin workstation if requested
			if adminWorkstation {
				secImage := imgMgr.FindSecurityRootfs()
				if secImage == "" {
					return fmt.Errorf("--admin-workstation requires a security-* rootfs image (none found, run 'vmm image pull' first)")
				}
				cl.AdminVM = fmt.Sprintf("%s-admin", name)
				fmt.Printf("Admin workstation enabled: %s (image: %s)\n", cl.AdminVM, secImage)
			}

			// Save cluster config
			if err := cl.Save(paths.Clusters); err != nil {
				return fmt.Errorf("failed to save cluster config: %w", err)
			}

			if isOpenShift {
				fmt.Printf("Creating OpenShift cluster '%s' (MicroShift %s, single-node)\n", name, openshiftVersion)
			} else {
				fmt.Printf("Creating cluster '%s' with Kubernetes %s (%d control-plane + %d workers)\n",
					name, k8sVersion, 1, workers)
			}

			// Create all VMs (cluster nodes + admin if enabled)
			allVMs := cl.AllVMs()
			for _, vmName := range allVMs {
				if vm.Exists(paths.VMs, vmName) {
					return fmt.Errorf("VM '%s' already exists", vmName)
				}
				newVM := vm.NewVM(vmName)
				if vmName == cl.AdminVM {
					secImage := imgMgr.FindSecurityRootfs()
					newVM.CPUs = 2
					newVM.MemoryMB = 4096
					newVM.DiskSizeMB = 20480
					newVM.Image = secImage
					newVM.Kernel = ""
				} else {
					newVM.CPUs = cl.CPUs
					newVM.MemoryMB = cl.MemoryMB
					newVM.DiskSizeMB = cl.DiskSizeMB
					newVM.Image = cl.Image
					newVM.Kernel = cl.Kernel
				}
				newVM.MacAddress = newVM.GenerateMacAddress()
				newVM.TapDevice = network.GenerateTapName(newVM.ID)
				newVM.SSHPublicKey = sshPubKey
				newVM.SocketPath = fmt.Sprintf("%s/%s.sock", paths.Sockets, vmName)

				if err := newVM.Save(paths.VMs); err != nil {
					return fmt.Errorf("failed to save VM '%s': %w", vmName, err)
				}
				fmt.Printf("  Created VM '%s'\n", vmName)
			}

			// Start all VMs
			fmt.Println("Starting all VMs...")
			var nodeInfos []cluster.NodeInfo
			var adminIP string
			for _, vmName := range allVMs {
				ip, err := startClusterVM(vmName)
				if err != nil {
					cl.SetError(fmt.Sprintf("failed to start VM %s: %v", vmName, err))
					cl.Save(paths.Clusters)
					return fmt.Errorf("failed to start VM '%s': %w", vmName, err)
				}
				if vmName == cl.AdminVM {
					adminIP = ip
				} else {
					nodeInfos = append(nodeInfos, cluster.NodeInfo{Name: vmName, IP: ip})
				}
				fmt.Printf("  Started VM '%s' (%s)\n", vmName, ip)
			}

			// Provision the cluster (admin VM is excluded from provisioning)
			if isOpenShift {
				fmt.Println("\nProvisioning OpenShift cluster...")
			} else {
				fmt.Println("\nProvisioning Kubernetes cluster...")
			}
			if err := cluster.ProvisionCluster(cl, sshPrivateKeyPath, nodeInfos); err != nil {
				cl.SetError(fmt.Sprintf("provisioning failed: %v", err))
				cl.Save(paths.Clusters)
				return fmt.Errorf("cluster provisioning failed: %w\nVMs are left running for debugging. Use 'vmm cluster delete %s -f' to clean up", err, name)
			}

			// Extract and merge kubeconfig
			fmt.Println("Configuring kubeconfig...")
			cpClient, err := cluster.WaitForSSH(cl.ControlPlaneIP, sshPrivateKeyPath, 30*time.Second)
			if err != nil {
				cl.SetError(fmt.Sprintf("failed to connect for kubeconfig: %v", err))
				cl.Save(paths.Clusters)
				return fmt.Errorf("failed to connect to control plane for kubeconfig: %w", err)
			}
			defer cpClient.Close()

			var kubeconfigYAML string
			if isOpenShift {
				kubeconfigYAML, err = cluster.ExtractMicroShiftKubeconfig(cpClient, cl.ControlPlaneIP)
			} else {
				kubeconfigYAML, err = cluster.ExtractKubeconfig(cpClient)
			}
			if err != nil {
				cl.SetError(fmt.Sprintf("failed to extract kubeconfig: %v", err))
				cl.Save(paths.Clusters)
				return fmt.Errorf("failed to extract kubeconfig: %w", err)
			}

			if err := cluster.MergeKubeconfig(name, kubeconfigYAML); err != nil {
				cl.SetError(fmt.Sprintf("failed to merge kubeconfig: %v", err))
				cl.Save(paths.Clusters)
				return fmt.Errorf("failed to merge kubeconfig: %w", err)
			}

			// Copy kubeconfig to admin workstation
			if cl.AdminVM != "" && adminIP != "" {
				fmt.Printf("Copying kubeconfig to admin workstation %s...\n", cl.AdminVM)
				if err := cluster.CopyKubeconfigToVM(adminIP, sshPrivateKeyPath, kubeconfigYAML, cl.ControlPlaneIP); err != nil {
					fmt.Printf("Warning: failed to copy kubeconfig to admin workstation: %v\n", err)
				} else {
					fmt.Println("Kubeconfig copied to admin workstation at /root/.kube/config")
				}
			}

			cl.State = cluster.StateRunning
			cl.Save(paths.Clusters)

			fmt.Printf("\nCluster '%s' is ready!\n", name)
			if isOpenShift {
				fmt.Printf("  OpenShift (MicroShift): %s\n", cl.OpenShiftVer)
			} else {
				fmt.Printf("  Kubernetes: %s\n", cl.K8sVersion)
			}
			fmt.Printf("  Control plane: %s\n", cl.ControlPlaneIP)
			fmt.Printf("  Nodes: %d\n", len(cl.ClusterVMs()))
			fmt.Printf("  Context: vmm-%s\n", name)
			if cl.AdminVM != "" {
				fmt.Printf("  Admin workstation: %s (%s)\n", cl.AdminVM, adminIP)
			}
			fmt.Printf("\nUse: kubectl --context vmm-%s get nodes\n", name)

			return nil
		},
	}

	cmd.Flags().StringVar(&distro, "type", "kubeadm", "Cluster type: 'kubeadm' (Kubernetes) or 'openshift' (MicroShift)")
	cmd.Flags().StringVar(&distro, "distro", "kubeadm", "Alias for --type")
	cmd.Flags().MarkHidden("distro")
	cmd.Flags().IntVar(&workers, "workers", 0, "Number of worker nodes (kubeadm only)")
	cmd.Flags().IntVar(&cpus, "cpus", 2, "CPUs per node")
	cmd.Flags().IntVar(&memory, "memory", 4096, "Memory per node in MB")
	cmd.Flags().IntVar(&disk, "disk", 10240, "Disk per node in MB")
	cmd.Flags().StringVar(&k8sVersion, "k8s-version", "1.36.0", "Kubernetes version (kubeadm only)")
	cmd.Flags().StringVar(&openshiftVersion, "openshift-version", "4.20", "OpenShift/MicroShift major.minor version (openshift only)")
	cmd.Flags().StringVar(&sshKeyPath, "ssh-key", "", "Path to SSH public key file")
	cmd.Flags().StringVar(&imageName, "image", "", "Name of rootfs image to use")
	cmd.Flags().StringVar(&kernelName, "kernel", "", "Name of kernel to use")
	cmd.Flags().BoolVar(&adminWorkstation, "admin-workstation", false, "Create an admin workstation VM with security tools and cluster kubeconfig")
	cmd.RegisterFlagCompletionFunc("kernel", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeKernelNames(cmd, nil, toComplete)
	})
	cmd.RegisterFlagCompletionFunc("image", func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		return completeImageNames(cmd, nil, toComplete)
	})

	return cmd
}

func startClusterVM(vmName string) (string, error) {
	paths := cfg.GetPaths()

	existingVM, err := vm.Load(paths.VMs, vmName)
	if err != nil {
		return "", fmt.Errorf("VM '%s' not found", vmName)
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	if err := imgMgr.EnsureDefaultImages(); err != nil {
		return "", fmt.Errorf("failed to ensure images: %w", err)
	}

	vmRootfs, err := imgMgr.CreateVMRootfs(vmName, paths.VMs, existingVM.DiskSizeMB, existingVM.Image)
	if err != nil {
		return "", fmt.Errorf("failed to create VM rootfs: %w", err)
	}
	existingVM.RootfsPath = vmRootfs
	existingVM.KernelPath = imgMgr.GetKernelPath(existingVM.Kernel)

	if err := sshkey.EnsureKeyPair(paths.SSH); err != nil {
		return "", fmt.Errorf("failed to ensure vmm SSH key: %w", err)
	}
	authorizedKeys, err := sshkey.BuildAuthorizedKeys(paths.SSH, existingVM.SSHPublicKey)
	if err != nil {
		return "", fmt.Errorf("failed to build authorized keys: %w", err)
	}
	if err := image.InjectSSHKey(existingVM.RootfsPath, authorizedKeys); err != nil {
		return "", fmt.Errorf("failed to inject SSH key: %w", err)
	}

	if err := image.InjectDNSConfig(existingVM.RootfsPath, existingVM.DNSServers); err != nil {
		return "", fmt.Errorf("failed to inject DNS config: %w", err)
	}

	netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
	if err := netMgr.EnsureBridge(); err != nil {
		return "", fmt.Errorf("failed to setup bridge: %w", err)
	}

	if !netMgr.TapExists(existingVM.TapDevice) {
		if err := netMgr.CreateTap(existingVM.TapDevice); err != nil {
			return "", fmt.Errorf("failed to create TAP device: %w", err)
		}
	}

	ip, err := netMgr.AllocateIP(usedVMIPs(paths.VMs))
	if err != nil {
		return "", fmt.Errorf("failed to allocate IP: %w", err)
	}
	existingVM.IPAddress = ip

	existingVM.State = vm.StateStarting
	existingVM.Save(paths.VMs)

	ctx := context.Background()
	fcClient := firecracker.NewClient()
	vmCfg := &firecracker.VMConfig{
		SocketPath: existingVM.SocketPath,
		KernelPath: existingVM.KernelPath,
		RootfsPath: existingVM.RootfsPath,
		CPUs:       existingVM.CPUs,
		MemoryMB:   existingVM.MemoryMB,
		TapDevice:  existingVM.TapDevice,
		MacAddress: existingVM.MacAddress,
		LogPath:    fmt.Sprintf("%s/%s.log", paths.Logs, vmName),
		IPAddress:  existingVM.IPAddress,
		Gateway:    cfg.Gateway,
		Subnet:     cfg.Subnet,
	}

	machine, err := fcClient.StartVM(ctx, vmCfg)
	if err != nil {
		existingVM.State = vm.StateError
		existingVM.Save(paths.VMs)
		return "", fmt.Errorf("failed to start VM: %w", err)
	}

	existingVM.State = vm.StateRunning
	existingVM.PID = fcClient.GetVMPID(machine)
	existingVM.StartedAt = time.Now()
	existingVM.Save(paths.VMs)

	return ip, nil
}

func clusterDeleteCmd() *cobra.Command {
	var force bool

	cmd := &cobra.Command{
		Use:               "delete <name>",
		Short:             "Delete a Kubernetes cluster and all its VMs",
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

			if cl.State == cluster.StateRunning && !force {
				return fmt.Errorf("cluster '%s' is running. Use --force to delete", name)
			}

			fmt.Printf("Deleting cluster '%s'...\n", name)

			// Delete all VMs
			fcClient := firecracker.NewClient()
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)

			for _, vmName := range cl.AllVMs() {
				existingVM, err := vm.Load(paths.VMs, vmName)
				if err != nil {
					fmt.Printf("  Warning: VM '%s' not found, skipping\n", vmName)
					continue
				}

				fcClient.UpdateVMState(existingVM)
				if existingVM.State == vm.StateRunning {
					fmt.Printf("  Stopping VM '%s'...\n", vmName)
					ctx := context.Background()
					if err := fcClient.StopVM(ctx, existingVM.SocketPath); err != nil {
						fmt.Printf("  Warning: failed to stop VM '%s': %v\n", vmName, err)
					}
				}

				if existingVM.TapDevice != "" && netMgr.TapExists(existingVM.TapDevice) {
					netMgr.DeleteTap(existingVM.TapDevice)
				}
				imgMgr.DeleteVMRootfs(vmName, paths.VMs)

				if len(existingVM.Mounts) > 0 {
					mountMgr := mount.NewManager(paths.Mounts)
					mountMgr.DeleteAllMountImages(vmName, existingVM.Mounts)
				}

				os.Remove(existingVM.SocketPath)
				vm.Delete(paths.VMs, vmName)
				fmt.Printf("  Deleted VM '%s'\n", vmName)
			}

			// Remove kubeconfig context
			if err := cluster.RemoveKubeconfigContext(name); err != nil {
				fmt.Printf("Warning: failed to remove kubeconfig context: %v\n", err)
			}

			// Delete cluster config
			if err := cluster.Delete(paths.Clusters, name); err != nil {
				return fmt.Errorf("failed to delete cluster config: %w", err)
			}

			fmt.Printf("Cluster '%s' deleted\n", name)
			return nil
		},
	}

	cmd.Flags().BoolVarP(&force, "force", "f", false, "Force delete running cluster")

	return cmd
}

func clusterListCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "list",
		Aliases: []string{"ls"},
		Short:   "List Kubernetes clusters",
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			clusters, err := cluster.List(paths.Clusters)
			if err != nil {
				return fmt.Errorf("failed to list clusters: %w", err)
			}

			if len(clusters) == 0 {
				fmt.Println("No clusters found")
				return nil
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tSTATE\tTYPE\tVERSION\tNODES\tCONTROL PLANE IP\tCONTEXT")
			for _, cl := range clusters {
				// Update VM states
				fcClient := firecracker.NewClient()
				allRunning := true
				for _, vmName := range cl.AllVMs() {
					v, err := vm.Load(paths.VMs, vmName)
					if err != nil {
						allRunning = false
						continue
					}
					fcClient.UpdateVMState(v)
					if v.State != vm.StateRunning {
						allRunning = false
					}
				}
				state := string(cl.State)
				if cl.State == cluster.StateRunning && !allRunning {
					state = "degraded"
				}

				nodes := 1 + len(cl.WorkerVMs)
				distro := cl.Distro
				if distro == "" {
					distro = cluster.DistroKubeadm
				}
				version := cl.K8sVersion
				if distro == cluster.DistroOpenShift {
					version = cl.OpenShiftVer
				}
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%d\t%s\tvmm-%s\n",
					cl.Name, state, distro, version, nodes, cl.ControlPlaneIP, cl.Name)
			}
			w.Flush()
			return nil
		},
	}
}

func clusterKubeconfigCmd() *cobra.Command {
	return &cobra.Command{
		Use:               "kubeconfig <name>",
		Short:             "Print or re-merge cluster kubeconfig",
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

			if cl.ControlPlaneIP == "" {
				return fmt.Errorf("cluster '%s' has no control plane IP (not yet started?)", name)
			}

			// Resolve SSH private key path
			sshPrivateKeyPath := cl.SSHKeyPath
			if len(sshPrivateKeyPath) > 4 && sshPrivateKeyPath[len(sshPrivateKeyPath)-4:] == ".pub" {
				sshPrivateKeyPath = sshPrivateKeyPath[:len(sshPrivateKeyPath)-4]
			}
			sshPrivateKeyPath = expandHomePath(sshPrivateKeyPath)

			cpClient, err := cluster.WaitForSSH(cl.ControlPlaneIP, sshPrivateKeyPath, 30*time.Second)
			if err != nil {
				return fmt.Errorf("failed to connect to control plane: %w", err)
			}
			defer cpClient.Close()

			var kubeconfigYAML string
			if cl.Distro == cluster.DistroOpenShift {
				kubeconfigYAML, err = cluster.ExtractMicroShiftKubeconfig(cpClient, cl.ControlPlaneIP)
			} else {
				kubeconfigYAML, err = cluster.ExtractKubeconfig(cpClient)
			}
			if err != nil {
				return fmt.Errorf("failed to extract kubeconfig: %w", err)
			}

			if err := cluster.MergeKubeconfig(name, kubeconfigYAML); err != nil {
				return fmt.Errorf("failed to merge kubeconfig: %w", err)
			}

			fmt.Printf("Kubeconfig merged for cluster '%s' (context: vmm-%s)\n", name, name)
			return nil
		},
	}
}
