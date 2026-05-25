package network

import (
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
