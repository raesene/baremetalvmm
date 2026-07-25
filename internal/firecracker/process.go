package firecracker

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// gracefulShutdownWait is how long to wait for the guest to power itself
	// off after a Ctrl+Alt+Del request before escalating to signals. Guest
	// kernels built without CONFIG_SERIO_I8042/CONFIG_KEYBOARD_ATKBD never
	// see the request, so this is kept short.
	gracefulShutdownWait = 3 * time.Second
	// sigtermWait is how long to wait for Firecracker to exit after SIGTERM.
	sigtermWait = 5 * time.Second
	// sigkillWait is how long to wait for the process to disappear after SIGKILL.
	sigkillWait = 3 * time.Second
	// exitPollInterval is how often process liveness is polled while waiting.
	exitPollInterval = 100 * time.Millisecond
)

// procArgs returns the argument vector of a process from /proc/<pid>/cmdline.
// Returns nil if the process does not exist or cannot be read.
func procArgs(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return nil
	}
	args := strings.Split(string(data), "\x00")
	// cmdline is null-terminated, so the split leaves a trailing empty element
	for len(args) > 0 && args[len(args)-1] == "" {
		args = args[:len(args)-1]
	}
	return args
}

// IsFirecrackerProcess checks if the given PID belongs to a Firecracker process
// by reading /proc/<pid>/cmdline and verifying argv[0] is the Firecracker binary.
// Returns false if the PID does not exist or does not belong to Firecracker.
func IsFirecrackerProcess(pid int) bool {
	args := procArgs(pid)
	if len(args) == 0 {
		return false
	}
	return strings.Contains(filepath.Base(args[0]), "firecracker")
}

// ProcessMatchesSocket reports whether pid is a Firecracker process serving the
// given API socket. This is stronger than IsFirecrackerProcess: it protects
// against acting on a recycled PID that belongs to a different VM.
func ProcessMatchesSocket(pid int, socketPath string) bool {
	if pid <= 0 || socketPath == "" {
		return false
	}
	args := procArgs(pid)
	if len(args) == 0 || !strings.Contains(filepath.Base(args[0]), "firecracker") {
		return false
	}
	for _, arg := range args[1:] {
		if arg == socketPath {
			return true
		}
	}
	return false
}

// FindPIDForSocket scans /proc for a running Firecracker process whose API
// socket is socketPath. Returns 0 if none is found. This recovers the PID of a
// VM whose recorded PID was lost (for example cleared by an earlier stop that
// did not actually terminate the process).
func FindPIDForSocket(socketPath string) int {
	if socketPath == "" {
		return 0
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return 0
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}
		if ProcessMatchesSocket(pid, socketPath) {
			return pid
		}
	}
	return 0
}

// ResolvePID returns the PID of the live Firecracker process for a VM. It
// prefers the recorded PID when that still refers to the VM's Firecracker
// process, and otherwise falls back to scanning /proc for the API socket.
// Returns 0 when no matching process is running.
func ResolvePID(socketPath string, recordedPID int) int {
	if ProcessMatchesSocket(recordedPID, socketPath) {
		return recordedPID
	}
	return FindPIDForSocket(socketPath)
}

// waitForExit polls until the process is gone or the timeout expires.
// Returns true if the process exited.
func waitForExit(pid int, socketPath string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !ProcessMatchesSocket(pid, socketPath) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(exitPollInterval)
	}
}

// signalAndWait sends sig to pid and waits up to timeout for it to exit.
// Returns true if the process exited.
func signalAndWait(pid int, socketPath string, sig syscall.Signal, timeout time.Duration) bool {
	if err := syscall.Kill(pid, sig); err != nil {
		// ESRCH means the process is already gone
		return err == syscall.ESRCH
	}
	return waitForExit(pid, socketPath, timeout)
}
