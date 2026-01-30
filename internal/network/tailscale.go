package network

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// TailscaleStatus contains the status of Tailscale on the system
type TailscaleStatus struct {
	Installed       bool
	Running         bool
	BackendState    string
	DNSName         string
	IPv4Forwarding  bool
	IPv6Forwarding  bool
	AdvertisedRoute string
}

// TailscaleManager handles Tailscale subnet routing configuration
type TailscaleManager struct {
	Subnet string
}

// NewTailscaleManager creates a new Tailscale manager
func NewTailscaleManager(subnet string) *TailscaleManager {
	return &TailscaleManager{Subnet: subnet}
}

// IsInstalled checks if tailscale CLI is available
func (t *TailscaleManager) IsInstalled() bool {
	_, err := exec.LookPath("tailscale")
	return err == nil
}

// IsRunning checks if tailscale daemon is running and connected
func (t *TailscaleManager) IsRunning() bool {
	cmd := exec.Command("tailscale", "status")
	return cmd.Run() == nil
}

// GetStatus returns comprehensive Tailscale status
func (t *TailscaleManager) GetStatus() (*TailscaleStatus, error) {
	status := &TailscaleStatus{
		Installed:       t.IsInstalled(),
		AdvertisedRoute: t.Subnet,
	}

	if !status.Installed {
		return status, nil
	}

	status.Running = t.IsRunning()
	if !status.Running {
		return status, nil
	}

	// Get detailed status via JSON
	cmd := exec.Command("tailscale", "status", "--json")
	output, err := cmd.Output()
	if err == nil {
		var jsonStatus map[string]interface{}
		if json.Unmarshal(output, &jsonStatus) == nil {
			if self, ok := jsonStatus["Self"].(map[string]interface{}); ok {
				if dnsName, ok := self["DNSName"].(string); ok {
					status.DNSName = dnsName
				}
			}
			if backendState, ok := jsonStatus["BackendState"].(string); ok {
				status.BackendState = backendState
			}
		}
	}

	// Check IP forwarding status
	status.IPv4Forwarding = t.isIPv4ForwardingEnabled()
	status.IPv6Forwarding = t.isIPv6ForwardingEnabled()

	return status, nil
}

// EnableSubnetRouting configures the system as a Tailscale subnet router
func (t *TailscaleManager) EnableSubnetRouting(acceptRoutes bool) error {
	if !t.IsInstalled() {
		return fmt.Errorf("tailscale not found in PATH. Install from https://tailscale.com/download")
	}

	if !t.IsRunning() {
		return fmt.Errorf("tailscale is not running. Run 'sudo tailscale up' first")
	}

	// Enable IP forwarding (required for subnet routing)
	if err := t.enableIPForwarding(); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Configure UDP GRO for better performance
	if err := t.configureUDPGRO(); err != nil {
		// Non-fatal, just log
		fmt.Printf("Warning: failed to configure UDP GRO: %v\n", err)
	}

	// Advertise subnet to tailnet
	tsArgs := []string{"up", "--advertise-routes=" + t.Subnet}
	if acceptRoutes {
		tsArgs = append(tsArgs, "--accept-routes")
	}

	cmd := exec.Command("tailscale", tsArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to advertise subnet: %w", err)
	}

	return nil
}

// DisableSubnetRouting removes subnet advertisement
func (t *TailscaleManager) DisableSubnetRouting() error {
	if !t.IsInstalled() {
		return fmt.Errorf("tailscale not found in PATH")
	}

	cmd := exec.Command("tailscale", "up", "--advertise-routes=")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to disable subnet advertisement: %w", err)
	}

	return nil
}

// enableIPForwarding enables IPv4/IPv6 forwarding and persists the settings
func (t *TailscaleManager) enableIPForwarding() error {
	// Enable IPv4 forwarding
	if err := runSysctl("net.ipv4.ip_forward", "1"); err != nil {
		return fmt.Errorf("IPv4 forwarding: %w", err)
	}

	// Enable IPv6 forwarding (non-fatal if it fails)
	if err := runSysctl("net.ipv6.conf.all.forwarding", "1"); err != nil {
		fmt.Printf("Warning: failed to enable IPv6 forwarding: %v\n", err)
	}

	// Persist settings
	if err := t.persistSysctlSetting("net.ipv4.ip_forward", "1"); err != nil {
		fmt.Printf("Warning: failed to persist IPv4 forwarding: %v\n", err)
	}

	if err := t.persistSysctlSetting("net.ipv6.conf.all.forwarding", "1"); err != nil {
		fmt.Printf("Warning: failed to persist IPv6 forwarding: %v\n", err)
	}

	return nil
}

