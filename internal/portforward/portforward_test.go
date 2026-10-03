package portforward

import (
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/raesene/baremetalvmm/internal/vm"
)

// fakeRules tracks installed forwards as "ip host:guest/proto" strings.
type fakeRules struct {
	active map[string]bool
	legacy map[string]bool
	fail   error
}

func newFakeRules() *fakeRules {
	return &fakeRules{active: map[string]bool{}, legacy: map[string]bool{}}
}

func (f *fakeRules) PortForwardLegacy(h, g int, ip, proto string) bool {
	return f.legacy[key(h, g, ip, proto)]
}

func key(h, g int, ip, proto string) string { return fmt.Sprintf("%s %d:%d/%s", ip, h, g, proto) }

func (f *fakeRules) AddPortForward(h, g int, ip, proto string) error {
	if f.fail != nil {
		return f.fail
	}
	f.active[key(h, g, ip, proto)] = true
	delete(f.legacy, key(h, g, ip, proto))
	return nil
}

func (f *fakeRules) RemovePortForward(h, g int, ip, proto string) error {
	delete(f.active, key(h, g, ip, proto))
	return nil
}

func (f *fakeRules) PortForwardActive(h, g int, ip, proto string) bool {
	return f.active[key(h, g, ip, proto)]
}

func noListen(t *testing.T) {
	t.Helper()
	orig := listenCheck
	listenCheck = func(int, string) error { return nil }
	t.Cleanup(func() { listenCheck = orig })
}

func runningVM(name, ip string, pfs ...vm.PortForward) *vm.VM {
	return &vm.VM{Name: name, IPAddress: ip, State: vm.StateRunning, PortForwards: pfs}
}

func TestParse(t *testing.T) {
	tests := []struct {
		spec, def string
		want      vm.PortForward
		wantErr   bool
	}{
		{spec: "8080:80", want: vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}},
		{spec: "8080:80", def: "udp", want: vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "udp"}},
		{spec: "5353:53/udp", want: vm.PortForward{HostPort: 5353, GuestPort: 53, Protocol: "udp"}},
		{spec: "5353:53/UDP", want: vm.PortForward{HostPort: 5353, GuestPort: 53, Protocol: "udp"}},
		{spec: "8080:80/tcp", def: "udp", want: vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}},
		{spec: "8080", wantErr: true},
		{spec: "a:80", wantErr: true},
		{spec: "8080:80/sctp", wantErr: true},
		{spec: "0:80", wantErr: true},
		{spec: "8080:65536", wantErr: true},
		{spec: "8080:80:90", wantErr: true},
	}
	for _, tt := range tests {
		got, err := Parse(tt.spec, tt.def)
		if (err != nil) != tt.wantErr {
			t.Errorf("Parse(%q, %q) err = %v, wantErr %v", tt.spec, tt.def, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got != tt.want {
			t.Errorf("Parse(%q, %q) = %+v, want %+v", tt.spec, tt.def, got, tt.want)
		}
	}
}

func TestAddToRunningVMIsLive(t *testing.T) {
	noListen(t)
	r := newFakeRules()
	v := runningVM("web", "172.16.0.5")
	pf := vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}

	live, err := Add(r, []*vm.VM{v}, v, pf)
	if err != nil || !live {
		t.Fatalf("Add = %v, %v; want live", live, err)
	}
	if !r.active[key(8080, 80, "172.16.0.5", "tcp")] {
		t.Error("rules not installed for running VM")
	}
	if len(v.PortForwards) != 1 {
		t.Errorf("forward not saved on VM: %+v", v.PortForwards)
	}
}

