// Package lifecycle starts VMs. The CLI, autostart, cluster provisioning and
// the web UI all go through Start, so a VM boots the same way whichever entry
// point asked for it.
package lifecycle

import (
	"fmt"
	"os"
	"time"

	"github.com/raesene/baremetalvmm/internal/config"
	"github.com/raesene/baremetalvmm/internal/hypervisor"
	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/mount"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/portforward"
	"github.com/raesene/baremetalvmm/internal/sshkey"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// Options control how Start reports progress and how strict it is.
type Options struct {
	// BestEffort carries on when a guest customisation step fails (SSH keys,
	// DNS, a mount image) or the bridge can't be set up, reporting it through
	// Warn instead. Autostart uses it so one bad mount doesn't leave a VM down
	// after a reboot.
	BestEffort bool
	// Progress reports each step as it begins. Nil means quiet.
	Progress func(msg string)
	// Warn reports problems that don't fail the start. Nil discards them.
	Warn func(msg string)
}

func (o Options) progress(msg string) {
	if o.Progress != nil {
		o.Progress(msg)
	}
}

func (o Options) warn(format string, args ...any) {
	if o.Warn != nil {
		o.Warn(fmt.Sprintf(format, args...))
	}
}

// soft returns err, unless the start is best-effort, in which case it is
// reported and swallowed.
func (o Options) soft(err error) error {
	if err == nil || !o.BestEffort {
		return err
	}
	o.warn("%v", err)
	return nil
}

// Start boots a stopped VM: it prepares the rootfs and mounts, sets up the
// TAP device and IP, launches the VM and installs its port forwards. The
// caller must have checked the VM isn't already running.
//
// On failure everything Start set up is torn down again and the VM is saved
// in the error state. An IP the VM already had is kept, so a VM that fails
// to start doesn't lose the address its port forwards, snapshots or cluster
// rely on.
func Start(cfg *config.Config, v *vm.VM, opts Options) error {
	paths := cfg.GetPaths()

	hv := hypervisor.For(v)
	if err := hypervisor.Supported(v); err != nil {
		return err
	}

	imgMgr := image.NewManager(paths.Kernels, paths.Rootfs)
	if err := imgMgr.EnsureDefaultImages(); err != nil {
		return fmt.Errorf("failed to ensure images: %w", err)
	}

	vmRootfs, err := imgMgr.CreateVMRootfs(v.Name, paths.VMs, v.DiskSizeMB, v.Image)
	if err != nil {
		return fmt.Errorf("failed to create VM rootfs: %w", err)
	}
	v.RootfsPath = vmRootfs
	v.KernelPath = imgMgr.GetKernelPath(v.Kernel)

	opts.progress("Injecting SSH public key...")
	if err := opts.soft(injectSSHKeys(paths.SSH, v)); err != nil {
		return err
	}

	var extraKernelArgs string
	info, err := image.PrepareRootfs(v.RootfsPath)
	if err := opts.soft(err); err != nil {
		return err
	}
	if !info.AppArmor {
		// Images without AppArmor userspace would otherwise fail to run
		// containers under an AppArmor kernel (the k8s profile).
		extraKernelArgs = "apparmor=0"
	}

	opts.progress("Configuring DNS...")
	if err := image.InjectDNSConfig(v.RootfsPath, v.DNSServers); err != nil {
		if err := opts.soft(fmt.Errorf("failed to inject DNS config: %w", err)); err != nil {
			return err
		}
	}

	if hypervisor.Name(v) == hypervisor.CloudHypervisor {
		// Cloud Hypervisor asks the guest to stop with the ACPI power button.
		if err := image.InjectPowerButtonService(v.RootfsPath); err != nil {
			if err := opts.soft(fmt.Errorf("failed to install power button service: %w", err)); err != nil {
				return err
			}
		}
	}

	var skip func(*vm.Mount, error)
	if opts.BestEffort {
		skip = func(m *vm.Mount, err error) {
			opts.warn("failed to create mount image for '%s': %v", m.GuestTag, err)
		}
	}
	if len(v.Mounts) > 0 {
		opts.progress("Creating mount images...")
	}
	mountDrives, err := mount.NewManager(paths.Mounts).PrepareVM(v, skip)
	if err := opts.soft(err); err != nil {
		return err
	}
	if len(v.Mounts) > 0 {
		// Keep the mount image paths even if the start fails later.
		if err := v.Save(paths.VMs); err != nil {
			opts.warn("failed to save mount image paths: %v", err)
		}
	}

	netMgr := network.NewManager(cfg.BridgeName, cfg.Subnet, cfg.Gateway, cfg.HostInterface)
	if err := netMgr.EnsureBridge(); err != nil {
		if err := opts.soft(fmt.Errorf("failed to setup bridge: %w", err)); err != nil {
			return err
		}
	}

	// Undo whatever was set up if the start fails, in reverse order.
	var cleanupFuncs []func()
	startSuccess := false
	defer func() {
		if !startSuccess {
			for i := len(cleanupFuncs) - 1; i >= 0; i-- {
				cleanupFuncs[i]()
			}
		}
	}()

	if !netMgr.TapExists(v.TapDevice) {
		if err := netMgr.CreateTap(v.TapDevice); err != nil {
			return fmt.Errorf("failed to create TAP device: %w", err)
		}
		cleanupFuncs = append(cleanupFuncs, func() {
			if err := netMgr.DeleteTap(v.TapDevice); err != nil {
				opts.warn("failed to clean up TAP device %s: %v", v.TapDevice, err)
			}
		})
	}

	// Reuse the VM's IP if it already has one, otherwise allocate.
	allocatedIP := false
	if v.IPAddress == "" {
		ip, err := netMgr.AllocateIP(usedVMIPs(paths.VMs))
		if err != nil {
			return fmt.Errorf("failed to allocate IP: %w", err)
		}
		v.IPAddress = ip
		allocatedIP = true
	}
	cleanupFuncs = append(cleanupFuncs, func() {
		if allocatedIP {
			v.IPAddress = ""
		}
		v.State = vm.StateError
		if err := v.Save(paths.VMs); err != nil {
			opts.warn("failed to save VM state during cleanup: %v", err)
		}
	})

	// A restricted VM must never boot unrestricted, even best-effort. For
	// any other VM this only clears a stale restriction on its address.
	if err := ApplyRestriction(netMgr, v); err != nil {
		if v.RestrictLocalNetworks {
			return fmt.Errorf("failed to restrict local networks: %w", err)
		}
		opts.warn("failed to clear local network restriction: %v", err)
	}

	v.State = vm.StateStarting
	if err := v.Save(paths.VMs); err != nil {
		opts.warn("failed to save VM state: %v", err)
	}

	cleanupFuncs = append(cleanupFuncs, func() {
		if err := os.Remove(v.SocketPath); err != nil && !os.IsNotExist(err) {
			opts.warn("failed to clean up socket file %s: %v", v.SocketPath, err)
		}
	})

	vmCfg := &vm.LaunchConfig{
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
		Nested:      v.Nested,

		ExtraKernelArgs: extraKernelArgs,
	}

	pid, err := hv.Start(vmCfg)
	if err != nil {
		return fmt.Errorf("failed to start VM: %w", err)
	}

	startSuccess = true

	v.State = vm.StateRunning
	v.PID = pid
	v.StartedAt = time.Now()
	if err := v.Save(paths.VMs); err != nil {
		// The VM is up and UpdateVMState finds it by its socket, so this
		// doesn't strand it.
		opts.warn("failed to save VM state: %v", err)
	}

	// The VM is up, so a broken forward shouldn't fail the start.
	if err := portforward.Apply(netMgr, v); err != nil {
		opts.warn("failed to apply port forwards: %v", err)
	}
	return nil
}

// injectSSHKeys writes the vmm managed key, plus the VM's own key if it has
// one, into the guest's authorized_keys.
func injectSSHKeys(sshDir string, v *vm.VM) error {
	if err := sshkey.EnsureKeyPair(sshDir); err != nil {
		return fmt.Errorf("failed to ensure vmm SSH key: %w", err)
	}
	authorizedKeys, err := sshkey.BuildAuthorizedKeys(sshDir, v.SSHPublicKey)
	if err != nil {
		return fmt.Errorf("failed to build authorized keys: %w", err)
	}
	if err := image.InjectSSHKey(v.RootfsPath, authorizedKeys); err != nil {
		return fmt.Errorf("failed to inject SSH key: %w", err)
	}
	return nil
}

// usedVMIPs returns the IPs held by every VM, running or not.
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
