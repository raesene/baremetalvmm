package network

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

// Manager handles network setup for VMs
type Manager struct {
	BridgeName    string
	Subnet        string
	Gateway       string
	HostInterface string

	// run replaces command execution in tests.
	run func(name string, args ...string) error
	// bridgeAddrs replaces reading the bridge's IPv4 addresses in tests.
	bridgeAddrs func() ([]string, error)
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

// prefixLen extracts the prefix length from the subnet CIDR (e.g. "16" from "172.16.0.0/16").
func (m *Manager) prefixLen() string {
	_, ipnet, err := net.ParseCIDR(m.Subnet)
	if err != nil {
		return "16" // fallback
	}
	ones, _ := ipnet.Mask.Size()
	return fmt.Sprintf("%d", ones)
}

// EnsureBridge creates the network bridge if it doesn't exist and ensures NAT is configured
func (m *Manager) EnsureBridge() error {
	// Create bridge if it doesn't exist
	if !m.bridgeExists() {
		if err := m.runCmd("ip", "link", "add", m.BridgeName, "type", "bridge"); err != nil {
			return fmt.Errorf("failed to create bridge: %w", err)
		}
	}

	if err := m.ensureGatewayAddr(); err != nil {
		return err
	}

	// Bring up bridge
	if err := m.runCmd("ip", "link", "set", m.BridgeName, "up"); err != nil {
		return fmt.Errorf("failed to bring up bridge: %w", err)
	}

	// Always ensure IP forwarding is enabled
	if err := m.runCmd("sysctl", "-w", "net.ipv4.ip_forward=1"); err != nil {
		return fmt.Errorf("failed to enable IP forwarding: %w", err)
	}

	// Always ensure NAT rules are in place (setupNAT checks for duplicates)
	if err := m.setupNAT(); err != nil {
		return fmt.Errorf("failed to setup NAT: %w", err)
	}

	if err := m.ensureFirewalldZone(); err != nil {
		return fmt.Errorf("failed to add %s to firewalld's trusted zone: %w", m.BridgeName, err)
	}

	return nil
}

// ensureGatewayAddr gives the bridge the configured gateway address and
// removes any other IPv4 address on it. The bridge outlives vmm runs, so it
// can carry an address from an earlier config (or from a service that read
// the defaults); guests are configured with the current gateway and can't
// reach the host through a stale one.
func (m *Manager) ensureGatewayAddr() error {
	want := m.Gateway + "/" + m.prefixLen()
	if err := m.runCmd("ip", "addr", "replace", want, "dev", m.BridgeName); err != nil {
		return fmt.Errorf("failed to assign gateway IP to bridge: %w", err)
	}

	addrs, err := m.listBridgeAddrs()
	if err != nil {
		return fmt.Errorf("failed to read bridge addresses: %w", err)
	}
	for _, addr := range addrs {
		if addr == want {
			continue
		}
		if err := m.runCmd("ip", "addr", "del", addr, "dev", m.BridgeName); err != nil {
			return fmt.Errorf("failed to remove stale address %s from bridge: %w", addr, err)
		}
	}
	return nil
}

// listBridgeAddrs returns the bridge's IPv4 addresses in CIDR form.
func (m *Manager) listBridgeAddrs() ([]string, error) {
	if m.bridgeAddrs != nil {
		return m.bridgeAddrs()
	}
	iface, err := net.InterfaceByName(m.BridgeName)
	if err != nil {
		return nil, err
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			out = append(out, ipnet.String())
		}
	}
	return out, nil
}