func TestAddToStoppedVMIsSavedOnly(t *testing.T) {
	noListen(t)
	r := newFakeRules()
	v := &vm.VM{Name: "web", IPAddress: "172.16.0.5", State: vm.StateStopped}

	live, err := Add(r, []*vm.VM{v}, v, vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"})
	if err != nil || live {
		t.Fatalf("Add = %v, %v; want saved but not live", live, err)
	}
	if len(r.active) != 0 {
		t.Error("rules installed for a stopped VM")
	}
	if len(v.PortForwards) != 1 {
		t.Error("forward not saved")
	}
}

func TestAddConflicts(t *testing.T) {
	noListen(t)
	r := newFakeRules()
	other := runningVM("db", "172.16.0.6", vm.PortForward{HostPort: 8080, GuestPort: 5432, Protocol: "tcp"})
	legacy := runningVM("old", "172.16.0.7", vm.PortForward{HostPort: 9000, GuestPort: 9000}) // no protocol saved
	v := runningVM("web", "172.16.0.5", vm.PortForward{HostPort: 8443, GuestPort: 443, Protocol: "tcp"})
	all := []*vm.VM{other, legacy, v}

	if _, err := Add(r, all, v, vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}); err == nil || !strings.Contains(err.Error(), "'db'") {
		t.Errorf("host port used by another VM: err = %v", err)
	}
	if _, err := Add(r, all, v, vm.PortForward{HostPort: 9000, GuestPort: 80, Protocol: "tcp"}); err == nil {
		t.Error("forward saved without protocol was not treated as tcp")
	}
	if _, err := Add(r, all, v, vm.PortForward{HostPort: 8443, GuestPort: 443, Protocol: "tcp"}); !errors.Is(err, ErrExists) {
		t.Errorf("identical forward: err = %v, want ErrExists", err)
	}
	if _, err := Add(r, all, v, vm.PortForward{HostPort: 8443, GuestPort: 8443, Protocol: "tcp"}); err == nil {
		t.Error("same host port to a different guest port on the same VM was accepted")
	}
	// The same port number over the other protocol is a different forward.
	if _, err := Add(r, all, v, vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "udp"}); err != nil {
		t.Errorf("udp on a port used for tcp elsewhere: %v", err)
	}
}

func TestAddRejectsPortInUseOnHost(t *testing.T) {
	l, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Skipf("cannot listen: %v", err)
	}
	defer l.Close()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("unexpected listener address %v", l.Addr())
	}
	port := addr.Port

	r := newFakeRules()
	v := runningVM("web", "172.16.0.5")
	_, err = Add(r, []*vm.VM{v}, v, vm.PortForward{HostPort: port, GuestPort: 80, Protocol: "tcp"})
	if err == nil || !strings.Contains(err.Error(), "in use") {
		t.Fatalf("Add on a port with a host listener: err = %v", err)
	}
	if len(v.PortForwards) != 0 || len(r.active) != 0 {
		t.Error("forward saved or applied despite the conflict")
	}
}

func TestAddRuleFailureSavesNothing(t *testing.T) {
	noListen(t)
	r := newFakeRules()
	r.fail = errors.New("iptables broke")
	v := runningVM("web", "172.16.0.5")
	if _, err := Add(r, []*vm.VM{v}, v, vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}); err == nil {
		t.Fatal("Add succeeded despite the rule failing")
	}
	if len(v.PortForwards) != 0 {
		t.Error("forward saved although its rules failed")
	}
}

func TestFindAndRemove(t *testing.T) {
	r := newFakeRules()
	v := runningVM("web", "172.16.0.5",
		vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"},
		vm.PortForward{HostPort: 5353, GuestPort: 53, Protocol: "tcp"},
		vm.PortForward{HostPort: 5353, GuestPort: 53, Protocol: "udp"},
	)
	if err := Apply(r, v); err != nil {
		t.Fatal(err)
	}

	if _, err := Find(v, 5353, 53, ""); err == nil {
		t.Error("ambiguous tcp/udp match was not reported")
	}
	if _, err := Find(v, 1, 2, ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing forward: err = %v, want ErrNotFound", err)
	}

	i, err := Find(v, 5353, 53, "udp")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Remove(r, v, i); err != nil {
		t.Fatal(err)
	}
	if r.active[key(5353, 53, "172.16.0.5", "udp")] {
		t.Error("udp rules still installed after remove")
	}
	if !r.active[key(5353, 53, "172.16.0.5", "tcp")] {
		t.Error("tcp forward on the same ports was removed too")
	}
	if len(v.PortForwards) != 2 {
		t.Errorf("forwards after remove = %+v", v.PortForwards)
	}

	// With only the tcp one left, an empty protocol finds it.
	if _, err := Find(v, 5353, 53, ""); err != nil {
		t.Errorf("unambiguous match: %v", err)
	}
}

