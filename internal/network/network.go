package network

import (
	"fmt"
	"net"
	"os/exec"
	"strings"
)

// Manager handles network setup for VMs
type Manager struct {
	BridgeName    string
	Subnet        string
	Gateway       string
	HostInterface string
}

// NewManager creates a new network manager
func NewManager(bridgeName, subnet, gateway, hostInterface string) *Manager {
	return &Manager{
		BridgeName:    bridgeName,
		Subnet:        subnet,
		Gateway:       gateway,
		HostInterface: hostInterface,
	}
}

// EnsureBridge creates the network bridge if it doesn't exist and ensures NAT is configured
func (m *Manager) EnsureBridge() error {
	// Create bridge if it doesn't exist
	if !m.bridgeExists() {
		// Create bridge
		if err := m.runCmd("ip", "link", "add", m.BridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}

		// Set bridge IP
		if err := m.runCmd("ip", "addr", "add", m.Gateway+"/16", "dev", m.BridgeName); err != nil {
			// Might already have an IP, continue
		}

		// Bring up bridge
		if err := m.runCmd("ip", "link", "set", m.BridgeName, "up"); err != nil {
			return fmt.Errorf("failed to bring up bridge: %w", err)
		}
	}

	// Always ensure IP forwarding is enabled
	if err := m.runCmd("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Always ensure NAT rules are in place (setupNAT checks for duplicates)
	if err := m.setupNAT(); err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}

	return nil
}

// CreateTap creates a TAP device for a VM
func (m *Manager) CreateTap(tapName string) error {
	// Create TAP device
	if err := m.runCmd("ip", "tuntap", "add", "dev", tapName, "mode", "tap"); err != nil {
		return fmt.Errorf("failed to create TAP device: %w", err)
	}

	// Add TAP to bridge
	if err := m.runCmd("ip", "link", "set", tapName, "master", m.BridgeName); err != nil {
		m.DeleteTap(tapName) // Cleanup on failure
		return fmt.Errorf("failed to add TAP to bridge: %w", err)
	}

	// Bring up TAP
	if err := m.runCmd("ip", "link", "set", tapName, "up"); err != nil {
		m.DeleteTap(tapName)
		return fmt.Errorf("failed to bring up TAP: %w", err)
	}

	return nil
}

// DeleteTap removes a TAP device
func (m *Manager) DeleteTap(tapName string) error {
	return m.runCmd("ip", "link", "del", tapName)
}

// AllocateIP finds the next free IP in the subnet, skipping any in usedIPs.
// The gateway (.1) is always reserved.
func (m *Manager) AllocateIP(usedIPs []string) (string, error) {
	_, ipnet, err := net.ParseCIDR(m.Subnet)
	if err != nil {
		return "", fmt.Errorf("invalid subnet: %w", err)
	}

	baseIP := ipnet.IP.To4()
	if baseIP == nil {
		return "", fmt.Errorf("invalid IPv4 subnet")
	}

	taken := make(map[string]bool)
	for _, ip := range usedIPs {
		taken[ip] = true
	}
	taken[m.Gateway] = true

	// Start from .2 and find the first unused IP
	for offset := 2; offset < 65534; offset++ {
		candidate := make(net.IP, 4)
		copy(candidate, baseIP)
		candidate[2] = byte(offset / 256)
		candidate[3] = byte(offset % 256)

		if !ipnet.Contains(candidate) {
			break
		}

		addr := candidate.String()
		if !taken[addr] {
			return addr, nil
		}
	}

	return "", fmt.Errorf("no free IP addresses in subnet %s", m.Subnet)
}

// AddPortForward adds a DNAT rule for port forwarding
func (m *Manager) AddPortForward(hostPort, guestPort int, guestIP, protocol string) error {
	rule := fmt.Sprintf("-t nat -A PREROUTING -p %s --dport %d -j DNAT --to-destination %s:%d",
		protocol, hostPort, guestIP, guestPort)

	if err := m.runCmd("iptables", strings.Split(rule, " ")...); err != nil {
		return fmt.Errorf("failed to add port forward: %w", err)
	}

	return nil
}

// RemovePortForward removes a DNAT rule
func (m *Manager) RemovePortForward(hostPort, guestPort int, guestIP, protocol string) error {
	rule := fmt.Sprintf("-t nat -D PREROUTING -p %s --dport %d -j DNAT --to-destination %s:%d",
		protocol, hostPort, guestIP, guestPort)

	return m.runCmd("iptables", strings.Split(rule, " ")...)
}

// setupNAT configures iptables for NAT
func (m *Manager) setupNAT() error {
	// MASQUERADE for outbound traffic (match any interface except the bridge itself)
	if err := m.runCmd("iptables", "-t", "nat", "-C", "POSTROUTING",
		"-s", m.Subnet, "!", "-o", m.BridgeName, "-j", "MASQUERADE"); err != nil {
		// Rule doesn't exist, add it
		if err := m.runCmd("iptables", "-t", "nat", "-A", "POSTROUTING",
			"-s", m.Subnet, "!", "-o", m.BridgeName, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}

	// Allow forwarding from bridge
	if err := m.runCmd("iptables", "-C", "FORWARD",
		"-i", m.BridgeName, "-o", m.HostInterface, "-j", "ACCEPT"); err != nil {
		if err := m.runCmd("iptables", "-A", "FORWARD",
			"-i", m.BridgeName, "-o", m.HostInterface, "-j", "ACCEPT"); err != nil {
			return err
		}
	}

	// Allow forwarding to bridge (established connections)
	if err := m.runCmd("iptables", "-C", "FORWARD",
		"-i", m.HostInterface, "-o", m.BridgeName,
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		if err := m.runCmd("iptables", "-A", "FORWARD",
			"-i", m.HostInterface, "-o", m.BridgeName,
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
			return err
		}
	}

	return nil
}

// bridgeExists checks if the bridge interface exists
func (m *Manager) bridgeExists() bool {
	_, err := net.InterfaceByName(m.BridgeName)
	return err == nil
}

// TapExists checks if a TAP device exists
func (m *Manager) TapExists(tapName string) bool {
	_, err := net.InterfaceByName(tapName)
	return err == nil
}

// runCmd executes a shell command
func (m *Manager) runCmd(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %s", err, string(output))
	}
	return nil
}

// GenerateTapName generates a TAP device name for a VM
func GenerateTapName(vmID string) string {
	return fmt.Sprintf("vmm-%s", vmID[:6])
}
