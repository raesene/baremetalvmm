package firecracker

import (
	"fmt"
	"os"
	"strings"
)

// IsFirecrackerProcess checks if the given PID belongs to a Firecracker process
// by reading /proc/<pid>/cmdline and verifying it contains "firecracker".
// Returns false if the PID does not exist or does not belong to Firecracker.
func IsFirecrackerProcess(pid int) bool {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	// cmdline uses null bytes as separators
	cmdline := strings.ReplaceAll(string(data), "\x00", " ")
	return strings.Contains(cmdline, "firecracker")
}
