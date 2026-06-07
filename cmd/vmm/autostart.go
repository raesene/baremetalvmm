package main

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/vm"
	"github.com/spf13/cobra"
)

func autostartCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "autostart",
		Short:  "Start all VMs marked for auto-start (used by systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			vms, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			fcClient := firecracker.NewClient()
			imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
			netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)

			// Ensure bridge exists first
			if err := netMgr.EnsureBridge(); err != nil {
				fmt.Printf("Warning: failed to setup bridge: %v\n", err)
			}

			started := 0
			for _, v := range vms {
				// Skip VMs not marked for autostart
				if !v.AutoStart {
					continue
				}

				// Update actual state
				fcClient.UpdateVMState(v)
				if v.State == vm.StateRunning {
					fmt.Printf("VM '%s' is already running\n", v.Name)
					continue
				}

				fmt.Printf("Auto-starting VM '%s'...\n", v.Name)

				// Ensure images
				if err := imgMgr.EnsureDefaultImages(); err != nil {
					fmt.Printf("  Error: failed to ensure images: %v\n", err)
					continue
				}

				// Create rootfs if needed
				vmRootfs, err := imgMgr.CreateVMRootfs(v.Name, paths.VMs, v.DiskSizeMB, v.Image)
				if err != nil {
					fmt.Printf("  Error: failed to create rootfs: %v\n", err)
					continue
				}
				v.RootfsPath = vmRootfs

				// Set kernel path based on custom kernel or default
				v.KernelPath = imgMgr.GetKernelPath(v.Kernel)

				// Inject SSH keys (vmm managed key + user key)
				if err := sshkey.EnsureKeyPair(paths.SSH); err != nil {
					fmt.Printf("  Warning: failed to ensure vmm SSH key: %v\n", err)
				}
				authorizedKeys, err := sshkey.BuildAuthorizedKeys(paths.SSH, v.SSHPublicKey)
				if err != nil {
					fmt.Printf("  Warning: failed to build authorized keys: %v\n", err)
				} else {
					if err := image.InjectSSHKey(v.RootfsPath, authorizedKeys); err != nil {
						fmt.Printf("  Warning: failed to inject SSH key: %v\n", err)
					}
				}

				// Inject DNS configuration
				if err := image.InjectDNSConfig(v.RootfsPath, v.DNSServers); err != nil {
					fmt.Printf("  Warning: failed to inject DNS config: %v\n", err)
				}

				// Create mount images and configure fstab
				var mountDrives []firecracker.MountDrive
				if len(v.Mounts) > 0 {
					mountMgr := mount.NewManager(paths.Mounts)
					var mountEntries []image.MountEntry
					for j := range v.Mounts {
						m := &v.Mounts[j]
						if err := mountMgr.CreateMountImage(m, v.Name); err != nil {
							fmt.Printf("  Warning: failed to create mount image for '%s': %v\n", m.GuestTag, err)
							continue
						}
						deviceLetter := string(rune('b' + j))
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
					if len(mountEntries) > 0 {
						if err := image.InjectMountFstab(v.RootfsPath, mountEntries); err != nil {
							fmt.Printf("  Warning: failed to inject mount fstab: %v\n", err)
						}
					}
					v.Save(paths.VMs)
				}

				// Create TAP if needed
				if !netMgr.TapExists(v.TapDevice) {
					if err := netMgr.CreateTap(v.TapDevice); err != nil {
						fmt.Printf("  Error: failed to create TAP: %v\n", err)
						continue
					}
				}

				// Allocate IP
				ip, _ := netMgr.AllocateIP(usedVMIPs(paths.VMs))
				v.IPAddress = ip

				// Start VM
				ctx := context.Background()
				vmCfg := &firecracker.VMConfig{
					SocketPath:  v.SocketPath,
					KernelPath:  v.KernelPath,
					RootfsPath:  v.RootfsPath,
					CPUs:        v.CPUs,
					MemoryMB:    v.MemoryMB,
					TapDevice:   v.TapDevice,
					MacAddress:  v.MacAddress,
					LogPath:     fmt.Sprintf("%s/%s.log", paths.Logs, v.Name),
					IPAddress:   v.IPAddress,
					Gateway:     cfg.Gateway,
					Subnet:      cfg.Subnet,
					MountDrives: mountDrives,
				}

				machine, err := fcClient.StartVM(ctx, vmCfg)
				if err != nil {
					fmt.Printf("  Error: failed to start: %v\n", err)
					v.State = vm.StateError
					v.Save(paths.VMs)
					continue
				}

				v.State = vm.StateRunning
				v.PID = fcClient.GetVMPID(machine)
				v.StartedAt = time.Now()
				v.Save(paths.VMs)

				fmt.Printf("  Started (IP: %s, PID: %d)\n", v.IPAddress, v.PID)
				started++
			}

			fmt.Printf("Auto-started %d VMs\n", started)
			return nil
		},
	}
}

func autostopCmd() *cobra.Command {
	return &cobra.Command{
		Use:    "autostop",
		Short:  "Stop all running VMs (used by systemd)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			paths := cfg.GetPaths()

			vms, err := vm.List(paths.VMs)
			if err != nil {
				return fmt.Errorf("failed to list VMs: %w", err)
			}

			fcClient := firecracker.NewClient()
			stopped := 0

			for _, v := range vms {
				fcClient.UpdateVMState(v)
				if v.State != vm.StateRunning {
					continue
				}

				fmt.Printf("Stopping VM '%s'...\n", v.Name)

				ctx := context.Background()
				if err := fcClient.StopVM(ctx, v.SocketPath); err != nil {
					// Try SIGKILL as fallback
					if v.PID > 0 && firecracker.IsFirecrackerProcess(v.PID) {
						if proc, err := os.FindProcess(v.PID); err == nil {
							proc.Signal(syscall.SIGKILL)
						}
					}
				}

				v.State = vm.StateStopped
				v.PID = 0
				v.Save(paths.VMs)

				os.Remove(v.SocketPath)
				stopped++
			}

			fmt.Printf("Stopped %d VMs\n", stopped)
			return nil
		},
	}
}
