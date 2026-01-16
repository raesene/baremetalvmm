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

// ImportDockerImage imports a Docker image as a VMM rootfs
// It exports the Docker image, creates an ext4 filesystem, and configures it for Firecracker
func (m *Manager) ImportDockerImage(dockerImage, imageName string, sizeMB int) error {
	if sizeMB == 0 {
		sizeMB = 2048 // Default 2GB
	}

	destPath := filepath.Join(m.RootfsDir, imageName+".ext4")

	// Check if image already exists
	if _, err := os.Stat(destPath); err == nil {
		return fmt.Errorf("image '%s' already exists at %s", imageName, destPath)
	}

	fmt.Printf("Importing Docker image '%s' as '%s'...\n", dockerImage, imageName)

	// Create a temporary directory for the export
	tmpDir, err := os.MkdirTemp("", "vmm-import-*")
	if err != nil {
		return fmt.Errorf("failed to create temp directory: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	exportDir := filepath.Join(tmpDir, "rootfs")
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		return fmt.Errorf("failed to create export directory: %w", err)
	}

	// Step 1: Create a container from the image and export it
	fmt.Println("  Exporting Docker image...")
	containerID, err := runCmdOutput("docker", "create", dockerImage)
	if err != nil {
		return fmt.Errorf("failed to create container from image: %w", err)
	}
	containerID = strings.TrimSpace(containerID)
	defer exec.Command("docker", "rm", containerID).Run()

	// Export and extract in one step
	exportCmd := exec.Command("docker", "export", containerID)
	tarCmd := exec.Command("tar", "-xf", "-", "-C", exportDir)
	tarCmd.Stdin, _ = exportCmd.StdoutPipe()
	tarCmd.Stderr = os.Stderr

	if err := tarCmd.Start(); err != nil {
		return fmt.Errorf("failed to start tar: %w", err)
	}
	if err := exportCmd.Run(); err != nil {
		return fmt.Errorf("failed to export container: %w", err)
	}
	if err := tarCmd.Wait(); err != nil {
		return fmt.Errorf("failed to extract export: %w", err)
	}

	// Step 2: Install systemd and SSH if not present
	fmt.Println("  Configuring rootfs for Firecracker...")
	if err := configureRootfsForFirecracker(exportDir); err != nil {
		return fmt.Errorf("failed to configure rootfs: %w", err)
	}

	// Step 3: Create the ext4 image
	fmt.Printf("  Creating %dMB ext4 image...\n", sizeMB)
	if err := createExt4Image(destPath, exportDir, sizeMB); err != nil {
		return fmt.Errorf("failed to create ext4 image: %w", err)
	}

	fmt.Printf("Successfully imported '%s' as '%s'\n", dockerImage, imageName)
	fmt.Printf("  Image path: %s\n", destPath)
	return nil
}

// configureRootfsForFirecracker prepares a rootfs for Firecracker boot
func configureRootfsForFirecracker(rootfsDir string) error {
	// Check if this looks like a Debian/Ubuntu system
	if _, err := os.Stat(filepath.Join(rootfsDir, "etc", "debian_version")); err != nil {
		if _, err := os.Stat(filepath.Join(rootfsDir, "etc", "apt")); err != nil {
			return fmt.Errorf("only Debian/Ubuntu-based images are currently supported")
		}
	}

	// Create necessary directories
	dirs := []string{
		"dev", "proc", "sys", "run", "tmp",
		"var/run", "var/log",
	}
	for _, dir := range dirs {
		os.MkdirAll(filepath.Join(rootfsDir, dir), 0755)
	}

	// Mount required filesystems for chroot
	mounts := []struct {
		source string
		target string
		fstype string
		flags  string
	}{
		{"/dev", filepath.Join(rootfsDir, "dev"), "", "--bind"},
		{"/dev/pts", filepath.Join(rootfsDir, "dev", "pts"), "", "--bind"},
		{"/proc", filepath.Join(rootfsDir, "proc"), "proc", ""},
		{"/sys", filepath.Join(rootfsDir, "sys"), "sysfs", ""},
	}

	// Ensure dev/pts exists
	os.MkdirAll(filepath.Join(rootfsDir, "dev", "pts"), 0755)

	var mountedPaths []string
	cleanup := func() {
		// Unmount in reverse order
		for i := len(mountedPaths) - 1; i >= 0; i-- {
			exec.Command("umount", "-l", mountedPaths[i]).Run()
		}
	}
	defer cleanup()

	for _, m := range mounts {
		var cmd *exec.Cmd
		if m.flags == "--bind" {
			cmd = exec.Command("mount", "--bind", m.source, m.target)
		} else {
			cmd = exec.Command("mount", "-t", m.fstype, m.fstype, m.target)
		}
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("failed to mount %s: %w", m.target, err)
		}
		mountedPaths = append(mountedPaths, m.target)
	}

	// Copy resolv.conf for DNS during package installation
	resolvConf := filepath.Join(rootfsDir, "etc", "resolv.conf")
	os.Remove(resolvConf) // Remove if it's a symlink
	if err := copyFile("/etc/resolv.conf", resolvConf); err != nil {
		// Create a basic one if copy fails
		os.WriteFile(resolvConf, []byte("nameserver 8.8.8.8\n"), 0644)
	}

	// Update package lists and install required packages
	fmt.Println("  Installing systemd and openssh-server (this may take a while)...")

	// Set DEBIAN_FRONTEND to avoid interactive prompts
	env := []string{
		"DEBIAN_FRONTEND=noninteractive",
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin",
	}

	// Run apt-get update
	updateCmd := exec.Command("chroot", rootfsDir, "apt-get", "update", "-qq")
	updateCmd.Env = append(os.Environ(), env...)
	updateCmd.Stdout = os.Stdout
	updateCmd.Stderr = os.Stderr
	if err := updateCmd.Run(); err != nil {
		return fmt.Errorf("apt-get update failed: %w", err)
	}

	// Install systemd, openssh-server, and essential networking tools
	packages := []string{
		"systemd",
		"systemd-sysv",
		"openssh-server",
		"iproute2",
		"iputils-ping",
		"dbus",
	}

	installCmd := exec.Command("chroot", rootfsDir, "apt-get", "install", "-qq", "-y", "--no-install-recommends")
	installCmd.Args = append(installCmd.Args, packages...)
	installCmd.Env = append(os.Environ(), env...)
	installCmd.Stdout = os.Stdout
	installCmd.Stderr = os.Stderr
	if err := installCmd.Run(); err != nil {
		return fmt.Errorf("apt-get install failed: %w", err)
	}

	// Clean up apt cache to reduce image size
	cleanCmd := exec.Command("chroot", rootfsDir, "apt-get", "clean")
	cleanCmd.Env = append(os.Environ(), env...)
	cleanCmd.Run()

	// Remove apt lists to save space
	os.RemoveAll(filepath.Join(rootfsDir, "var", "lib", "apt", "lists"))

	// Configure systemd for Firecracker
	// Enable serial console on ttyS0
	serialConf := `[Unit]
Description=Serial Console on ttyS0
After=systemd-user-sessions.service

[Service]
ExecStart=/sbin/agetty -o '-p -- \\u' --keep-baud 115200,38400,9600 ttyS0 xterm-256color
Type=idle
Restart=always
RestartSec=0
UtmpIdentifier=ttyS0
TTYPath=/dev/ttyS0
TTYReset=yes
TTYVHangup=yes

[Install]
WantedBy=multi-user.target
`
	serialServicePath := filepath.Join(rootfsDir, "etc", "systemd", "system", "serial-getty@ttyS0.service")
	os.MkdirAll(filepath.Dir(serialServicePath), 0755)
	os.WriteFile(serialServicePath, []byte(serialConf), 0644)

	// Enable the serial console service
	wantsDir := filepath.Join(rootfsDir, "etc", "systemd", "system", "multi-user.target.wants")
	os.MkdirAll(wantsDir, 0755)
	os.Symlink("/etc/systemd/system/serial-getty@ttyS0.service",
		filepath.Join(wantsDir, "serial-getty@ttyS0.service"))

	// Enable SSH service
	sshWantsDir := filepath.Join(rootfsDir, "etc", "systemd", "system", "sshd.service.wants")
	os.MkdirAll(sshWantsDir, 0755)
	// SSH might be named ssh or sshd depending on the distro
	sshServiceLink := filepath.Join(wantsDir, "ssh.service")
	if _, err := os.Stat(filepath.Join(rootfsDir, "lib", "systemd", "system", "ssh.service")); err == nil {
		os.Symlink("/lib/systemd/system/ssh.service", sshServiceLink)
	} else {
		os.Symlink("/lib/systemd/system/sshd.service", sshServiceLink)
	}

	// Configure SSH to allow root login with keys
	sshdConfig := filepath.Join(rootfsDir, "etc", "ssh", "sshd_config")
	if data, err := os.ReadFile(sshdConfig); err == nil {
		content := string(data)
		// Ensure PermitRootLogin is set to prohibit-password (key-only)
		if !strings.Contains(content, "PermitRootLogin") {
			content += "\nPermitRootLogin prohibit-password\n"
		} else {
			content = strings.ReplaceAll(content, "PermitRootLogin no", "PermitRootLogin prohibit-password")
			content = strings.ReplaceAll(content, "#PermitRootLogin", "PermitRootLogin")
		}
		os.WriteFile(sshdConfig, []byte(content), 0644)
	}

	// Create /etc/fstab
	fstab := `# /etc/fstab - VMM generated
/dev/vda / ext4 defaults 0 1
`
	os.WriteFile(filepath.Join(rootfsDir, "etc", "fstab"), []byte(fstab), 0644)

	// Set hostname
	os.WriteFile(filepath.Join(rootfsDir, "etc", "hostname"), []byte("vmm-guest\n"), 0644)

	// Configure networking - let kernel ip= parameter handle it, but ensure interface comes up
	// Create a simple network configuration that works with the kernel ip= parameter
	networkConf := `[Match]
Name=eth0

[Network]
DHCP=no
`
	networkDir := filepath.Join(rootfsDir, "etc", "systemd", "network")
	os.MkdirAll(networkDir, 0755)
	os.WriteFile(filepath.Join(networkDir, "10-eth0.network"), []byte(networkConf), 0644)

	// Enable systemd-networkd
	os.Symlink("/lib/systemd/system/systemd-networkd.service",
		filepath.Join(wantsDir, "systemd-networkd.service"))

	// Set root password to empty (will use SSH keys)
	// This is done by setting the password field to empty in /etc/shadow
	shadowPath := filepath.Join(rootfsDir, "etc", "shadow")
	if data, err := os.ReadFile(shadowPath); err == nil {
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			if strings.HasPrefix(line, "root:") {
				parts := strings.SplitN(line, ":", 3)
				if len(parts) >= 3 {
					// Set password to '*' (locked but allows SSH key login)
					lines[i] = "root:*:" + parts[2]
				}
			}
		}
		os.WriteFile(shadowPath, []byte(strings.Join(lines, "\n")), 0640)
	}

	return nil
}

