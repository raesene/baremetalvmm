package network

import (
	"errors"
	"strings"
	"testing"
)

// fakeNft records nft invocations and pretends the restriction tables exist
// once a ruleset has been loaded.
type fakeNft struct {
	tables bool
	calls  []string
}

func (f *fakeNft) run(name string, args ...string) error {
	if name != "nft" {
		return nil
	}
	call := strings.Join(args, " ")
	f.calls = append(f.calls, call)
	switch {
	case strings.HasPrefix(call, "list table"):
		if !f.tables {
			return errors.New("No such file or directory")
		}
	case strings.HasPrefix(call, "add table"):
		f.tables = true
	}
	return nil
}

func newRestrictManager() (*Manager, *fakeNft) {
	f := &fakeNft{}
	return &Manager{BridgeName: "vmm-br0", Subnet: "172.16.0.0/16", run: f.run}, f
}

func TestRestrictLoadsRulesetAndAddsElements(t *testing.T) {
	m, f := newRestrictManager()
	if err := m.SetLocalNetworkRestriction(true, "vmm-abc123", "06:00:AC:10:00:02", "172.16.0.2"); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != 2 {
		t.Fatalf("calls = %q, want ruleset then elements", f.calls)
	}
	if !strings.HasPrefix(f.calls[0], "add table bridge vmm_restrict") {
		t.Errorf("first call should load the ruleset, got %q", f.calls[0])
	}
	want := []string{
		`add element bridge vmm_restrict ports { "vmm-abc123" }`,
		`add element bridge vmm_restrict bindings { "vmm-abc123" . 06:00:ac:10:00:02 . 172.16.0.2 }`,
		`add element ip vmm_restrict vms { 172.16.0.2 }`,
	}
	if got := f.calls[1]; got != strings.Join(want, "\n") {
		t.Errorf("elements =\n%s\nwant\n%s", got, strings.Join(want, "\n"))
	}
}

func TestUnrestrictWithoutTablesDoesNothing(t *testing.T) {
	m, f := newRestrictManager()
	if err := m.SetLocalNetworkRestriction(false, "vmm-abc123", "06:00:ac:10:00:02", "172.16.0.2"); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if !strings.HasPrefix(c, "list table") {
			t.Errorf("unexpected nft call %q on a host without the tables", c)
		}
	}
}

func TestUnrestrictWithoutNftDoesNothing(t *testing.T) {
	m := &Manager{BridgeName: "vmm-br0", Subnet: "172.16.0.0/16", run: func(name string, args ...string) error {
		return errors.New("exec: \"nft\": executable file not found in $PATH")
	}}
	if err := m.SetLocalNetworkRestriction(false, "vmm-abc123", "06:00:ac:10:00:02", "172.16.0.2"); err != nil {
		t.Errorf("unrestricting on a host without nft should be a no-op, got %v", err)
	}
}

func TestUnrestrictRemovesElementsIdempotently(t *testing.T) {
	m, f := newRestrictManager()
	f.tables = true
	if err := m.SetLocalNetworkRestriction(false, "vmm-abc123", "06:00:ac:10:00:02", "172.16.0.2"); err != nil {
		t.Fatal(err)
	}
	last := f.calls[len(f.calls)-1]
	lines := strings.Split(last, "\n")
	if len(lines) != 6 {
		t.Fatalf("want 3 adds then 3 deletes in one transaction, got %q", last)
	}
	for i, l := range lines {
		op := "add"
		if i >= 3 {
			op = "delete"
		}
		if !strings.HasPrefix(l, op+" element ") {
			t.Errorf("line %d = %q, want %s element", i, l, op)
		}
	}
}

func TestRestrictRejectsBadIdentity(t *testing.T) {
	m, f := newRestrictManager()
	for _, tc := range []struct{ tap, mac, ip string }{
		{`vmm-x" }; flush ruleset; {`, "06:00:ac:10:00:02", "172.16.0.2"},
		{"vmm-abc123", "not-a-mac", "172.16.0.2"},
		{"vmm-abc123", "06:00:ac:10:00:02", ""},
		{"vmm-abc123", "06:00:ac:10:00:02", "fd00::2"},
	} {
		if err := m.SetLocalNetworkRestriction(true, tc.tap, tc.mac, tc.ip); err == nil {
			t.Errorf("SetLocalNetworkRestriction(%q, %q, %q) succeeded, want error", tc.tap, tc.mac, tc.ip)
		}
	}
	if len(f.calls) != 0 {
		t.Errorf("nft ran for invalid input: %q", f.calls)
	}
}

func TestRestrictRulesetBlocksSubnetOutsideLocalRanges(t *testing.T) {
	m, _ := newRestrictManager()
	if strings.Contains(m.restrictRuleset(), "172.16.0.0/16") {
		t.Error("default subnet is inside 172.16.0.0/12 and shouldn't be listed twice")
	}
	m.Subnet = "203.0.113.0/24"
	if !strings.Contains(m.restrictRuleset(), "203.0.113.0/24 } drop") {
		t.Error("a VM subnet outside the local ranges should be blocked too")
	}
}

func TestIsLocalAddress(t *testing.T) {
	for ip, want := range map[string]bool{
		"192.168.1.1": true,
		"10.0.0.53":   true,
		"172.20.0.1":  true,
		"100.100.1.1": true,
		"169.254.1.1": true,
		"8.8.8.8":     false,
		"1.1.1.1":     false,
		"172.32.0.1":  false,
		"bogus":       false,
	} {
		if got := IsLocalAddress(ip); got != want {
			t.Errorf("IsLocalAddress(%q) = %v, want %v", ip, got, want)
		}
	}
}