// persistSysctlSetting adds or updates a sysctl setting in /etc/sysctl.conf
func (t *TailscaleManager) persistSysctlSetting(key, value string) error {
	const sysctlConf = "/etc/sysctl.conf"
	entry := fmt.Sprintf("%s = %s", key, value)

	// Read existing file
	content, err := os.ReadFile(sysctlConf)
	if err != nil && !os.IsNotExist(err) {
		return err
	}

	// Check if setting already exists
	lines := strings.Split(string(content), "\n")
	found := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		// Match the key (with or without spaces around =)
		if strings.HasPrefix(trimmed, key) && strings.Contains(trimmed, "=") {
			lines[i] = entry
			found = true
			break
		}
	}

	if !found {
		// Append new entry
		if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
			lines = append(lines, "")
		}
		lines = append(lines, entry)
	}

	// Write back
	newContent := strings.Join(lines, "\n")
	if !strings.HasSuffix(newContent, "\n") {
		newContent += "\n"
	}

	return os.WriteFile(sysctlConf, []byte(newContent), 0644)
}

// configureUDPGRO enables UDP GRO forwarding on physical network interfaces
func (t *TailscaleManager) configureUDPGRO() error {
	interfaces, err := t.getPhysicalInterfaces()
	if err != nil {
		return err
	}

	var lastErr error
	for _, ifname := range interfaces {
		groPath := fmt.Sprintf("/sys/class/net/%s/udp_gro_forwarding", ifname)
		if _, err := os.Stat(groPath); err == nil {
			if err := os.WriteFile(groPath, []byte("1"), 0644); err != nil {
				lastErr = fmt.Errorf("%s: %w", ifname, err)
			}
		}
	}

	return lastErr
}

// getPhysicalInterfaces returns a list of physical network interfaces
// by checking if they have a device symlink in /sys/class/net
func (t *TailscaleManager) getPhysicalInterfaces() ([]string, error) {
	const netPath = "/sys/class/net"
	entries, err := os.ReadDir(netPath)
	if err != nil {
		return nil, err
	}

	var interfaces []string
	for _, entry := range entries {
		name := entry.Name()
		if name == "lo" {
			continue
		}

		// Check if this is a physical interface by looking for a device symlink
		// Virtual interfaces (bridges, TAPs, etc.) don't have this
		devicePath := fmt.Sprintf("%s/%s/device", netPath, name)
		if _, err := os.Stat(devicePath); err == nil {
			interfaces = append(interfaces, name)
		}
	}

	return interfaces, nil
}

// isIPv4ForwardingEnabled checks if IPv4 forwarding is enabled
func (t *TailscaleManager) isIPv4ForwardingEnabled() bool {
	cmd := exec.Command("sysctl", "-n", "net.ipv4.ip_forward")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "1"
}

// isIPv6ForwardingEnabled checks if IPv6 forwarding is enabled
func (t *TailscaleManager) isIPv6ForwardingEnabled() bool {
	cmd := exec.Command("sysctl", "-n", "net.ipv6.conf.all.forwarding")
	output, err := cmd.Output()
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(output)) == "1"
}

// runSysctl sets a sysctl value
func runSysctl(key, value string) error {
	cmd := exec.Command("sysctl", "-w", fmt.Sprintf("%s=%s", key, value))
	if output, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}
	return nil
}

// FormatAcceptInstructions returns instructions for accepting routes on another device
func (t *TailscaleManager) FormatAcceptInstructions() string {
	var b strings.Builder
	b.WriteString("To access VMs from another Tailscale device:\n\n")
	b.WriteString("1. Ensure you're logged into Tailscale on that device:\n")
	b.WriteString("   tailscale up\n\n")
	b.WriteString("2. Accept advertised subnet routes:\n")
	b.WriteString("   sudo tailscale up --accept-routes\n\n")
	b.WriteString("3. Verify routes are accepted:\n")
	b.WriteString("   tailscale status\n\n")
	b.WriteString("4. Access VMs using their IP addresses:\n")
	b.WriteString("   - SSH: ssh root@172.16.0.2\n")
	b.WriteString("   - HTTP: curl http://172.16.0.2:8080\n\n")
	b.WriteString("To see running VMs and their IPs:\n")
	b.WriteString("   vmm list\n")
	return b.String()
}