// createExt4Image creates an ext4 image file from a directory
func createExt4Image(imagePath, sourceDir string, sizeMB int) error {
	// Create a sparse file
	if err := exec.Command("truncate", "-s", fmt.Sprintf("%dM", sizeMB), imagePath).Run(); err != nil {
		return fmt.Errorf("failed to create image file: %w", err)
	}

	// Create ext4 filesystem
	mkfsCmd := exec.Command("mkfs.ext4", "-F", "-L", "rootfs", imagePath)
	if output, err := mkfsCmd.CombinedOutput(); err != nil {
		os.Remove(imagePath)
		return fmt.Errorf("failed to create ext4 filesystem: %w: %s", err, string(output))
	}

	// Mount the image
	mountPoint, err := os.MkdirTemp("", "vmm-mount-*")
	if err != nil {
		os.Remove(imagePath)
		return fmt.Errorf("failed to create mount point: %w", err)
	}
	defer os.RemoveAll(mountPoint)

	mountCmd := exec.Command("mount", "-o", "loop", imagePath, mountPoint)
	if output, err := mountCmd.CombinedOutput(); err != nil {
		os.Remove(imagePath)
		return fmt.Errorf("failed to mount image: %w: %s", err, string(output))
	}
	defer exec.Command("umount", mountPoint).Run()

	// Copy files from source directory to mounted image
	// Use tar to preserve permissions and special files
	tarCreate := exec.Command("tar", "-cf", "-", "-C", sourceDir, ".")
	tarExtract := exec.Command("tar", "-xf", "-", "-C", mountPoint)
	tarExtract.Stdin, _ = tarCreate.StdoutPipe()

	if err := tarExtract.Start(); err != nil {
		return fmt.Errorf("failed to start tar extract: %w", err)
	}
	if err := tarCreate.Run(); err != nil {
		return fmt.Errorf("failed to create tar: %w", err)
	}
	if err := tarExtract.Wait(); err != nil {
		return fmt.Errorf("failed to extract tar: %w", err)
	}

	return nil
}