// ensureFirewalldZone puts the bridge in firewalld's trusted zone when
// firewalld is running (the default on Fedora and RHEL). firewalld's own
// nftables table is evaluated separately from the iptables rules above, and
// it rejects forwarded traffic between zones: an unzoned bridge falls into
// the default zone, so VMs lose outbound access whenever the host interface
// is in any other zone. The change is made both at runtime and permanently,
// so it survives a firewall-cmd --reload. Without firewalld this is a no-op.
func (m *Manager) ensureFirewalldZone() error {
	if !m.firewalldRunning() {
		return nil
	}
	if m.runCmd("firewall-cmd", "--zone=trusted", "--query-interface="+m.BridgeName) == nil {
		return nil
	}
	if err := m.runCmd("firewall-cmd", "--zone=trusted", "--change-interface="+m.BridgeName); err != nil {
		return err
	}
	return m.runCmd("firewall-cmd", "--permanent", "--zone=trusted", "--change-interface="+m.BridgeName)
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

// Port forward rules
//
// A forward is two DNAT rules sending <host-port> to <guest-ip>:<guest-port>:
//
//   - PREROUTING, for connections arriving at one of the host's addresses
//     from the network or from other VMs.
//   - OUTPUT, for connections the host itself makes to one of its own
//     addresses (127.0.0.0/8 is excluded; DNAT from loopback needs
//     route_localnet, which vmm does not turn on).
//
// Both match only traffic addressed to the host (addrtype LOCAL), so a VM's
// outbound connection to some remote server on the same port is left alone.
// setupNAT adds the static rules that let the redirected traffic through a
// default-drop FORWARD chain and that make VM-to-VM forwards (hairpin) work.

// ValidateProtocol checks that a port forward protocol is tcp or udp.
func ValidateProtocol(protocol string) error {
	if protocol != "tcp" && protocol != "udp" {
		return fmt.Errorf("invalid protocol %q: must be tcp or udp", protocol)
	}
	return nil
}

func validateForward(hostPort, guestPort int, guestIP, protocol string) error {
	if hostPort < 1 || hostPort > 65535 {
		return fmt.Errorf("invalid host port %d: must be 1-65535", hostPort)
	}
	if guestPort < 1 || guestPort > 65535 {
		return fmt.Errorf("invalid guest port %d: must be 1-65535", guestPort)
	}
	if ip := net.ParseIP(guestIP); ip == nil || ip.To4() == nil {
		return fmt.Errorf("invalid guest IP %q", guestIP)
	}
	return ValidateProtocol(protocol)
}

// forwardRules returns the nat-table rules (chain first) for a forward.
func forwardRules(hostPort, guestPort int, guestIP, protocol string) [][]string {
	dport := strconv.Itoa(hostPort)
	dest := net.JoinHostPort(guestIP, strconv.Itoa(guestPort))
	return [][]string{
		{"PREROUTING", "-p", protocol, "--dport", dport,
			"-m", "addrtype", "--dst-type", "LOCAL",
			"-j", "DNAT", "--to-destination", dest},
		{"OUTPUT", "-p", protocol, "--dport", dport,
			"-m", "addrtype", "--dst-type", "LOCAL", "!", "-d", "127.0.0.0/8",
			"-j", "DNAT", "--to-destination", dest},
	}
}

// legacyForwardRule is the single PREROUTING rule older vmm versions used.
// It matched any destination address, so it is removed whenever its forward
// is re-applied or deleted.
func legacyForwardRule(hostPort, guestPort int, guestIP, protocol string) []string {
	return []string{"PREROUTING", "-p", protocol, "--dport", strconv.Itoa(hostPort),
		"-j", "DNAT", "--to-destination", net.JoinHostPort(guestIP, strconv.Itoa(guestPort))}
}

// ensureRule appends (or with insert, prepends) a rule unless it is present.
func (m *Manager) ensureRule(table string, insert bool, rule []string) error {
	if m.ruleExists(table, rule) {
		return nil
	}
	op := "-A"
	if insert {
		op = "-I"
	}
	return m.runCmd("iptables", append([]string{"-t", table, op}, rule...)...)
}

// ruleExists reports whether a rule is present (iptables -C succeeds).
func (m *Manager) ruleExists(table string, rule []string) bool {
	return m.runCmd("iptables", append([]string{"-t", table, "-C"}, rule...)...) == nil
}

// deleteRule removes a rule if it is present. A missing rule is not an error.
func (m *Manager) deleteRule(table string, rule []string) error {
	if !m.ruleExists(table, rule) {
		return nil
	}
	return m.runCmd("iptables", append([]string{"-t", table, "-D"}, rule...)...)
}

// AddPortForward installs the DNAT rules for a forward. It is idempotent and
// also replaces a rule left over from an older vmm version.
func (m *Manager) AddPortForward(hostPort, guestPort int, guestIP, protocol string) error {
	if err := validateForward(hostPort, guestPort, guestIP, protocol); err != nil {
		return err
	}
	if err := m.setupNAT(); err != nil {
		return fmt.Errorf("failed to set up forwarding rules: %w", err)
	}
	for _, rule := range forwardRules(hostPort, guestPort, guestIP, protocol) {
		if err := m.ensureRule("nat", false, rule); err != nil {
			return fmt.Errorf("failed to add port forward: %w", err)
		}
	}
	if err := m.deleteRule("nat", legacyForwardRule(hostPort, guestPort, guestIP, protocol)); err != nil {
		return fmt.Errorf("failed to remove old-style port forward rule: %w", err)
	}
	return nil
}

// RemovePortForward removes a forward's DNAT rules, including the older
// single-rule form. Rules that are already gone are not an error.
func (m *Manager) RemovePortForward(hostPort, guestPort int, guestIP, protocol string) error {
	if err := validateForward(hostPort, guestPort, guestIP, protocol); err != nil {
		return err
	}
	rules := append(forwardRules(hostPort, guestPort, guestIP, protocol),
		legacyForwardRule(hostPort, guestPort, guestIP, protocol))
	var errs []error
	for _, rule := range rules {
		if err := m.deleteRule("nat", rule); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// PortForwardLegacy reports whether a forward is installed in the form older
// vmm versions used (re-adding it migrates it).
func (m *Manager) PortForwardLegacy(hostPort, guestPort int, guestIP, protocol string) bool {
	if validateForward(hostPort, guestPort, guestIP, protocol) != nil {
		return false
	}
	return m.ruleExists("nat", legacyForwardRule(hostPort, guestPort, guestIP, protocol))
}

// PortForwardActive reports whether a forward's rules are installed.
func (m *Manager) PortForwardActive(hostPort, guestPort int, guestIP, protocol string) bool {
	if validateForward(hostPort, guestPort, guestIP, protocol) != nil {
		return false
	}
	for _, rule := range forwardRules(hostPort, guestPort, guestIP, protocol) {
		if !m.ruleExists("nat", rule) {
			return false
		}
	}
	return true
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

	// Allow forwarding from the bridge to any interface. Matching on the
	// bridge alone rather than a specific host interface keeps VM egress
	// working when the active interface changes (docks, Wi-Fi, VPNs).
	if err := m.runCmd("iptables", "-C", "FORWARD",
		"-i", m.BridgeName, "-j", "ACCEPT"); err != nil {
		if err := m.runCmd("iptables", "-A", "FORWARD",
			"-i", m.BridgeName, "-j", "ACCEPT"); err != nil {
			return err
		}
	}

	// Allow reply traffic back to the bridge (established connections),
	// regardless of which interface it arrives on
	if err := m.runCmd("iptables", "-C", "FORWARD",
		"-o", m.BridgeName,
		"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
		if err := m.runCmd("iptables", "-A", "FORWARD",
			"-o", m.BridgeName,
			"-m", "conntrack", "--ctstate", "RELATED,ESTABLISHED", "-j", "ACCEPT"); err != nil {
			return err
		}
	}

	for _, r := range m.forwardingRules() {
		if err := m.ensureRule(r.table, r.insert, r.rule); err != nil {
			return err
		}
	}

	return nil
}

type staticRule struct {
	table  string
	insert bool
	rule   []string
}

// forwardingRules are the static rules port forwards depend on.
func (m *Manager) forwardingRules() []staticRule {
	return []staticRule{
		// Accept new connections that a port forward redirected to a VM.
		// Inserted at the top so it applies even when the FORWARD policy is
		// DROP (as Docker and ufw set it) and later rules would reject.
		// It matches only DNAT'd traffic, so nothing else reaches the VMs.
		{"filter", true, []string{"FORWARD", "-o", m.BridgeName,
			"-m", "conntrack", "--ctstate", "DNAT", "-j", "ACCEPT"}},
		// Hairpin: when a VM connects to another VM's forward through a host
		// address, rewrite the source so the reply comes back via the host
		// instead of going straight to the client, which would drop it.
		{"nat", false, []string{"POSTROUTING", "-s", m.Subnet, "-o", m.BridgeName,
			"-m", "conntrack", "--ctstate", "DNAT", "-j", "MASQUERADE"}},
	}
}

// firewalldRunning reports whether firewalld is installed and running.
func (m *Manager) firewalldRunning() bool {
	if m.run == nil {
		if _, err := exec.LookPath("firewall-cmd"); err != nil {
			return false
		}
	}
	return m.runCmd("firewall-cmd", "--state") == nil
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

// runCmd executes a command, or the test runner when one is set.
func (m *Manager) runCmd(name string, args ...string) error {
	if m.run != nil {
		return m.run(name, args...)
	}
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w: %s%s", err, string(output), missingExtensionHint(name, output))
	}
	return nil
}

// missingExtensionHint explains iptables' "Extension MASQUERADE revision 0 not
// supported, missing kernel module?". RHEL 10 and its rebuilds (Rocky, Alma)
// ship every xtables extension vmm uses (MASQUERADE, DNAT, conntrack,
// addrtype) in kernel-modules-extra, which minimal and cloud installs leave
// out. The installer adds it, but an older install, or a kernel update that
// didn't bring the package along, still ends up here.
func missingExtensionHint(name string, output []byte) string {
	if name != "iptables" || !bytes.Contains(output, []byte("missing kernel module?")) {
		return ""
	}
	release := "$(uname -r)"
	if b, err := os.ReadFile("/proc/sys/kernel/osrelease"); err == nil {
		release = strings.TrimSpace(string(b))
	}
	return fmt.Sprintf("the running kernel has no iptables extension modules, which vmm needs for VM networking. "+
		"On RHEL, Rocky or AlmaLinux 10 they are in kernel-modules-extra: "+
		"sudo dnf install kernel-modules-extra-%s (if that version is gone, install kernel-modules-extra and reboot into the newest kernel)", release)
}

// GenerateTapName generates a TAP device name for a VM
func GenerateTapName(vmID string) string {
	return fmt.Sprintf("vmm-%s", vmID[:6])
}

// InterfaceIPv4 returns the first IPv4 address on an interface, or "" if it
// has none. It is used to show where a port forward can be reached.
func InterfaceIPv4(name string) string {
	iface, err := net.InterfaceByName(name)
	if err != nil {
		return ""
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return ""
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return ""
}
