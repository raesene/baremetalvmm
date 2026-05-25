package validate

import (
	"testing"
)

func TestName(t *testing.T) {
	tests := []struct {
		kind    string
		value   string
		wantErr bool
	}{
		// Valid names
		{"VM", "test1", false},
		{"VM", "my-vm", false},
		{"VM", "my.vm", false},
		{"VM", "my_vm", false},
		{"VM", "MyVM-2.0", false},
		{"VM", "a", false},
		{"VM", "0start-with-digit", false},
		{"VM", "k8s-1.30", false},
		{"VM", "rootfs-24.04-20260101", false},

		// Max length (64 chars)
		{"VM", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", false},

		// Empty
		{"VM", "", true},

		// Dot and double-dot
		{"VM", ".", true},
		{"VM", "..", true},

		// Path traversal
		{"VM", "../etc", true},
		{"VM", "../../tmp/foo", true},
		{"VM", "foo/bar", true},
		{"VM", "foo\\bar", true},

		// Leading dash
		{"VM", "-badname", true},

		// Leading dot
		{"VM", ".hidden", true},

		// Leading underscore
		{"VM", "_leading", true},

		// Control characters
		{"VM", "foo\nbar", true},
		{"VM", "foo\x00bar", true},
		{"VM", "foo\tbar", true},

		// Spaces
		{"VM", "foo bar", true},
		{"VM", " foo", true},

		// Too long (65 chars)
		{"VM", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", true},

		// Special characters
		{"VM", "foo;bar", true},
		{"VM", "foo|bar", true},
		{"VM", "foo&bar", true},
		{"VM", "foo$bar", true},
		{"VM", "foo`bar", true},
		{"VM", "foo'bar", true},
		{"VM", "foo\"bar", true},
	}

	for _, tt := range tests {
		t.Run(tt.value, func(t *testing.T) {
			err := Name(tt.kind, tt.value)
			if (err != nil) != tt.wantErr {
				t.Errorf("Name(%q, %q) error = %v, wantErr %v", tt.kind, tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestTypedValidators(t *testing.T) {
	valid := "test-vm"
	invalid := "../escape"

	if err := VMName(valid); err != nil {
		t.Errorf("VMName(%q) unexpected error: %v", valid, err)
	}
	if err := VMName(invalid); err == nil {
		t.Errorf("VMName(%q) expected error", invalid)
	}

	if err := ClusterName(valid); err != nil {
		t.Errorf("ClusterName(%q) unexpected error: %v", valid, err)
	}
	if err := ClusterName(invalid); err == nil {
		t.Errorf("ClusterName(%q) expected error", invalid)
	}

	if err := ImageName(valid); err != nil {
		t.Errorf("ImageName(%q) unexpected error: %v", valid, err)
	}
	if err := ImageName(invalid); err == nil {
		t.Errorf("ImageName(%q) expected error", invalid)
	}

	if err := KernelName(valid); err != nil {
		t.Errorf("KernelName(%q) unexpected error: %v", valid, err)
	}
	if err := KernelName(invalid); err == nil {
		t.Errorf("KernelName(%q) expected error", invalid)
	}

	if err := MountTag(valid); err != nil {
		t.Errorf("MountTag(%q) unexpected error: %v", valid, err)
	}
	if err := MountTag(invalid); err == nil {
		t.Errorf("MountTag(%q) expected error", invalid)
	}
}

func TestNameErrorMessages(t *testing.T) {
	err := VMName("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if got := err.Error(); got != "VM name cannot be empty" {
		t.Errorf("unexpected error message: %s", got)
	}

	err = VMName("..")
	if err == nil {
		t.Fatal("expected error for '..'")
	}
	if got := err.Error(); got != "VM name cannot be '.' or '..'" {
		t.Errorf("unexpected error message: %s", got)
	}
}
