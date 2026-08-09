// Package snapshot implements point-in-time snapshots of running microVMs.
//
// A snapshot captures the full VM state at an instant: the guest memory and
// device/vcpu state (via Firecracker's snapshot API) plus a copy of the VM's
// rootfs and any mount images. Because a Firecracker snapshot does not include
// the block devices, the disks must be copied at the same instant the memory
// snapshot is taken (while the VM is paused) or the restored guest would see an
// inconsistent filesystem.
//
// Restore is in-place: it rolls a VM back to one of its own snapshots, reusing
// the VM's original IP/MAC/TAP identity (which is frozen into the guest memory).
package snapshot

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/vm"
)

const (
	metadataFile = "snapshot.json"
	memFile      = "memory"
	stateFile    = "vmstate"
	rootfsFile   = "rootfs.ext4"
)

// MountSnapshot records a copied mount image within a snapshot.
type MountSnapshot struct {
	GuestTag  string `json:"guest_tag"`
	File      string `json:"file"`       // filename within the snapshot directory
	ImagePath string `json:"image_path"` // original host path to restore the image to
	ReadOnly  bool   `json:"read_only"`
}

// Metadata describes a stored VM snapshot. It records everything needed to
// restore the exact environment the frozen guest expects.
type Metadata struct {
	Name       string          `json:"name"`
	VMName     string          `json:"vm_name"`
	VMID       string          `json:"vm_id"`
	CreatedAt  time.Time       `json:"created_at"`
	FCVersion  string          `json:"fc_version,omitempty"`
	CPUs       int             `json:"cpus"`
	MemoryMB   int             `json:"memory_mb"`
	Kernel     string          `json:"kernel,omitempty"`
	KernelPath string          `json:"kernel_path"`
	IPAddress  string          `json:"ip_address"`
	MacAddress string          `json:"mac_address"`
	TapDevice  string          `json:"tap_device"`
	RootfsPath string          `json:"rootfs_path"` // original host path to restore the rootfs to
	MemFile    string          `json:"mem_file"`
	StateFile  string          `json:"state_file"`
	RootfsFile string          `json:"rootfs_file"`
	Mounts     []MountSnapshot `json:"mounts,omitempty"`
	SizeBytes  int64           `json:"size_bytes"`
}

// Manager handles snapshot storage under a base directory (one subdirectory per
// VM, one subdirectory per snapshot).
type Manager struct {
	baseDir string
}

// NewManager creates a snapshot Manager rooted at snapshotsDir.
func NewManager(snapshotsDir string) *Manager {
	return &Manager{baseDir: snapshotsDir}
}

// VMDir returns the directory holding all snapshots for a VM.
func (m *Manager) VMDir(vmName string) string {
	return filepath.Join(m.baseDir, vmName)
}

// Dir returns the directory for a single snapshot.
func (m *Manager) Dir(vmName, snapName string) string {
	return filepath.Join(m.baseDir, vmName, snapName)
}

// Exists reports whether a snapshot already exists.
func (m *Manager) Exists(vmName, snapName string) bool {
	_, err := os.Stat(filepath.Join(m.Dir(vmName, snapName), metadataFile))
	return err == nil
}

