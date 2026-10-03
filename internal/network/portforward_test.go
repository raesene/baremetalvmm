package network

import (
	"errors"
	"strings"
	"testing"
)

// fakeIptables records rules per table/chain and answers -C/-A/-I/-D like
// iptables does, so rule handling can be tested without root.
type fakeIptables struct {
	rules map[string]bool // "table|rule args" -> present
	calls []string
}

func newFakeManager() (*Manager, *fakeIptables) {
	f := &fakeIptables{rules: map[string]bool{}}
	m := &Manager{
		BridgeName:    "vmm-br0",
		Subnet:        "172.16.0.0/16",
		Gateway:       "172.16.0.1",
		HostInterface: "eth0",
		run:           f.run,
	}
	return m, f
}

func (f *fakeIptables) run(name string, args ...string) error {
	f.calls = append(f.calls, name+" "+strings.Join(args, " "))
	if name != "iptables" {
		return nil
	}
	table := "filter"
	if len(args) >= 2 && args[0] == "-t" {
		table, args = args[1], args[2:]
	}
	op, rule := args[0], strings.Join(args[1:], " ")
	key := table + "|" + rule
	switch op {
	case "-C":
		if !f.rules[key] {
			return errors.New("Bad rule (does a matching rule exist in that chain?)")
		}
	case "-A", "-I":
		f.rules[key] = true
	case "-D":
		if !f.rules[key] {
			return errors.New("Bad rule")
		}
		delete(f.rules, key)
	}
	return nil
}

func (f *fakeIptables) has(table, rule string) bool { return f.rules[table+"|"+rule] }

func (f *fakeIptables) count(prefix string) int {
	n := 0
	for k := range f.rules {
		if strings.HasPrefix(k, prefix) {
			n++
		}
	}
	return n
}

const (
	prerouting = "PREROUTING -p tcp --dport 8080 -m addrtype --dst-type LOCAL -j DNAT --to-destination 172.16.0.5:80"
	output     = "OUTPUT -p tcp --dport 8080 -m addrtype --dst-type LOCAL ! -d 127.0.0.0/8 -j DNAT --to-destination 172.16.0.5:80"
	legacy     = "PREROUTING -p tcp --dport 8080 -j DNAT --to-destination 172.16.0.5:80"
)

func TestAddPortForwardInstallsRules(t *testing.T) {
	m, f := newFakeManager()
	if err := m.AddPortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Fatalf("AddPortForward: %v", err)
	}
	for _, want := range []struct{ table, rule string }{
		{"nat", prerouting},
		{"nat", output},
		{"filter", "FORWARD -o vmm-br0 -m conntrack --ctstate DNAT -j ACCEPT"},
		{"nat", "POSTROUTING -s 172.16.0.0/16 -o vmm-br0 -m conntrack --ctstate DNAT -j MASQUERADE"},
	} {
		if !f.has(want.table, want.rule) {
			t.Errorf("missing %s rule: %s", want.table, want.rule)
		}
	}
	if !m.PortForwardActive(8080, 80, "172.16.0.5", "tcp") {
		t.Error("PortForwardActive = false after add")
	}
}

func TestForwardAcceptRuleIsInserted(t *testing.T) {
	m, f := newFakeManager()
	if err := m.AddPortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, c := range f.calls {
		if strings.HasPrefix(c, "iptables -t filter -I FORWARD -o vmm-br0 -m conntrack --ctstate DNAT") {
			found = true
		}
	}
	if !found {
		t.Error("DNAT accept rule was not inserted at the top of FORWARD")
	}
}

func TestAddPortForwardIsIdempotent(t *testing.T) {
	m, f := newFakeManager()
	for range 3 {
		if err := m.AddPortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
			t.Fatal(err)
		}
	}
	appends := 0
	for _, c := range f.calls {
		if strings.Contains(c, " -A PREROUTING") || strings.Contains(c, " -A OUTPUT") {
			appends++
		}
	}
	if appends != 2 {
		t.Errorf("forward rules appended %d times over 3 adds, want 2", appends)
	}
}

func TestAddPortForwardReplacesLegacyRule(t *testing.T) {
	m, f := newFakeManager()
	f.rules["nat|"+legacy] = true
	if err := m.AddPortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Fatal(err)
	}
	if f.has("nat", legacy) {
		t.Error("legacy any-destination rule was not removed")
	}
	if !f.has("nat", prerouting) {
		t.Error("new PREROUTING rule missing")
	}
	if m.PortForwardLegacy(8080, 80, "172.16.0.5", "tcp") {
		t.Error("PortForwardLegacy = true after migration")
	}
}

func TestPortForwardLegacyDetection(t *testing.T) {
	m, f := newFakeManager()
	f.rules["nat|"+legacy] = true
	if !m.PortForwardLegacy(8080, 80, "172.16.0.5", "tcp") {
		t.Error("legacy rule not detected")
	}
	if m.PortForwardActive(8080, 80, "172.16.0.5", "tcp") {
		t.Error("legacy-only forward reported as active")
	}
}

func TestRemovePortForward(t *testing.T) {
	m, f := newFakeManager()
	if err := m.AddPortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Fatal(err)
	}
	f.rules["nat|"+legacy] = true

	if err := m.RemovePortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Fatalf("RemovePortForward: %v", err)
	}
	if n := f.count("nat|PREROUTING") + f.count("nat|OUTPUT"); n != 0 {
		t.Errorf("%d DNAT rules left after remove", n)
	}
	// The shared accept/hairpin rules stay for other forwards.
	if !f.has("filter", "FORWARD -o vmm-br0 -m conntrack --ctstate DNAT -j ACCEPT") {
		t.Error("shared FORWARD accept rule was removed")
	}
	// Removing again is not an error.
	if err := m.RemovePortForward(8080, 80, "172.16.0.5", "tcp"); err != nil {
		t.Errorf("second RemovePortForward: %v", err)
	}
	if m.PortForwardActive(8080, 80, "172.16.0.5", "tcp") {
		t.Error("PortForwardActive = true after remove")
	}
}

func TestPortForwardRejectsBadInput(t *testing.T) {
	m, f := newFakeManager()
	cases := []struct {
		host, guest int
		ip, proto   string
	}{
		{0, 80, "172.16.0.5", "tcp"},
		{8080, 70000, "172.16.0.5", "tcp"},
		{8080, 80, "172.16.0.5", "icmp"},
		{8080, 80, "172.16.0.5", "tcp -j ACCEPT"},
		{8080, 80, "not-an-ip", "tcp"},
		{8080, 80, "", "tcp"},
		{8080, 80, "::1", "tcp"},
	}
	for _, c := range cases {
		if err := m.AddPortForward(c.host, c.guest, c.ip, c.proto); err == nil {
			t.Errorf("AddPortForward(%d, %d, %q, %q) accepted", c.host, c.guest, c.ip, c.proto)
		}
		if err := m.RemovePortForward(c.host, c.guest, c.ip, c.proto); err == nil {
			t.Errorf("RemovePortForward(%d, %d, %q, %q) accepted", c.host, c.guest, c.ip, c.proto)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("iptables ran for invalid input: %v", f.calls)
	}
}

func TestUDPForwardRules(t *testing.T) {
	m, f := newFakeManager()
	if err := m.AddPortForward(5353, 53, "172.16.0.9", "udp"); err != nil {
		t.Fatal(err)
	}
	if !f.has("nat", "PREROUTING -p udp --dport 5353 -m addrtype --dst-type LOCAL -j DNAT --to-destination 172.16.0.9:53") {
		t.Error("udp PREROUTING rule missing")
	}
}
