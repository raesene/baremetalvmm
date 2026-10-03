// Package portforward manages a VM's host-to-guest port forwards: saving them
// on the VM, and keeping the iptables rules in step with the VM's state.
//
// Forwards are saved on the VM whether or not it is running. Their rules are
// installed only while the VM runs: Apply on start or restore, Clear on stop
// or delete. Add and Remove change a running VM's forwards immediately.
// The CLI and web UI both go through this package so they behave the same.
package portforward

import (
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/raesene/baremetalvmm/internal/network"
	"github.com/raesene/baremetalvmm/internal/vm"
)

var (
	// ErrExists is returned by Add when the VM already has the forward.
	ErrExists = errors.New("port forward already exists")
	// ErrNotFound is returned by Remove when the VM has no such forward.
	ErrNotFound = errors.New("port forward not found")
)

// Rules is the subset of network.Manager this package needs.
type Rules interface {
	AddPortForward(hostPort, guestPort int, guestIP, protocol string) error
	RemovePortForward(hostPort, guestPort int, guestIP, protocol string) error
	PortForwardActive(hostPort, guestPort int, guestIP, protocol string) bool
	PortForwardLegacy(hostPort, guestPort int, guestIP, protocol string) bool
}

var _ Rules = (*network.Manager)(nil)

// Validate checks a forward's ports and protocol.
func Validate(pf vm.PortForward) error {
	if pf.HostPort < 1 || pf.HostPort > 65535 {
		return fmt.Errorf("invalid host port %d: must be 1-65535", pf.HostPort)
	}
	if pf.GuestPort < 1 || pf.GuestPort > 65535 {
		return fmt.Errorf("invalid guest port %d: must be 1-65535", pf.GuestPort)
	}
	return network.ValidateProtocol(pf.Protocol)
}

// Parse reads "host:guest" or "host:guest/proto". The protocol defaults to
// defaultProto (tcp if empty) when the spec doesn't name one.
func Parse(spec, defaultProto string) (vm.PortForward, error) {
	if defaultProto == "" {
		defaultProto = "tcp"
	}
	ports, proto, hasProto := strings.Cut(spec, "/")
	if !hasProto {
		proto = defaultProto
	}
	hostStr, guestStr, ok := strings.Cut(ports, ":")
	if !ok {
		return vm.PortForward{}, fmt.Errorf("invalid port spec %q, expected host-port:guest-port[/tcp|udp]", spec)
	}
	hostPort, err1 := strconv.Atoi(hostStr)
	guestPort, err2 := strconv.Atoi(guestStr)
	if err1 != nil || err2 != nil {
		return vm.PortForward{}, fmt.Errorf("invalid port spec %q, expected host-port:guest-port[/tcp|udp]", spec)
	}
	pf := vm.PortForward{HostPort: hostPort, GuestPort: guestPort, Protocol: strings.ToLower(proto)}
	return pf, Validate(pf)
}

// Format renders a forward as "8080:80/tcp", the form Parse accepts.
func Format(pf vm.PortForward) string {
	return fmt.Sprintf("%d:%d/%s", pf.HostPort, pf.GuestPort, pf.Protocol)
}

// protocolOf treats a forward saved without a protocol as tcp, which is what
// vmm always used for those.
func protocolOf(pf vm.PortForward) string {
	if pf.Protocol == "" {
		return "tcp"
	}
	return pf.Protocol
}

// Conflict reports another VM, or the same VM, already using pf's host port
// and protocol. Each host port/protocol can only lead to one place.
func Conflict(all []*vm.VM, self string, pf vm.PortForward) error {
	for _, other := range all {
		for _, existing := range other.PortForwards {
			if existing.HostPort != pf.HostPort || protocolOf(existing) != pf.Protocol {
				continue
			}
			if other.Name == self {
				if existing.GuestPort == pf.GuestPort {
					return ErrExists
				}
				return fmt.Errorf("host port %d/%s is already forwarded to guest port %d on this VM",
					pf.HostPort, pf.Protocol, existing.GuestPort)
			}
			return fmt.Errorf("host port %d/%s is already forwarded to VM '%s'", pf.HostPort, pf.Protocol, other.Name)
		}
	}
	return nil
}

// CheckNew validates the forwards for a VM being created: each must be valid
// and must not clash with another VM's forwards or with each other.
func CheckNew(all []*vm.VM, name string, pfs []vm.PortForward) error {
	pending := &vm.VM{Name: name}
	withPending := append(append([]*vm.VM{}, all...), pending)
	for _, pf := range pfs {
		if err := Validate(pf); err != nil {
			return err
		}
		if err := Conflict(withPending, name, pf); err != nil {
			if errors.Is(err, ErrExists) {
				return fmt.Errorf("port forward %s is listed twice", Format(pf))
			}
			return err
		}
		pending.PortForwards = append(pending.PortForwards, pf)
	}
	return nil
}

// listenCheck is replaced in tests.
var listenCheck = hostPortInUse

// hostPortInUse reports a service on the host already bound to the port. A
// forward would capture that service's traffic from other machines.
func hostPortInUse(port int, protocol string) error {
	addr := ":" + strconv.Itoa(port)
	var err error
	if protocol == "udp" {
		var c net.PacketConn
		if c, err = net.ListenPacket("udp", addr); err == nil {
			return c.Close()
		}
	} else {
		var l net.Listener
		if l, err = net.Listen("tcp", addr); err == nil {
			return l.Close()
		}
	}
	if errors.Is(err, syscall.EADDRINUSE) {
		return fmt.Errorf("host port %d/%s is already in use by a service on this host", port, protocol)
	}
	// Anything else (such as no permission for a low port when not root)
	// says nothing about whether the port is free.
	return nil
}

