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

// consoleLogPath derives the console log path from the VM log path.
// e.g. /var/lib/vmm/logs/myvm.log -> /var/lib/vmm/logs/myvm-console.log
func consoleLogPath(logPath string) string {
	ext := filepath.Ext(logPath)
	return logPath[:len(logPath)-len(ext)] + "-console" + ext
}

// ConsoleLogPath returns the path to the console log for a VM given its log path.
func ConsoleLogPath(logPath string) string {
	return consoleLogPath(logPath)
}

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

	// Create log directory and console log file
	if cfg.LogPath != "" {
		logDir := filepath.Dir(cfg.LogPath)
		if err := os.MkdirAll(logDir, 0700); err != nil {
			return nil, fmt.Errorf("failed to create log directory: %w", err)
		}
	}

	// Build Firecracker command, capturing serial console output to a log file
	cmdBuilder := sdk.VMCommandBuilder{}.
		WithBin(fcBin).
		WithSocketPath(cfg.SocketPath)

	if cfg.LogPath != "" {
		consolePath := consoleLogPath(cfg.LogPath)
		consoleFile, err := os.OpenFile(consolePath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
		if err != nil {
			return nil, fmt.Errorf("failed to create console log file: %w", err)
		}
		cmdBuilder = cmdBuilder.WithStdout(consoleFile).WithStderr(consoleFile)
	}

	cmd := cmdBuilder.Build(ctx)

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

// Terminate stops the Firecracker process backing a VM and does not return
// until the process is gone. It escalates from a guest shutdown request
// (Ctrl+Alt+Del over the API socket) to SIGTERM and finally SIGKILL, then
// removes the API socket file.
//
// The VM's PID field is updated to reflect reality: it is set to the PID that
// was actually terminated while work is in progress, and cleared to 0 once the
// process is confirmed gone. An error is returned only when a process is still
// running afterwards, in which case PID still holds the surviving PID so the
// caller can report it rather than orphan it.
func (c *Client) Terminate(ctx context.Context, v *vm.VM) error {
	pid := ResolvePID(v.SocketPath, v.PID)
	if pid == 0 {
		// Nothing running for this VM
		v.PID = 0
		if err := os.Remove(v.SocketPath); err != nil && !os.IsNotExist(err) {
			c.Logger.Debugf("failed to remove socket %s: %v", v.SocketPath, err)
		}
		return nil
	}
	v.PID = pid

	// Ask the guest to shut down cleanly. This only works when the guest
	// kernel has the i8042 driver, so don't wait long for it.
	if _, err := os.Stat(v.SocketPath); err == nil {
		shutdownCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := c.StopVM(shutdownCtx, v.SocketPath)
		cancel()
		if err != nil {
			c.Logger.Debugf("graceful shutdown request failed for VM %s: %v", v.Name, err)
		} else if waitForExit(pid, v.SocketPath, gracefulShutdownWait) {
			return c.finishTerminate(v)
		}
	}

	// Escalate: SIGTERM makes Firecracker exit and tear down the microVM.
	if signalAndWait(pid, v.SocketPath, syscall.SIGTERM, sigtermWait) {
		return c.finishTerminate(v)
	}

	c.Logger.Warnf("Firecracker process %d for VM %s ignored SIGTERM, sending SIGKILL", pid, v.Name)
	if signalAndWait(pid, v.SocketPath, syscall.SIGKILL, sigkillWait) {
		return c.finishTerminate(v)
	}

	return fmt.Errorf("firecracker process %d for VM '%s' is still running after SIGKILL", pid, v.Name)
}

// finishTerminate records that the VM's process is gone and cleans up the socket.
func (c *Client) finishTerminate(v *vm.VM) error {
	v.PID = 0
	if err := os.Remove(v.SocketPath); err != nil && !os.IsNotExist(err) {
		c.Logger.Debugf("failed to remove socket %s: %v", v.SocketPath, err)
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

// IsRunning checks if a VM is running by looking for its Firecracker process.
// The process is authoritative: a missing socket file does not mean the VM is
// gone, and a stale recorded PID does not mean it is alive.
func (c *Client) IsRunning(socketPath string, pid int) bool {
	return ResolvePID(socketPath, pid) > 0
}

// GetVMPID extracts the PID from the machine (if available)
func (c *Client) GetVMPID(machine *sdk.Machine) int {
	if machine == nil {
		return 0
	}
	pid, _ := machine.PID()
	return pid
}

// UpdateVMState updates the VM struct based on actual state. When a running
// Firecracker process is found, the VM's PID is repaired to match it so that
// later stop/delete operations can terminate the right process.
func (c *Client) UpdateVMState(v *vm.VM) {
	if pid := ResolvePID(v.SocketPath, v.PID); pid > 0 {
		v.PID = pid
		v.State = vm.StateRunning
	} else {
		v.PID = 0
		if v.State == vm.StateRunning || v.State == vm.StateStarting || v.State == vm.StateStopping {
			v.State = vm.StateStopped
		}
	}
}
