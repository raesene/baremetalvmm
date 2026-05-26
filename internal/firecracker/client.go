package firecracker

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	sdk "github.com/firecracker-microvm/firecracker-go-sdk"
	"github.com/firecracker-microvm/firecracker-go-sdk/client/models"
	"github.com/sirupsen/logrus"

	"github.com/raesene/baremetalvmm/internal/vm"
)

const (
	DefaultFirecrackerBin = "/usr/local/bin/firecracker"
)

// Client wraps the Firecracker SDK for VM management
type Client struct {
	FirecrackerBin string
	Logger         *logrus.Logger
}

// NewClient creates a new Firecracker client
func NewClient() *Client {
	logger := logrus.New()
	logger.SetLevel(logrus.InfoLevel)

	return &Client{
		FirecrackerBin: DefaultFirecrackerBin,
		Logger:         logger,
	}
}

// MountDrive represents an additional block device for host directory mounts
type MountDrive struct {
	ImagePath string
	Tag       string
	ReadOnly  bool
}

// VMConfig holds the configuration needed to start a Firecracker VM
type VMConfig struct {
	SocketPath  string
	KernelPath  string
	RootfsPath  string
	CPUs        int
	MemoryMB    int
	TapDevice   string
	MacAddress  string
	KernelArgs  string
	LogPath     string
	IPAddress   string
	Gateway     string
	Subnet      string
	MountDrives []MountDrive
}

// netmaskFromCIDR derives a dotted-decimal netmask from a CIDR string (e.g. "172.16.0.0/16" -> "255.255.0.0").
func netmaskFromCIDR(cidr string) string {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return "255.255.0.0" // fallback
	}
	mask := ipnet.Mask
	if len(mask) != 4 {
		return "255.255.0.0"
	}
	return fmt.Sprintf("%d.%d.%d.%d", mask[0], mask[1], mask[2], mask[3])
}