// Apply installs the rules for every forward on a running VM. It keeps going
// past failures and returns them joined.
func Apply(r Rules, v *vm.VM) error {
	if v.IPAddress == "" {
		return nil
	}
	var errs []error
	for _, pf := range v.PortForwards {
		if err := r.AddPortForward(pf.HostPort, pf.GuestPort, v.IPAddress, protocolOf(pf)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", Format(pf), err))
		}
	}
	return errors.Join(errs...)
}

// Clear removes the rules for every forward on a VM. The forwards stay saved
// so Apply can restore them on the next start.
func Clear(r Rules, v *vm.VM) error {
	if v.IPAddress == "" {
		return nil
	}
	var errs []error
	for _, pf := range v.PortForwards {
		if err := r.RemovePortForward(pf.HostPort, pf.GuestPort, v.IPAddress, protocolOf(pf)); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", Format(pf), err))
		}
	}
	return errors.Join(errs...)
}

// Add saves a new forward on v and, if v is running, installs it at once.
// all is every VM on the host, for conflict checks; v must have its current
// state (see firecracker.Client.UpdateVMState). It reports whether the
// forward is live now. The caller saves v on success.
func Add(r Rules, all []*vm.VM, v *vm.VM, pf vm.PortForward) (live bool, err error) {
	if err := Validate(pf); err != nil {
		return false, err
	}
	if err := Conflict(all, v.Name, pf); err != nil {
		return false, err
	}
	if err := listenCheck(pf.HostPort, pf.Protocol); err != nil {
		return false, err
	}
	if v.State == vm.StateRunning && v.IPAddress != "" {
		if err := r.AddPortForward(pf.HostPort, pf.GuestPort, v.IPAddress, pf.Protocol); err != nil {
			return false, err
		}
		live = true
	}
	v.PortForwards = append(v.PortForwards, pf)
	return live, nil
}

// Find returns the index of a forward on v. An empty protocol matches either,
// as long as only one forward has those ports.
func Find(v *vm.VM, hostPort, guestPort int, protocol string) (int, error) {
	found := -1
	for i, pf := range v.PortForwards {
		if pf.HostPort != hostPort || pf.GuestPort != guestPort {
			continue
		}
		if protocol != "" && protocolOf(pf) != protocol {
			continue
		}
		if found != -1 {
			return -1, fmt.Errorf("both tcp and udp forwards use %d:%d; say which to remove", hostPort, guestPort)
		}
		found = i
	}
	if found == -1 {
		return -1, ErrNotFound
	}
	return found, nil
}

// Remove deletes forward i from v and its rules. The caller saves v.
func Remove(r Rules, v *vm.VM, i int) (vm.PortForward, error) {
	pf := v.PortForwards[i]
	if v.IPAddress != "" {
		if err := r.RemovePortForward(pf.HostPort, pf.GuestPort, v.IPAddress, protocolOf(pf)); err != nil {
			return pf, err
		}
	}
	v.PortForwards = append(v.PortForwards[:i:i], v.PortForwards[i+1:]...)
	return pf, nil
}

// Status describes whether a forward is passing traffic.
type Status string

const (
	// StatusActive: the VM is running and the rules are installed.
	StatusActive Status = "active"
	// StatusMissing: the VM is running but the rules are not installed, for
	// example after a start by an older vmm. Apply fixes it.
	StatusMissing Status = "missing"
	// StatusOutdated: the VM is running with the rule an older vmm wrote,
	// which also catches the VM's outbound traffic to that port on other
	// hosts. Apply replaces it.
	StatusOutdated Status = "outdated"
	// StatusInactive: the VM is not running; the forward applies on start.
	StatusInactive Status = "inactive"
)

// Entry is one forward in the host-wide listing.
type Entry struct {
	VMName    string   `json:"vm"`
	VMState   vm.State `json:"vm_state"`
	GuestIP   string   `json:"guest_ip"`
	HostPort  int      `json:"host_port"`
	GuestPort int      `json:"guest_port"`
	Protocol  string   `json:"protocol"`
	Status    Status   `json:"status"`
}

// List returns every forward on every VM, ordered by host port. VMs must have
// their current state.
func List(r Rules, all []*vm.VM) []Entry {
	var entries []Entry
	for _, v := range all {
		for _, pf := range v.PortForwards {
			e := Entry{
				VMName:    v.Name,
				VMState:   v.State,
				GuestIP:   v.IPAddress,
				HostPort:  pf.HostPort,
				GuestPort: pf.GuestPort,
				Protocol:  protocolOf(pf),
				Status:    StatusInactive,
			}
			if v.State == vm.StateRunning && v.IPAddress != "" {
				switch {
				case r.PortForwardActive(pf.HostPort, pf.GuestPort, v.IPAddress, e.Protocol):
					e.Status = StatusActive
				case r.PortForwardLegacy(pf.HostPort, pf.GuestPort, v.IPAddress, e.Protocol):
					e.Status = StatusOutdated
				default:
					e.Status = StatusMissing
				}
			}
			entries = append(entries, e)
		}
	}
	sort.SliceStable(entries, func(i, j int) bool {
		if entries[i].HostPort != entries[j].HostPort {
			return entries[i].HostPort < entries[j].HostPort
		}
		return entries[i].Protocol < entries[j].Protocol
	})
	return entries
}
