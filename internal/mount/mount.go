package mount

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/raesene/baremetalvmm/internal/vm"
)

// Manager handles mount image creation and management
type Manager struct {
	MountsDir string
}

// NewManager creates a new mount manager
func NewManager(mountsDir string) *Manager {
	return &Manager{
		MountsDir: mountsDir,
	}
}

// CreateMountImage creates an ext4 image from a host directory
// The image will contain a copy of all files from the host directory
func (m *Manager) CreateMountImage(mount *vm.Mount, vmName string) error {
	// Validate host path exists
	info, err := os.Stat(mount.HostPath)
	if err != nil {
		return fmt.Errorf("host path '%s' does not exist: %w", mount.HostPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("host path '%s' is not a directory", mount.HostPath)
	}

	// Create the image path
	imagePath := m.GetMountImagePath(vmName, mount.GuestTag)
	mount.ImagePath = imagePath

	// Ensure mounts directory exists
	if err := os.MkdirAll(m.MountsDir, 0755); err != nil {
		return fmt.Errorf("failed to create mounts directory: %w", err)
	}

	// Calculate size needed for the directory
	sizeMB, err := calculateDirSize(mount.HostPath)
	if err != nil {
		return fmt.Errorf("failed to calculate directory size: %w", err)
	}

	// calculateDirSize already includes filesystem overhead and minimum.

	fmt.Printf("  Creating mount image for '%s' (%d MB)...\n", mount.GuestTag, sizeMB)

	// Create a sparse file
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), imagePath).Run(); err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}

	// Create ext4 filesystem
	mkfsCmd := exec.Command("mkfs.ext4", "-F", "-L", mount.GuestTag, imagePath)
	if output, err := mkfsCmd.CombinedOutput(); err != nil {
		os.Remove(imagePath)
		return fmt.Errorf("failed to create ext4 filesystem: %w: %s", err, string(output))
	}

	// Copy files from host directory to the image
	if err := m.copyFilesToImage(mount.HostPath, imagePath); err != nil {
		os.Remove(imagePath)
		return fmt.Errorf("failed to copy files to mount image: %w", err)
	}

	return nil
}

// SyncMountImage refreshes a mount image from the host directory
func (m *Manager) SyncMountImage(mount *vm.Mount, vmName string) error {
	if mount.ImagePath == "" {
		mount.ImagePath = m.GetMountImagePath(vmName, mount.GuestTag)
	}

	// Check if image exists
	if _, err := os.Stat(mount.ImagePath); os.IsNotExist(err) {
		// Image doesn't exist, create it
		return m.CreateMountImage(mount, vmName)
	}

	// Validate host path exists
	info, err := os.Stat(mount.HostPath)
	if err != nil {
		return fmt.Errorf("host path '%s' does not exist: %w", mount.HostPath, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("host path '%s' is not a directory", mount.HostPath)
	}

	// Check if we need to resize the image
	sizeMB, err := calculateDirSize(mount.HostPath)
	if err != nil {
		return fmt.Errorf("failed to calculate directory size: %w", err)
	}
	// calculateDirSize already includes filesystem overhead and minimum.

	// Get current image size
	imgInfo, err := os.Stat(mount.ImagePath)
	if err != nil {
		return fmt.Errorf("failed to stat image: %w", err)
	}
	currentSizeMB := int(imgInfo.Size() / (1024 * 1024))

	// Resize if needed (only grow, never shrink)
	if sizeMB > currentSizeMB {
		fmt.Printf("  Resizing mount image to %d MB...\n", sizeMB)
		if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), mount.ImagePath).Run(); err != nil {
			return fmt.Errorf("failed to resize image file: %w", err)
		}
		// Check filesystem
		exec.Command("e2fsck", "-f", "-y", mount.ImagePath).Run()
		// Resize filesystem
		if err := exec.Command("resize2fs", mount.ImagePath).Run(); err != nil {
			return fmt.Errorf("failed to resize filesystem: %w", err)
		}
	}

	fmt.Printf("  Syncing mount image for '%s'...\n", mount.GuestTag)

	// Mount, clear, and copy files
	mountPoint, err := os.MkdirTemp("", "vmm-mount-sync-*")
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	// Mount the image
	mountCmd := exec.Command("mount", "-o", "loop", mount.ImagePath, mountPoint)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to mount image: %w: %s", err, string(output))
	}
	defer exec.Command("umount", mountPoint).Run()

	// Remove all files from the image (except lost+found)
	entries, err := os.ReadDir(mountPoint)
	if err != nil {
		return fmt.Errorf("failed to read mount point: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == "lost+found" {
			continue
		}
		path := filepath.Join(mountPoint, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("failed to remove %s: %w", path, err)
		}
	}

	// Copy files from host to image using tar to preserve permissions
	tarCreate := exec.Command("tar", "-cf", "-", "-C", mount.HostPath, ".")
	tarExtract := exec.Command("tar", "-xf", "-", "-C", mountPoint)
	tarExtract.Stdin, _ = tarCreate.StdoutPipe()
	var extractStderr bytes.Buffer
	tarExtract.Stderr = &extractStderr

	if err := tarExtract.Start(); err != nil {
		return fmt.Errorf("failed to start tar extract: %w", err)
	}
	if err := tarCreate.Run(); err != nil {
		return fmt.Errorf("failed to create tar: %w", err)
	}
	if err := tarExtract.Wait(); err != nil {
		return fmt.Errorf("failed to extract tar: %w: %s", err, extractStderr.String())
	}

	return nil
}

// DeleteMountImage removes a mount image file
func (m *Manager) DeleteMountImage(vmName, guestTag string) error {
	imagePath := m.GetMountImagePath(vmName, guestTag)
	if _, err := os.Stat(imagePath); os.IsNotExist(err) {
		return nil // Already deleted
	}
	return os.Remove(imagePath)
}