// StartVM starts a Firecracker microVM with the given configuration
func (c *Client) StartVM(ctx context.Context, cfg *VMConfig) (*sdk.Machine, error) {
	// Ensure socket doesn't exist
	os.Remove(cfg.SocketPath)

	// Validate paths
	if _, err := os.Stat(cfg.KernelPath); err != nil {
		return nil, fmt.Errorf("kernel not found at %s: %w", cfg.KernelPath, err)
	}
	if _, err := os.Stat(cfg.RootfsPath); err != nil {
		return nil, fmt.Errorf("rootfs not found at %s: %w", cfg.RootfsPath, err)
	}

	// Default kernel args for a basic Linux boot
	kernelArgs := cfg.KernelArgs
	if kernelArgs == "" {
		kernelArgs = "console=ttyS0 reboot=k panic=1 pci=off"
	}

	// Add IP configuration if provided
	// Format: ip=<client-ip>::<gateway-ip>:<netmask>::eth0:off
	if cfg.IPAddress != "" && cfg.Gateway != "" {
		kernelArgs += fmt.Sprintf(" ip=%s::%s:%s::eth0:off", cfg.IPAddress, cfg.Gateway, netmaskFromCIDR(cfg.Subnet))
	}

	// Build drives list starting with rootfs
	drives := []models.Drive{
		{
			DriveID:      sdk.String("rootfs"),
			PathOnHost:   sdk.String(cfg.RootfsPath),
			IsRootDevice: sdk.Bool(true),
			IsReadOnly:   sdk.Bool(false),
		},
	}

	// Add mount drives (vdb, vdc, etc.)
	for i, mountDrive := range cfg.MountDrives {
		driveID := fmt.Sprintf("mount%d", i)
		drives = append(drives, models.Drive{
			DriveID:      sdk.String(driveID),
			PathOnHost:   sdk.String(mountDrive.ImagePath),
			IsRootDevice: sdk.Bool(false),
			IsReadOnly:   sdk.Bool(mountDrive.ReadOnly),
		})
	}

	// Build Firecracker configuration
	fcCfg := sdk.Config{
		SocketPath:      cfg.SocketPath,
		KernelImagePath: cfg.KernelPath,
		KernelArgs:      kernelArgs,
		Drives:          drives,
		MachineCfg: models.MachineConfiguration{
			VcpuCount:  sdk.Int64(int64(cfg.CPUs)),
			MemSizeMib: sdk.Int64(int64(cfg.MemoryMB)),
		},
	}

	// Add network interface if configured
	if cfg.TapDevice != "" {
		fcCfg.NetworkInterfaces = []sdk.NetworkInterface{
			{
				StaticConfiguration: &sdk.StaticNetworkConfiguration{
					HostDevName: cfg.TapDevice,
					MacAddress:  cfg.MacAddress,
				},
			},
		}
	}

	// Find Firecracker binary
	fcBin := c.FirecrackerBin
	if _, err := os.Stat(fcBin); err != nil {
		// Try to find it in PATH
		if path, err := exec.LookPath("firecracker"); err == nil {
			fcBin = path
		} else {
			return nil, fmt.Errorf("firecracker binary not found at %s or in PATH", c.FirecrackerBin)
		}
	}

	// Set up machine options
	machineOpts := []sdk.Opt{
		sdk.WithLogger(logrus.NewEntry(c.Logger)),
	}

	// Create log file if specified
	if cfg.LogPath != "" {
		logDir := filepath.Dir(cfg.LogPath)
		if err := os.MkdirAll(logDir, 0755); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	// Create the Firecracker command
	cmd := sdk.VMCommandBuilder{}.
		WithBin(fcBin).
		WithSocketPath(cfg.SocketPath).
		Build(ctx)

	machineOpts = append(machineOpts, sdk.WithProcessRunner(cmd))

	// Create the machine
	machine, err := sdk.NewMachine(ctx, fcCfg, machineOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create Firecracker machine: %w", err)
	}

	// Start the machine
	if err := machine.Start(ctx); err != nil {
		return nil, fmt.Errorf("failed to start Firecracker machine: %w", err)
	}

	return machine, nil
}

// StopVM gracefully stops a running Firecracker VM
func (c *Client) StopVM(ctx context.Context, socketPath string) error {
	// Connect to existing machine
	machine, err := c.connectToMachine(ctx, socketPath)
	if err != nil {
		return fmt.Errorf("failed to connect to VM: %w", err)
	}

	// Try graceful shutdown first
	shutdownCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if err := machine.Shutdown(shutdownCtx); err != nil {
		c.Logger.Warnf("Graceful shutdown failed, forcing stop: %v", err)
		// Force stop
		if err := machine.StopVMM(); err != nil {
			return fmt.Errorf("failed to stop VMM: %w", err)
		}
	}

	return nil
}

// connectToMachine connects to an existing Firecracker instance
func (c *Client) connectToMachine(ctx context.Context, socketPath string) (*sdk.Machine, error) {
	if _, err := os.Stat(socketPath); err != nil {
		return nil, fmt.Errorf("socket not found: %w", err)
	}

	// Minimal config just for connecting
	fcCfg := sdk.Config{
		SocketPath: socketPath,
	}

	machine, err := sdk.NewMachine(ctx, fcCfg,
		sdk.WithLogger(logrus.NewEntry(c.Logger)),
	)
	if err != nil {
		return nil, err
	}

	return machine, nil
}

// IsRunning checks if a VM is running by checking the socket and process
func (c *Client) IsRunning(socketPath string, pid int) bool {
	// Check if socket exists
	if _, err := os.Stat(socketPath); err != nil {
		return false
	}

	// Check if process is running and is actually a Firecracker process
	if pid > 0 {
		if !IsFirecrackerProcess(pid) {
			return false
		}
		process, err := os.FindProcess(pid)
		if err != nil {
			return false
		}
		// On Unix, FindProcess always succeeds, so we need to send signal 0
		if err := process.Signal(syscall.Signal(0)); err != nil {
			// EPERM means the process exists but we don't have permission to signal it
			// This happens when the VM runs as root but vmm is run as regular user
			if err == syscall.EPERM {
				return true
			}
			return false
		}
	}

	return true
}

// GetVMPID extracts the PID from the machine (if available)
func (c *Client) GetVMPID(machine *sdk.Machine) int {
	if machine == nil {
		return 0
	}
	pid, _ := machine.PID()
	return pid
}

// UpdateVMState updates the VM struct based on actual state
func (c *Client) UpdateVMState(v *vm.VM) {
	if c.IsRunning(v.SocketPath, v.PID) {
		v.State = vm.StateRunning
	} else {
		if v.State == vm.StateRunning || v.State == vm.StateStarting {
			v.State = vm.StateStopped
		}
	}
}