// runCmdOutput runs a command and returns its output
func runCmdOutput(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s: %s", err, string(exitErr.Stderr))
		}
		return "", err
	}
	return string(output), nil
}

// GetImagePath returns the path to a named image
func (m *Manager) GetImagePath(imageName string) string {
	return filepath.Join(m.RootfsDir, imageName+".ext4")
}

// ImageExists checks if a named image exists
func (m *Manager) ImageExists(imageName string) bool {
	_, err := os.Stat(m.GetImagePath(imageName))
	return err == nil
}

// DeleteImage removes a named image
func (m *Manager) DeleteImage(imageName string) error {
	path := m.GetImagePath(imageName)
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return fmt.Errorf("image '%s' not found", imageName)
	}
	return os.Remove(path)
}

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
// If imageName is empty, uses the default rootfs; otherwise uses the named image
func (m *Manager) CreateVMRootfs(vmName string, vmDir string, diskSizeMB int, imageName string) (string, error) {
	var srcPath string
	if imageName != "" {
		srcPath = m.GetImagePath(imageName)
	} else {
		srcPath = m.GetDefaultRootfsPath()
	}
	dstPath := filepath.Join(vmDir, vmName+".ext4")

	// Check if VM rootfs already exists
	if _, err := os.Stat(dstPath); err == nil {
		return dstPath, nil
	}

	// Check if source exists
	if _, err := os.Stat(srcPath); err != nil {
		if imageName != "" {
			return "", fmt.Errorf("image '%s' not found at %s: %w", imageName, srcPath, err)
		}
		return "", fmt.Errorf("default rootfs not found at %s: %w", srcPath, err)
	}

	// Copy the rootfs
	if imageName != "" {
		fmt.Printf("Creating rootfs for VM '%s' from image '%s'...\n", vmName, imageName)
	} else {
		fmt.Printf("Creating rootfs for VM '%s'...\n", vmName)
	}
	if err := copyFile(srcPath, dstPath); err != nil {
		return "", fmt.Errorf("failed to copy rootfs: %w", err)
	}

	// Resize the rootfs if a size was specified
	if diskSizeMB > 0 {
		// Get current file size
		info, _ := os.Stat(dstPath)
		currentSizeMB := int(info.Size() / (1024 * 1024))

		// Only resize if requested size is larger than current
		if diskSizeMB > currentSizeMB {
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

// DefaultDNSServers are used when no custom DNS servers are specified
var DefaultDNSServers = []string{"8.8.8.8", "8.8.4.4", "1.1.1.1"}

// InjectDNSConfig injects DNS configuration into a rootfs image
// This mounts the ext4 image and writes /etc/resolv.conf
// If dnsServers is empty, default public DNS servers are used
func InjectDNSConfig(rootfsPath string, dnsServers []string) error {
	// Use defaults if no custom servers specified
	if len(dnsServers) == 0 {
		dnsServers = DefaultDNSServers
	}

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

	// Build resolv.conf content
	var resolvConf strings.Builder
	resolvConf.WriteString("# Generated by vmm\n")
	for _, server := range dnsServers {
		resolvConf.WriteString(fmt.Sprintf("nameserver %s\n", server))
	}

	resolvPath := filepath.Join(mountPoint, "etc", "resolv.conf")
	if err := os.WriteFile(resolvPath, []byte(resolvConf.String()), 0644); err != nil {
		return fmt.Errorf("failed to write resolv.conf: %w", err)
	}

	return nil
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