// Create takes a full snapshot of a running VM. The VM is paused, its memory
// and device state are written, its rootfs and mount images are copied, and
// then it is resumed (unless resume is false, in which case it is left paused
// for the caller to stop).
func (m *Manager) Create(ctx context.Context, fc *firecracker.Client, v *vm.VM, snapName string, resume bool) (*Metadata, error) {
	if m.Exists(v.Name, snapName) {
		return nil, fmt.Errorf("snapshot '%s' already exists for VM '%s'", snapName, v.Name)
	}

	dir := m.Dir(v.Name, snapName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("failed to create snapshot directory: %w", err)
	}

	success := false
	defer func() {
		if !success {
			os.RemoveAll(dir)
		}
	}()

	memPath := filepath.Join(dir, memFile)
	statePath := filepath.Join(dir, stateFile)

	// Pause the guest so memory and disk are captured at the same instant.
	if err := fc.PauseVM(ctx, v.SocketPath); err != nil {
		return nil, err
	}
	// Ensure the guest is resumed if anything below fails while it is paused.
	needResume := true
	defer func() {
		if needResume {
			if err := fc.ResumeVM(ctx, v.SocketPath); err != nil {
				fmt.Printf("Warning: failed to resume VM '%s' after snapshot error: %v\n", v.Name, err)
			}
		}
	}()

	if err := fc.CreateSnapshotFiles(ctx, v.SocketPath, memPath, statePath); err != nil {
		return nil, err
	}

	// Copy the rootfs while still paused so it matches the memory snapshot.
	if err := copyFile(v.RootfsPath, filepath.Join(dir, rootfsFile)); err != nil {
		return nil, fmt.Errorf("failed to copy rootfs: %w", err)
	}

	var mounts []MountSnapshot
	for _, mnt := range v.Mounts {
		if mnt.ImagePath == "" {
			continue
		}
		fname := "mount-" + mnt.GuestTag + ".ext4"
		if err := copyFile(mnt.ImagePath, filepath.Join(dir, fname)); err != nil {
			return nil, fmt.Errorf("failed to copy mount image '%s': %w", mnt.GuestTag, err)
		}
		mounts = append(mounts, MountSnapshot{
			GuestTag:  mnt.GuestTag,
			File:      fname,
			ImagePath: mnt.ImagePath,
			ReadOnly:  mnt.ReadOnly,
		})
	}

	// Resume (or intentionally leave paused for a --stop caller).
	if resume {
		if err := fc.ResumeVM(ctx, v.SocketPath); err != nil {
			return nil, err
		}
	}
	needResume = false

	meta := &Metadata{
		Name:       snapName,
		VMName:     v.Name,
		VMID:       v.ID,
		CreatedAt:  time.Now(),
		FCVersion:  fc.Version(),
		CPUs:       v.CPUs,
		MemoryMB:   v.MemoryMB,
		Kernel:     v.Kernel,
		KernelPath: v.KernelPath,
		IPAddress:  v.IPAddress,
		MacAddress: v.MacAddress,
		TapDevice:  v.TapDevice,
		RootfsPath: v.RootfsPath,
		MemFile:    memFile,
		StateFile:  stateFile,
		RootfsFile: rootfsFile,
		Mounts:     mounts,
	}
	meta.SizeBytes = dirSize(dir)

	if err := m.saveMetadata(dir, meta); err != nil {
		return nil, err
	}

	success = true
	return meta, nil
}

// Restore rolls a VM back to a snapshot in place. The VM must already be
// stopped. The snapshot's rootfs and mount images are copied back to their
// original host paths; then, if start is true, the network is re-established
// (same TAP/IP as when the snapshot was taken) and Firecracker is started from
// the snapshot with the guest resumed.
//
// On a successful start the VM's State, PID and StartedAt fields are updated;
// the caller is responsible for persisting the VM.
func (m *Manager) Restore(ctx context.Context, fc *firecracker.Client, netMgr *network.Manager, v *vm.VM, snapName, logPath string, start bool) (*Metadata, error) {
	meta, err := m.Get(v.Name, snapName)
	if err != nil {
		return nil, err
	}

	dir := m.Dir(v.Name, snapName)

	// Restore the rootfs to the exact path recorded in the snapshot state.
	if err := copyFile(filepath.Join(dir, meta.RootfsFile), meta.RootfsPath); err != nil {
		return nil, fmt.Errorf("failed to restore rootfs: %w", err)
	}
	v.RootfsPath = meta.RootfsPath

	for _, mnt := range meta.Mounts {
		if err := copyFile(filepath.Join(dir, mnt.File), mnt.ImagePath); err != nil {
			return nil, fmt.Errorf("failed to restore mount image '%s': %w", mnt.GuestTag, err)
		}
	}

	if !start {
		return meta, nil
	}

	// Re-establish networking with the same identity frozen into guest memory.
	if err := netMgr.EnsureBridge(); err != nil {
		return nil, fmt.Errorf("failed to setup bridge: %w", err)
	}
	if v.TapDevice != "" && !netMgr.TapExists(v.TapDevice) {
		if err := netMgr.CreateTap(v.TapDevice); err != nil {
			return nil, fmt.Errorf("failed to create TAP device: %w", err)
		}
	}

	memPath := filepath.Join(dir, meta.MemFile)
	statePath := filepath.Join(dir, meta.StateFile)

	machine, err := fc.RestoreVM(ctx, v.SocketPath, logPath, memPath, statePath)
	if err != nil {
		if v.TapDevice != "" && netMgr.TapExists(v.TapDevice) {
			if derr := netMgr.DeleteTap(v.TapDevice); derr != nil {
				fmt.Printf("Warning: failed to clean up TAP device %s: %v\n", v.TapDevice, derr)
			}
		}
		return nil, err
	}

	v.State = vm.StateRunning
	v.PID = fc.GetVMPID(machine)
	v.StartedAt = time.Now()

	return meta, nil
}

