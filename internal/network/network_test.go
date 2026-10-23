package network

import (
	"errors"
	"strings"
	"testing"
)

func TestGenerateTapName(t *testing.T) {
	tests := []struct {
		vmID string
		want string
	}{
		{"abcdef12", "vmm-abcdef"},
		{"123456ab", "vmm-123456"},
		{"aabbcc", "vmm-aabbcc"},
	}

	for _, tt := range tests {
		t.Run(tt.vmID, func(t *testing.T) {
			got := GenerateTapName(tt.vmID)
			if got != tt.want {
				t.Errorf("GenerateTapName(%q) = %q, want %q", tt.vmID, got, tt.want)
			}
		})
	}
}

func TestAllocateIP(t *testing.T) {
	m := &Manager{
		Subnet:  "172.16.0.0/16",
		Gateway: "172.16.0.1",
	}

	t.Run("first allocation returns .2", func(t *testing.T) {
		ip, err := m.AllocateIP(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip != "172.16.0.2" {
			t.Errorf("got %s, want 172.16.0.2", ip)
		}
	})

	t.Run("skips used IPs", func(t *testing.T) {
		ip, err := m.AllocateIP([]string{"172.16.0.2", "172.16.0.3"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip != "172.16.0.4" {
			t.Errorf("got %s, want 172.16.0.4", ip)
		}
	})

	t.Run("skips gateway", func(t *testing.T) {
		ip, err := m.AllocateIP(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip == "172.16.0.1" {
			t.Error("allocated gateway IP")
		}
	})

	t.Run("wraps to next octet", func(t *testing.T) {
		used := make([]string, 0, 254)
		for i := 2; i <= 255; i++ {
			used = append(used, "172.16.0."+itoa(i))
		}
		ip, err := m.AllocateIP(used)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip != "172.16.1.0" {
			t.Errorf("got %s, want 172.16.1.0", ip)
		}
	})

	t.Run("invalid subnet errors", func(t *testing.T) {
		bad := &Manager{Subnet: "not-a-cidr", Gateway: "1.2.3.4"}
		_, err := bad.AllocateIP(nil)
		if err == nil {
			t.Error("expected error for invalid subnet")
		}
	})
}

func TestAllocateIP_Slash24(t *testing.T) {
	m := &Manager{
		Subnet:  "10.0.0.0/24",
		Gateway: "10.0.0.1",
	}

	t.Run("allocates within /24", func(t *testing.T) {
		ip, err := m.AllocateIP(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if ip != "10.0.0.2" {
			t.Errorf("got %s, want 10.0.0.2", ip)
		}
	})

	// NOTE: AllocateIP currently iterates beyond the subnet prefix due to
	// hardcoded /16 iteration (known issue, see codex_code_review/review.md
	// finding "Network configuration claims configurability but hardcodes /16").
	// When the subnet fix is applied, this test should be updated to expect
	// an error when all /24 addresses are exhausted.
}

func itoa(i int) string {
	s := ""
	if i == 0 {
		return "0"
	}
	for i > 0 {
		s = string(rune('0'+i%10)) + s
		i /= 10
	}
	return s
}

func TestEnsureFirewalldZone(t *testing.T) {
	tests := []struct {
		name    string
		running bool
		trusted bool
		want    []string
	}{
		{"firewalld not running", false, false, []string{
			"firewall-cmd --state",
		}},
		{"bridge already trusted", true, true, []string{
			"firewall-cmd --state",
			"firewall-cmd --zone=trusted --query-interface=vmm-br0",
		}},
		{"bridge moved to trusted", true, false, []string{
			"firewall-cmd --state",
			"firewall-cmd --zone=trusted --query-interface=vmm-br0",
			"firewall-cmd --zone=trusted --change-interface=vmm-br0",
			"firewall-cmd --permanent --zone=trusted --change-interface=vmm-br0",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			m := &Manager{BridgeName: "vmm-br0", run: func(name string, args ...string) error {
				call := name + " " + strings.Join(args, " ")
				calls = append(calls, call)
				switch {
				case call == "firewall-cmd --state" && !tt.running:
					return errors.New("not running")
				case strings.Contains(call, "--query-interface") && !tt.trusted:
					return errors.New("no")
				}
				return nil
			}}
			if err := m.ensureFirewalldZone(); err != nil {
				t.Fatalf("ensureFirewalldZone: %v", err)
			}
			if strings.Join(calls, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestEnsureGatewayAddr(t *testing.T) {
	tests := []struct {
		name     string
		existing []string
		want     []string
	}{
		{"only gateway", []string{"10.99.0.1/16"}, []string{
			"ip addr replace 10.99.0.1/16 dev vmm-br0",
		}},
		{"stale default address removed", []string{"172.16.0.1/16", "10.99.0.1/16"}, []string{
			"ip addr replace 10.99.0.1/16 dev vmm-br0",
			"ip addr del 172.16.0.1/16 dev vmm-br0",
		}},
		{"old prefix removed", []string{"10.99.0.1/24", "10.99.0.1/16"}, []string{
			"ip addr replace 10.99.0.1/16 dev vmm-br0",
			"ip addr del 10.99.0.1/24 dev vmm-br0",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			m := &Manager{
				BridgeName: "vmm-br0",
				Subnet:     "10.99.0.0/16",
				Gateway:    "10.99.0.1",
				run: func(name string, args ...string) error {
					calls = append(calls, name+" "+strings.Join(args, " "))
					return nil
				},
				bridgeAddrs: func() ([]string, error) { return tt.existing, nil },
			}
			if err := m.ensureGatewayAddr(); err != nil {
				t.Fatalf("ensureGatewayAddr: %v", err)
			}
			if strings.Join(calls, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestEnsureGatewayAddrReplaceFails(t *testing.T) {
	m := &Manager{
		BridgeName: "vmm-br0",
		Subnet:     "10.99.0.0/16",
		Gateway:    "10.99.0.1",
		run:        func(string, ...string) error { return errors.New("boom") },
		bridgeAddrs: func() ([]string, error) {
			t.Fatal("addresses read after replace failed")
			return nil, nil
		},
	}
	if err := m.ensureGatewayAddr(); err == nil {
		t.Fatal("want error when ip addr replace fails")
	}
}

func TestMissingExtensionHint(t *testing.T) {
	el10 := []byte("Warning: Extension MASQUERADE revision 0 not supported, missing kernel module?\n" +
		"iptables v1.8.11 (nf_tables):  RULE_APPEND failed (No such file or directory): rule in chain POSTROUTING\n")
	if hint := missingExtensionHint("iptables", el10); !strings.Contains(hint, "kernel-modules-extra-") {
		t.Errorf("no kernel-modules-extra hint for the EL10 error, got %q", hint)
	}
	if hint := missingExtensionHint("iptables", []byte("iptables: Bad rule (does a matching rule exist in that chain?).\n")); hint != "" {
		t.Errorf("hint for an unrelated iptables error: %q", hint)
	}
	if hint := missingExtensionHint("nft", el10); hint != "" {
		t.Errorf("hint for a non-iptables command: %q", hint)
	}
}
