package image

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const (
	// Default Firecracker-compatible kernel and rootfs URLs
	// These are from the Firecracker quickstart guide
	DefaultKernelURL = "https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/kernels/vmlinux.bin"
	DefaultRootfsURL = "https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/rootfs/bionic.rootfs.ext4"

	DefaultKernelName = "vmlinux.bin"
	DefaultRootfsName = "rootfs.ext4"
)

// Manager handles kernel and rootfs image management
type Manager struct {
	KernelDir string
	RootfsDir string
}

// NewManager creates a new image manager
func NewManager(kernelDir, rootfsDir string) *Manager {
	return &Manager{
		KernelDir: kernelDir,
		RootfsDir: rootfsDir,
	}
}

// EnsureDefaultImages downloads default kernel and rootfs if not present
func (m *Manager) EnsureDefaultImages() error {
	kernelPath := filepath.Join(m.KernelDir, DefaultKernelName)
	rootfsPath := filepath.Join(m.RootfsDir, DefaultRootfsName)

	// Download kernel if not exists
	if _, err := os.Stat(kernelPath); os.IsNotExist(err) {
		fmt.Println("Downloading default kernel...")
		if err := m.downloadFile(DefaultKernelURL, kernelPath); err != nil {
			return fmt.Errorf("failed to download kernel: %w", err)
		}
		fmt.Println("Kernel downloaded successfully")
	}

	// Download rootfs if not exists
	if _, err := os.Stat(rootfsPath); os.IsNotExist(err) {
		fmt.Println("Downloading default rootfs (this may take a while)...")
		if err := m.downloadFile(DefaultRootfsURL, rootfsPath); err != nil {
			return fmt.Errorf("failed to download rootfs: %w", err)
		}
		fmt.Println("Rootfs downloaded successfully")
	}

	return nil
}

// GetDefaultKernelPath returns the path to the default kernel
func (m *Manager) GetDefaultKernelPath() string {
	return filepath.Join(m.KernelDir, DefaultKernelName)
}

// GetDefaultRootfsPath returns the path to the default rootfs
func (m *Manager) GetDefaultRootfsPath() string {
	return filepath.Join(m.RootfsDir, DefaultRootfsName)
}

// CreateVMRootfs creates a copy of the rootfs for a specific VM with the specified size
func (m *Manager) CreateVMRootfs(vmName string, vmDir string, diskSizeMB int) (string, error) {
	srcPath := m.GetDefaultRootfsPath()
	dstPath := filepath.Join(vmDir, vmName+".ext4")

	// Check if VM rootfs already exists
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, nil
	}

	// Check if source exists
	if _, err := os.Stat(srcPath); err != nil {
		return "", fmt.Errorf("default rootfs not found at %s: %w", srcPath, err)
	}

	// Copy the rootfs
	fmt.Printf("Creating rootfs for VM '%s'...\n", vmName)
	if err := copyFile(srcPath, dstPath); err != nil {
		return "", fmt.Errorf("failed to copy rootfs: %w", err)
	}

	// Resize the rootfs if a size was specified
	if diskSizeMB > 0 {
		fmt.Printf("Resizing rootfs to %d MB...\n", diskSizeMB)

		// Expand the file to the desired size
		truncateCmd := exec.Command("truncate", "-s", fmt.Sprintf("%dM", diskSizeMB), dstPath)
		if output, err := truncateCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to expand rootfs file: %w: %s", err, string(output))
		}

		// Check the filesystem before resizing
		e2fsckCmd := exec.Command("e2fsck", "-f", "-y", dstPath)
		e2fsckCmd.Run() // Best effort, ignore errors

		// Resize the ext4 filesystem to fill the file
		resize2fsCmd := exec.Command("resize2fs", dstPath)
		if output, err := resize2fsCmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("failed to resize filesystem: %w: %s", err, string(output))
		}
	}

	return dstPath, nil
}

// DeleteVMRootfs removes a VM's rootfs
func (m *Manager) DeleteVMRootfs(vmName string, vmDir string) error {
	path := filepath.Join(vmDir, vmName+".ext4")
	if _, err := os.Stat(path); err == nil {
		return os.Remove(path)
	}
	return nil
}

// ListKernels returns all available kernels
func (m *Manager) ListKernels() ([]string, error) {
	return listFiles(m.KernelDir)
}

// ListRootfs returns all available rootfs images
func (m *Manager) ListRootfs() ([]string, error) {
	return listFiles(m.RootfsDir)
}

// downloadFile downloads a file from URL to the specified path
func (m *Manager) downloadFile(url, destPath string) error {
	// Ensure directory exists
	dir := filepath.Dir(destPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// Create temp file
	tmpPath := destPath + ".tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	defer out.Close()

	// Download
	resp, err := http.Get(url)
	if err != nil {
		os.Remove(tmpPath)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		os.Remove(tmpPath)
		return fmt.Errorf("bad status: %s", resp.Status)
	}

	// Copy with progress (simple version)
	_, err = io.Copy(out, resp.Body)
	if err != nil {
		os.Remove(tmpPath)
		return err
	}

	// Rename to final path
	return os.Rename(tmpPath, destPath)
}

// copyFile copies a file from src to dst
func copyFile(src, dst string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	dstFile, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer dstFile.Close()

	_, err = io.Copy(dstFile, srcFile)
	return err
}

// listFiles returns all files in a directory
func listFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}

	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	return files, nil
}

// InjectSSHKey injects an SSH public key into a rootfs image
// This mounts the ext4 image and writes the key to /root/.ssh/authorized_keys
func InjectSSHKey(rootfsPath, sshPublicKey string) error {
	if sshPublicKey == "" {
		return nil
	}

	// Ensure the key ends with a newline
	sshPublicKey = strings.TrimSpace(sshPublicKey) + "\n"

	// Create a temporary mount point
	mountPoint, err := os.MkdirTemp("", "vmm-rootfs-*")
	if err != nil {
		return fmt.Errorf("failed to create mount point: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	// Mount the rootfs image
	mountCmd := exec.Command("mount", "-o", "loop", rootfsPath, mountPoint)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		return fmt.Errorf("failed to mount rootfs: %w: %s", err, string(output))
	}

	// Ensure we unmount even if there's an error
	defer func() {
		umountCmd := exec.Command("umount", mountPoint)
		umountCmd.Run() // Best effort unmount
	}()

	// Create /root/.ssh directory if it doesn't exist
	sshDir := filepath.Join(mountPoint, "root", ".ssh")
	if err := os.MkdirAll(sshDir, 0700); err != nil {
		return fmt.Errorf("failed to create .ssh directory: %w", err)
	}

	// Write the authorized_keys file
	authKeysPath := filepath.Join(sshDir, "authorized_keys")
	if err := os.WriteFile(authKeysPath, []byte(sshPublicKey), 0600); err != nil {
		return fmt.Errorf("failed to write authorized_keys: %w", err)
	}

	// Ensure correct ownership (root:root = 0:0)
	if err := os.Chown(sshDir, 0, 0); err != nil {
		return fmt.Errorf("failed to set .ssh ownership: %w", err)
	}
	if err := os.Chown(authKeysPath, 0, 0); err != nil {
		return fmt.Errorf("failed to set authorized_keys ownership: %w", err)
	}

	return nil
}
