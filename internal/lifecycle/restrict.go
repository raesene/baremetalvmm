package lifecycle

import (
	"fmt"

	"github.com/raesene/baremetalvmm/internal/image"
	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/vm"
)

// ApplyRestriction brings the firewall in line with the VM's
// RestrictLocalNetworks setting. It is called for every VM that starts, so
// it also clears a restriction left on the VM's address by a VM deleted
// without cleanup. The VM needs its IP address.
func ApplyRestriction(netMgr *network.Manager, v *vm.VM) error {
	return netMgr.SetLocalNetworkRestriction(v.RestrictLocalNetworks, v.TapDevice, v.MacAddress, v.IPAddress)
}

// CheckRestrictedDNS refuses DNS servers a VM with restricted local networks
// couldn't reach. No servers means the public defaults.
func CheckRestrictedDNS(servers []string) error {
	if len(servers) == 0 {
		servers = image.DefaultDNSServers
	}
	for _, s := range servers {
		if network.IsLocalAddress(s) {
			return fmt.Errorf("DNS server %s is on a local network, which a VM with restricted local networks can't reach; use a public DNS server with --dns", s)
		}
	}
	return nil
}
