package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/sshkey"
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
			fcClient := firecracker.NewClient()
			fcClient.UpdateVMState(existingVM)

			if existingVM.State == vm.StateRunning {
				return fmt.Errorf("VM '%s' is already running", name)
			}

			fmt.Printf("Starting VM '%s'...\n", name)

			// Ensure images are available
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			if err := imgMgr.EnsureDefaultImages(); err != nil {
				return fmt.Errorf("failed to ensure images: %w", err)
			}

			// Create VM-specific rootfs if needed
			vmRootfs, err := imgMgr.CreateVMRootfs(name, paths.VMs, existingVM.DiskSizeMB, existingVM.Image)
			if err != nil {
				return fmt.Errorf("failed to create VM rootfs: %w", err)
			}
			existingVM.RootfsPath = vmRootfs

			// Set kernel path based on custom kernel or default
			existingVM.KernelPath = imgMgr.GetKernelPath(existingVM.Kernel)

			// Inject SSH keys (vmm managed key + user key if configured)
			fmt.Println("Injecting SSH public key...")
			if err := sshkey.EnsureKeyPair(paths.SSH); err != nil {
				return fmt.Errorf("failed to ensure vmm SSH key: %w", err)
			}
			authorizedKeys, err := sshkey.BuildAuthorizedKeys(paths.SSH, existingVM.SSHPublicKey)
			if err != nil {
				return fmt.Errorf("failed to build authorized keys: %w", err)
			}
			if err := image.InjectSSHKey(existingVM.RootfsPath, authorizedKeys); err != nil {
				return fmt.Errorf("failed to inject SSH key: %w", err)
			}

			// Inject DNS configuration
			fmt.Println("Configuring DNS...")
			if err := image.InjectDNSConfig(existingVM.RootfsPath, existingVM.DNSServers); err != nil {
				return fmt.Errorf("failed to inject DNS config: %w", err)
			}

			// Create mount images and configure fstab
			var mountDrives []firecracker.MountDrive
			if len(existingVM.Mounts) > 0 {
				fmt.Println("Creating mount images...")
				mountMgr := mount.NewManager(paths.Mounts)

				// Create mount images and collect drive configs
				var mountEntries []image.MountEntry
				for i := range existingVM.Mounts {
					m := &existingVM.Mounts[i]
					if err := mountMgr.CreateMountImage(m, name); err != nil {
						return fmt.Errorf("failed to create mount image for '%s': %w", m.GuestTag, err)
					}

					// Device names: vdb, vdc, vdd, etc. (vda is rootfs)
					deviceLetter := string(rune('b' + i))
					device := fmt.Sprintf("/dev/vd%s", deviceLetter)
					mountPath := fmt.Sprintf("/mnt/%s", m.GuestTag)

					mountEntries = append(mountEntries, image.MountEntry{
						Device:    device,
						MountPath: mountPath,
						ReadOnly:  m.ReadOnly,
					})

					mountDrives = append(mountDrives, firecracker.MountDrive{
						ImagePath: m.ImagePath,
						Tag:       m.GuestTag,
						ReadOnly:  m.ReadOnly,
					})
				}

				// Inject fstab entries for mounts
				fmt.Println("Configuring mount points in guest...")
				if err := image.InjectMountFstab(existingVM.RootfsPath, mountEntries); err != nil {
					return fmt.Errorf("failed to inject mount fstab: %w", err)
				}

				// Save updated mount image paths
				existingVM.Save(paths.VMs)
			}

			// Setup networking
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)

			// Ensure bridge exists
			if err := netMgr.EnsureBridge(); err != nil {
				return fmt.Errorf("failed to setup bridge: %w", err)
			}

			// Track resources for cleanup on failure
			var cleanupFuncs []func()
			startSuccess := false
			defer func() {
				if !startSuccess {
					for i := len(cleanupFuncs) - 1; i >= 0; i-- {
						cleanupFuncs[i]()
					}
				}
			}()

			// Create TAP device if it doesn't exist
			if !netMgr.TapExists(existingVM.TapDevice) {
				if err := netMgr.CreateTap(existingVM.TapDevice); err != nil {
					return fmt.Errorf("failed to create TAP device: %w", err)
				}
				cleanupFuncs = append(cleanupFuncs, func() {
					if err := netMgr.DeleteTap(existingVM.TapDevice); err != nil {
						fmt.Printf("Warning: failed to clean up TAP device %s: %v\n", existingVM.TapDevice, err)
					}
				})
			}

			// Allocate IP, skipping any already in use
			ip, err := netMgr.AllocateIP(usedVMIPs(paths.VMs))
			if err != nil {
				return fmt.Errorf("failed to allocate IP: %w", err)
			}
			existingVM.IPAddress = ip
			cleanupFuncs = append(cleanupFuncs, func() {
				existingVM.IPAddress = ""
				existingVM.State = vm.StateError
				if err := existingVM.Save(paths.VMs); err != nil {
					fmt.Printf("Warning: failed to save VM state during cleanup: %v\n", err)
				}
			})

			// Update state to starting
			existingVM.State = vm.StateStarting
			existingVM.Save(paths.VMs)

			// Clean up socket file on failure
			cleanupFuncs = append(cleanupFuncs, func() {
				if err := os.Remove(existingVM.SocketPath); err != nil && !os.IsNotExist(err) {
					fmt.Printf("Warning: failed to clean up socket file %s: %v\n", existingVM.SocketPath, err)
				}
			})

			// Start Firecracker
			ctx := context.Background()
			vmCfg := &firecracker.VMConfig{
				SocketPath:  existingVM.SocketPath,
				KernelPath:  existingVM.KernelPath,
				RootfsPath:  existingVM.RootfsPath,
				CPUs:        existingVM.CPUs,
				MemoryMB:    existingVM.MemoryMB,
				TapDevice:   existingVM.TapDevice,
				MacAddress:  existingVM.MacAddress,
				LogPath:     fmt.Sprintf("%s/%s.log", paths.Logs, name),
				IPAddress:   existingVM.IPAddress,
				Gateway:     cfg.Gateway,
				Subnet:      cfg.Subnet,
				MountDrives: mountDrives,
			}

			machine, err := fcClient.StartVM(ctx, vmCfg)
			if err != nil {
				return fmt.Errorf("failed to start VM: %w", err)
			}

			// Mark success to prevent cleanup
			startSuccess = true

			// Update VM state
			existingVM.State = vm.StateRunning
			existingVM.PID = fcClient.GetVMPID(machine)
			existingVM.StartedAt = time.Now()
			existingVM.Save(paths.VMs)

			fmt.Printf("VM '%s' started successfully\n", name)
			fmt.Printf("  IP Address: %s\n", existingVM.IPAddress)
			fmt.Printf("  PID: %d\n", existingVM.PID)
			fmt.Printf("  Socket: %s\n", existingVM.SocketPath)

			return nil
		},
	}
}