// DeleteAllMountImages removes all mount images for a VM
func (m *Manager) DeleteAllMountImages(vmName string, mounts []vm.Mount) error {
	for _, mount := range mounts {
		if err := m.DeleteMountImage(vmName, mount.GuestTag); err != nil {
			return err
		}
	}
	return nil
}

// GetMountImagePath returns the path for a mount image
func (m *Manager) GetMountImagePath(vmName, guestTag string) string {
	return filepath.Join(m.MountsDir, fmt.Sprintf("%s-%s.ext4", vmName, guestTag))
}

// copyFilesToImage mounts an image and copies files into it
func (m *Manager) copyFilesToImage(srcDir, imagePath string) error {
	// Create mount point
	mountPoint, err := os.MkdirTemp("", "vmm-mount-*")
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	// Mount the image
	mountCmd := exec.Command("mount", "-o", "loop", imagePath, mountPoint)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to mount image: %w: %s", err, string(output))
	}
	defer exec.Command("umount", mountPoint).Run()

	// Copy files using tar to preserve permissions and special files
	tarCreate := exec.Command("tar", "-cf", "-", "-C", srcDir, ".")
	tarExtract := exec.Command("tar", "-xf", "-", "-C", mountPoint)
	tarExtract.Stdin, _ = tarCreate.StdoutPipe()
	var extractStderr bytes.Buffer
	tarExtract.Stderr = &extractStderr

	if err := tarExtract.Start(); err != nil {
		return fmt.Errorf("failed to start tar extract: %w", err)
	}
	if err := tarCreate.Run(); err != nil {
		return fmt.Errorf("failed to create tar: %w", err)
	}
	if err := tarExtract.Wait(); err != nil {
		return fmt.Errorf("failed to extract tar: %w: %s", err, extractStderr.String())
	}

	return nil
}

// calculateDirSize returns the estimated ext4 image size needed to hold a
// directory, in MB. It accounts for both file content and filesystem overhead
// (inodes, journal, block group descriptors). A naive 1.2x multiplier on raw
// bytes breaks on trees with many small files (e.g. .git/objects) where inode
// and metadata overhead dominates.
func calculateDirSize(path string) (int, error) {
	var contentBytes int64
	var fileCount int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			contentBytes += info.Size()
		}
		fileCount++
		return nil
	})
	if err != nil {
		return 0, err
	}

	// Each file/dir needs an inode (256 bytes) plus directory entry overhead
	// (~64 bytes average). Round up to 4KB per entry to account for block
	// alignment on the ext4 side.
	inodeOverhead := fileCount * 4096

	// Fixed ext4 overhead: journal (4-32MB), superblock copies, block group
	// descriptors. Use 32MB as a safe baseline.
	const fixedOverheadMB = 32

	totalBytes := contentBytes + inodeOverhead
	sizeMB := int((totalBytes+1024*1024-1)/(1024*1024)) + fixedOverheadMB

	if sizeMB < 64 {
		sizeMB = 64
	}
	return sizeMB, nil
}

// ParseMountSpec parses a mount specification string in format "host_path:tag[:ro|rw]"
func ParseMountSpec(spec string) (*vm.Mount, error) {
	// Split by colon
	parts := splitMountSpec(spec)
	if len(parts) < 2 || len(parts) > 3 {
		return nil, fmt.Errorf("invalid mount spec '%s': expected format 'host_path:tag[:ro|rw]'", spec)
	}

	mount := &vm.Mount{
		HostPath: parts[0],
		GuestTag: parts[1],
		ReadOnly: false, // Default to read-write
	}

	if len(parts) == 3 {
		switch parts[2] {
		case "ro":
			mount.ReadOnly = true
		case "rw":
			mount.ReadOnly = false
		default:
			return nil, fmt.Errorf("invalid mount mode '%s': expected 'ro' or 'rw'", parts[2])
		}
	}

	// Validate host path exists
	if _, err := os.Stat(mount.HostPath); err != nil {
		return nil, fmt.Errorf("host path '%s' does not exist", mount.HostPath)
	}

	// Validate tag (no special characters)
	for _, c := range mount.GuestTag {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return nil, fmt.Errorf("invalid mount tag '%s': only alphanumeric, dash, and underscore allowed", mount.GuestTag)
		}
	}

	return mount, nil
}

// splitMountSpec splits a mount spec, handling paths that may contain colons (like Windows paths or special paths)
// It assumes the format is: path:tag[:mode] where tag and mode are simple identifiers
func splitMountSpec(spec string) []string {
	// Work backwards from the end to find tag and optional mode
	var result []string
	remaining := spec

	// Find the last colon for potential 'ro' or 'rw'
	lastColon := -1
	for i := len(remaining) - 1; i >= 0; i-- {
		if remaining[i] == ':' {
			lastColon = i
			break
		}
	}

	if lastColon == -1 {
		return []string{remaining}
	}

	lastPart := remaining[lastColon+1:]
	remaining = remaining[:lastColon]

	// Check if last part is a mode specifier
	if lastPart == "ro" || lastPart == "rw" {
		// Find the tag (second to last part)
		secondLastColon := -1
		for i := len(remaining) - 1; i >= 0; i-- {
			if remaining[i] == ':' {
				secondLastColon = i
				break
			}
		}
		if secondLastColon == -1 {
			return []string{remaining, lastPart}
		}
		tag := remaining[secondLastColon+1:]
		path := remaining[:secondLastColon]
		result = []string{path, tag, lastPart}
	} else {
		// lastPart is the tag, no mode specified
		result = []string{remaining, lastPart}
	}

	return result
}
