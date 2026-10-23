package network

import (
	"fmt"
	"net"
	"os/exec"
	"regexp"
	"strings"
)

// Local network restriction keeps a VM off the host's local networks while
// leaving it internet access. It lives in two nftables tables of its own
// rather than the iptables rules above: a drop in any nftables table is
// final, whatever Docker, ufw or firewalld accept in theirs, so the rules
// don't depend on their order in a shared chain.
//
// The bridge table works on the VM's TAP port, which identifies the VM
// without br_netfilter. It drops frames whose MAC and IP aren't the ones vmm
// assigned (so the guest can't take another address), anything other than
// IPv4 and ARP (no IPv6 link-local route to the host), and traffic to or
// from other VMs. The ip table, trusting the source address the bridge table
// has pinned, drops new connections to private and special ranges and to
// the host itself, apart from ping.
//
// Restricted VMs are elements of nftables sets, so the rules don't change as
// VMs come and go. The tables are only created when a restricted VM starts.
const restrictTable = "vmm_restrict"

// localNetworks are the destinations a restricted VM can't open connections
// to: RFC 1918, CGNAT (Tailscale), link-local, "this network", multicast and
// reserved space. The VM subnet is added in case it lies outside them.
var localNetworks = []string{
	"0.0.0.0/8", "10.0.0.0/8", "100.64.0.0/10", "127.0.0.0/8",
	"169.254.0.0/16", "172.16.0.0/12", "192.0.0.0/24", "192.168.0.0/16",
	"198.18.0.0/15", "224.0.0.0/4", "240.0.0.0/4",
}

var tapNamePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,15}$`)

// NftAvailable reports whether the nft command, which local network
// restriction needs, is installed. Debian and minimal EL images lack it.
func NftAvailable() bool {
	_, err := exec.LookPath("nft")
	return err == nil
}

// IsLocalAddress reports whether ip is in a range a restricted VM can't reach.
func IsLocalAddress(ip string) bool {
	addr := net.ParseIP(ip)
	if addr == nil {
		return false
	}
	for _, cidr := range localNetworks {
		if _, ipnet, err := net.ParseCIDR(cidr); err == nil && ipnet.Contains(addr) {
			return true
		}
	}
	return false
}

// SetLocalNetworkRestriction restricts or unrestricts the VM with this TAP
// device, MAC and IP. Restricting creates the tables if needed. Unrestricting
// is a no-op when the tables don't exist, which is the case on every host
// that has never run a restricted VM (nft needn't even be installed).
//
// Start calls it for every VM, restricted or not, so an address left in the
// set by a VM deleted without cleanup can't restrict the next VM to get it.
func (m *Manager) SetLocalNetworkRestriction(restrict bool, tap, mac, ip string) error {
	if !tapNamePattern.MatchString(tap) {
		return fmt.Errorf("invalid TAP device name %q", tap)
	}
	hw, err := net.ParseMAC(mac)
	if err != nil {
		return fmt.Errorf("invalid MAC address %q: %w", mac, err)
	}
	addr := net.ParseIP(ip).To4()
	if addr == nil {
		return fmt.Errorf("invalid IPv4 address %q", ip)
	}
	elements := restrictElements(tap, hw.String(), addr.String())

	if !restrict {
		if !m.restrictTablesExist() {
			return nil
		}
		// Adding first makes the delete succeed whether or not the
		// elements were present; both happen in one transaction.
		return m.runCmd("nft", strings.Join(append(elementCmds("add", elements), elementCmds("delete", elements)...), "\n"))
	}

	if err := m.ensureRestrictTables(); err != nil {
		return fmt.Errorf("failed to set up local network restriction (is nftables installed?): %w", err)
	}
	return m.runCmd("nft", strings.Join(elementCmds("add", elements), "\n"))
}

// restrictTablesExist reports whether both restriction tables are loaded.
func (m *Manager) restrictTablesExist() bool {
	return m.runCmd("nft", "list", "table", "bridge", restrictTable) == nil &&
		m.runCmd("nft", "list", "table", "ip", restrictTable) == nil
}

// ensureRestrictTables loads the restriction rules in one transaction. Sets
// are only added if missing, so their elements survive; the chains are
// flushed and refilled, so a newer vmm's rules replace an older one's.
func (m *Manager) ensureRestrictTables() error {
	return m.runCmd("nft", m.restrictRuleset())
}

func (m *Manager) restrictRuleset() string {
	blocked := append([]string{}, localNetworks...)
	if _, ipnet, err := net.ParseCIDR(m.Subnet); err == nil && !IsLocalAddress(ipnet.IP.String()) {
		blocked = append(blocked, ipnet.String())
	}
	t := restrictTable
	br := fmt.Sprintf("%q", m.BridgeName)
	return strings.Join([]string{
		"add table bridge " + t,
		"add set bridge " + t + " ports { type ifname; }",
		"add set bridge " + t + " bindings { type ifname . ether_addr . ipv4_addr; }",
		"add chain bridge " + t + " forward { type filter hook forward priority 0; policy accept; }",
		"add chain bridge " + t + " input { type filter hook input priority 0; policy accept; }",
		"flush chain bridge " + t + " forward",
		"flush chain bridge " + t + " input",
		// No traffic between a restricted VM and other VMs, either way.
		"add rule bridge " + t + " forward iifname @ports drop",
		"add rule bridge " + t + " forward oifname @ports drop",
		// Towards the host only the VM's own MAC and IP get through.
		"add rule bridge " + t + " input iifname @ports ether type arp iifname . arp saddr ether . arp saddr ip @bindings accept",
		"add rule bridge " + t + " input iifname @ports ether type ip iifname . ether saddr . ip saddr @bindings accept",
		"add rule bridge " + t + " input iifname @ports drop",

		"add table ip " + t,
		"add set ip " + t + " vms { type ipv4_addr; }",
		"add chain ip " + t + " forward { type filter hook forward priority 0; policy accept; }",
		"add chain ip " + t + " input { type filter hook input priority 0; policy accept; }",
		"flush chain ip " + t + " forward",
		"flush chain ip " + t + " input",
		// Replies to connections made to the VM (port forwards, vmm ssh)
		// pass; new connections to local networks don't.
		"add rule ip " + t + " forward iifname " + br + " ip saddr @vms ct state established,related accept",
		"add rule ip " + t + " forward iifname " + br + " ip saddr @vms ip daddr { " + strings.Join(blocked, ", ") + " } drop",
		"add rule ip " + t + " input iifname " + br + " ip saddr @vms ct state established,related accept",
		"add rule ip " + t + " input iifname " + br + " ip saddr @vms icmp type echo-request accept",
		"add rule ip " + t + " input iifname " + br + " ip saddr @vms drop",
	}, "\n")
}

type restrictElement struct {
	family, set, value string
}

func restrictElements(tap, mac, ip string) []restrictElement {
	return []restrictElement{
		{"bridge", "ports", fmt.Sprintf("%q", tap)},
		{"bridge", "bindings", fmt.Sprintf("%q . %s . %s", tap, mac, ip)},
		{"ip", "vms", ip},
	}
}

func elementCmds(op string, elements []restrictElement) []string {
	cmds := make([]string, len(elements))
	for i, e := range elements {
		cmds[i] = fmt.Sprintf("%s element %s %s %s { %s }", op, e.family, restrictTable, e.set, e.value)
	}
	return cmds
}
