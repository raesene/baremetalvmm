package mount

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/raesene/baremetalvmm/internal/vm"
)

// Manager handles mount image creation and management
type Manager struct {
	MountsDir string
}

// tmpfsMagic is TMPFS_MAGIC from linux/magic.h.
const tmpfsMagic = 0x01021994

// OnTmpfs reports whether path is on a tmpfs, such as /tmp on Fedora, whose
// contents are gone after a reboot.
func OnTmpfs(path string) bool {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return false
	}
	return st.Type == tmpfsMagic
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

	// Calculate the image budget for the directory
	budget, err := calculateMountBudget(mount.HostPath)
	if err != nil {
		return fmt.Errorf("failed to calculate directory size: %w", err)
	}

	fmt.Printf("  Creating mount image for '%s' (%d MB)...\n", mount.GuestTag, budget.SizeMB)

	// Create a sparse file
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", budget.SizeMB), imagePath).Run(); err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}

	// Create ext4 filesystem. -N reserves exactly the budgeted inode count:
	// the sizing budget assumes one inode per entry plus headroom, and
	// mkfs's default ratio (16KB/inode for images >= ~1GB) would starve
	// small-file trees, while a fixed low ratio would over-reserve on
	// large-file trees.
	mkfsCmd := exec.Command("mkfs.ext4", "-F", "-N", strconv.FormatInt(budget.Inodes, 10), "-L", mount.GuestTag, imagePath)
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
	budget, err := calculateMountBudget(mount.HostPath)
	if err != nil {
		return fmt.Errorf("failed to calculate directory size: %w", err)
	}

	// If the tree's entry count outgrew the image's inode count (e.g. a few
	// large files were replaced by many small ones), growing the image does
	// not help: resize2fs keeps the original bytes-per-inode ratio and cannot
	// add inodes. The sync below re-copies everything from the host anyway,
	// so recreate the image with the full budget instead.
	if currentInodes := ext4InodeCount(mount.ImagePath); currentInodes > 0 && budget.Inodes > currentInodes {
		if err := os.Remove(mount.ImagePath); err != nil {
			return fmt.Errorf("failed to remove undersized mount image: %w", err)
		}
		return m.CreateMountImage(mount, vmName)
	}

	// Get current image size
	imgInfo, err := os.Stat(mount.ImagePath)
	if err != nil {
		return fmt.Errorf("failed to stat image: %w", err)
	}
	currentSizeMB := int(imgInfo.Size() / (1024 * 1024))

	// Resize if needed (only grow, never shrink). resize2fs keeps the
	// bytes-per-inode ratio of the original mkfs, so the inode count grows
	// proportionally with the size budget.
	if budget.SizeMB > currentSizeMB {
		fmt.Printf("  Resizing mount image to %d MB...\n", budget.SizeMB)
		if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", budget.SizeMB), mount.ImagePath).Run(); err != nil {
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

// mountBudget is the ext4 image sizing needed to hold a directory tree.
type mountBudget struct {
	SizeMB int   // total image size in MB
	Inodes int64 // inode count to pass to mkfs via -N
}

// calculateMountBudget estimates the ext4 image size and inode count needed
// to hold a directory tree. Content is rounded up to whole 4KB blocks per
// file (ext4's minimum allocation) plus one block per directory. A naive
// multiplier on raw bytes breaks in both directions: trees with many small
// files (e.g. .git/objects) need one inode per entry regardless of content
// size, and the filesystem's own metadata (inode tables, journal, block
// group descriptors) scales with image size rather than being a fixed amount.
func calculateMountBudget(path string) (mountBudget, error) {
	var contentBytes, entryCount int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			contentBytes += 4096 // each directory needs at least one block
		} else {
			contentBytes += (info.Size() + 4095) &^ 4095 // ext4 allocates whole 4KB blocks
		}
		entryCount++ // files and directories each need an inode
		return nil
	})
	if err != nil {
		return mountBudget{}, err
	}

	// One inode per entry, plus headroom for entries created inside the VM
	// between syncs: 10% plus a fixed margin. mkfs reserves exactly this
	// count, so there is no per-inode waste on large-file trees.
	inodes := entryCount + entryCount/10 + 1024

	// Inode tables: mkfs reserves 256 bytes per inode up front.
	tableBytes := inodes * 256

	// The journal scales with image size and is capped by mkfs (16MB at 1GB,
	// 64MB from 4GB up to 16GB; larger images use up to 128MB, which the 2%
	// allowance below covers). It depends on the total size, which depends on
	// it, so iterate the estimate to convergence.
	const mb = 1024 * 1024
	totalBytes := contentBytes + tableBytes
	var journalBytes int64
	for range 2 {
		journalBytes = totalBytes / 64
		switch {
		case journalBytes < 16*mb:
			journalBytes = 16 * mb
		case journalBytes > 64*mb:
			journalBytes = 64 * mb
		}
		// Superblock copies, block group descriptors, and other ext4
		// metadata: a couple of percent of the total.
		totalBytes = contentBytes + tableBytes + journalBytes
		totalBytes += totalBytes / 50
	}

	sizeMB := int((totalBytes + mb - 1) / mb)
	if sizeMB < 64 {
		sizeMB = 64
	}
	return mountBudget{SizeMB: sizeMB, Inodes: inodes}, nil
}

// ext4InodeCount returns the inode count of an ext4 image file, or 0 if it
// cannot be determined (dumpe2fs missing or failed).
func ext4InodeCount(imagePath string) int64 {
	out, err := exec.Command("dumpe2fs", "-h", imagePath).Output()
	if err != nil {
		return 0
	}
	return parseInodeCount(string(out))
}

// parseInodeCount extracts the "Inode count" value from dumpe2fs -h output.
func parseInodeCount(dumpe2fsOutput string) int64 {
	for line := range strings.SplitSeq(dumpe2fsOutput, "\n") {
		if after, ok := strings.CutPrefix(line, "Inode count:"); ok {
			n, err := strconv.ParseInt(strings.TrimSpace(after), 10, 64)
			if err != nil {
				return 0
			}
			return n
		}
	}
	return 0
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