func TestApplyClearAndList(t *testing.T) {
	r := newFakeRules()
	web := runningVM("web", "172.16.0.5", vm.PortForward{HostPort: 8080, GuestPort: 80, Protocol: "tcp"})
	stopped := &vm.VM{Name: "batch", IPAddress: "172.16.0.6", State: vm.StateStopped,
		PortForwards: []vm.PortForward{{HostPort: 2222, GuestPort: 22, Protocol: "tcp"}}}
	lost := runningVM("lost", "172.16.0.7", vm.PortForward{HostPort: 3000, GuestPort: 3000})
	old := runningVM("old", "172.16.0.8", vm.PortForward{HostPort: 4000, GuestPort: 80, Protocol: "tcp"})
	r.legacy[key(4000, 80, "172.16.0.8", "tcp")] = true

	if err := Apply(r, web); err != nil {
		t.Fatal(err)
	}
	entries := List(r, []*vm.VM{web, stopped, lost, old})
	got := map[string]Status{}
	var order []int
	for _, e := range entries {
		got[e.VMName] = e.Status
		order = append(order, e.HostPort)
	}
	if got["web"] != StatusActive || got["batch"] != StatusInactive || got["lost"] != StatusMissing || got["old"] != StatusOutdated {
		t.Errorf("statuses = %v", got)
	}
	if fmt.Sprint(order) != "[2222 3000 4000 8080]" {
		t.Errorf("entries not ordered by host port: %v", order)
	}
	if entries[1].Protocol != "tcp" {
		t.Errorf("forward without saved protocol listed as %q, want tcp", entries[1].Protocol)
	}

	if err := Clear(r, web); err != nil {
		t.Fatal(err)
	}
	if len(r.active) != 0 {
		t.Errorf("rules left after Clear: %v", r.active)
	}
	if len(web.PortForwards) != 1 {
		t.Error("Clear dropped the saved forward")
	}
}

func TestCheckNew(t *testing.T) {
	other := runningVM("db", "172.16.0.6", vm.PortForward{HostPort: 5432, GuestPort: 5432, Protocol: "tcp"})
	all := []*vm.VM{other}
	ok := []vm.PortForward{{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}, {HostPort: 8080, GuestPort: 80, Protocol: "udp"}}
	if err := CheckNew(all, "web", ok); err != nil {
		t.Errorf("valid forwards rejected: %v", err)
	}
	for name, pfs := range map[string][]vm.PortForward{
		"clash with other VM": {{HostPort: 5432, GuestPort: 5432, Protocol: "tcp"}},
		"duplicate":           {{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}, {HostPort: 8080, GuestPort: 80, Protocol: "tcp"}},
		"same host port":      {{HostPort: 8080, GuestPort: 80, Protocol: "tcp"}, {HostPort: 8080, GuestPort: 81, Protocol: "tcp"}},
		"bad protocol":        {{HostPort: 8080, GuestPort: 80, Protocol: "tcp; reboot"}},
		"missing protocol":    {{HostPort: 8080, GuestPort: 80}},
		"bad port":            {{HostPort: 0, GuestPort: 80, Protocol: "tcp"}},
	} {
		if err := CheckNew(all, "web", pfs); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if len(other.PortForwards) != 1 {
		t.Error("CheckNew modified an existing VM")
	}
}