// Get loads the metadata for a single snapshot.
func (m *Manager) Get(vmName, snapName string) (*Metadata, error) {
	path := filepath.Join(m.Dir(vmName, snapName), metadataFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("snapshot '%s' not found for VM '%s'", snapName, vmName)
		}
		return nil, fmt.Errorf("failed to read snapshot metadata: %w", err)
	}
	var meta Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, fmt.Errorf("failed to parse snapshot metadata: %w", err)
	}
	return &meta, nil
}

// List returns snapshots for a single VM, or for all VMs when vmName is empty.
// Results are sorted newest first.
func (m *Manager) List(vmName string) ([]*Metadata, error) {
	var vmNames []string
	if vmName != "" {
		vmNames = []string{vmName}
	} else {
		entries, err := os.ReadDir(m.baseDir)
		if err != nil {
			if os.IsNotExist(err) {
				return []*Metadata{}, nil
			}
			return nil, err
		}
		for _, e := range entries {
			if e.IsDir() {
				vmNames = append(vmNames, e.Name())
			}
		}
	}

	var snapshots []*Metadata
	for _, name := range vmNames {
		entries, err := os.ReadDir(m.VMDir(name))
		if err != nil {
			continue // no snapshots for this VM
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			meta, err := m.Get(name, e.Name())
			if err != nil {
				continue // skip invalid snapshots
			}
			snapshots = append(snapshots, meta)
		}
	}

	sort.Slice(snapshots, func(i, j int) bool {
		return snapshots[i].CreatedAt.After(snapshots[j].CreatedAt)
	})
	return snapshots, nil
}

// Delete removes a single snapshot. The per-VM directory is removed too if it
// becomes empty.
func (m *Manager) Delete(vmName, snapName string) error {
	if !m.Exists(vmName, snapName) {
		return fmt.Errorf("snapshot '%s' not found for VM '%s'", snapName, vmName)
	}
	if err := os.RemoveAll(m.Dir(vmName, snapName)); err != nil {
		return fmt.Errorf("failed to delete snapshot: %w", err)
	}
	// Remove the VM directory if no snapshots remain (ignore if not empty).
	os.Remove(m.VMDir(vmName))
	return nil
}

// DeleteAllForVM removes every snapshot belonging to a VM. It is a no-op if the
// VM has no snapshots.
func (m *Manager) DeleteAllForVM(vmName string) error {
	err := os.RemoveAll(m.VMDir(vmName))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// saveMetadata writes snapshot metadata atomically (temp file + rename).
func (m *Manager) saveMetadata(dir string, meta *Metadata) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal snapshot metadata: %w", err)
	}

	tmp, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		tmp.Close()
		os.Remove(tmpPath)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return fmt.Errorf("failed to write snapshot metadata: %w", err)
	}
	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return fmt.Errorf("failed to set metadata permissions: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return fmt.Errorf("failed to sync snapshot metadata: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to close metadata temp file: %w", err)
	}
	if err := os.Rename(tmpPath, filepath.Join(dir, metadataFile)); err != nil {
		os.Remove(tmpPath)
		return fmt.Errorf("failed to save snapshot metadata: %w", err)
	}
	return nil
}

// copyFile copies src to dst, creating dst with 0600 permissions and syncing it
// to disk. Any existing dst is overwritten.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// dirSize returns the total size in bytes of all regular files in dir.
func dirSize(dir string) int64 {
	var total int64
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || e.IsDir() {
			continue
		}
		total += info.Size()
	}
	return total
}
