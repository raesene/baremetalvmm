package image

import (
	"path/filepath"
	"testing"
)

func TestGetImagePath(t *testing.T) {
	m := &Manager{RootfsDir: "/var/lib/vmm/rootfs"}

	tests := []struct {
		name string
		want string
	}{
		{"ubuntu-base", "/var/lib/vmm/rootfs/ubuntu-base.ext4"},
		{"k8s-1.30", "/var/lib/vmm/rootfs/k8s-1.30.ext4"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.GetImagePath(tt.name)
			if got != tt.want {
				t.Errorf("GetImagePath(%q) = %q, want %q", tt.name, got, tt.want)
			}
		})
	}
}

func TestGetKernelPath(t *testing.T) {
	m := &Manager{KernelDir: "/var/lib/vmm/kernels"}

	t.Run("empty name returns default", func(t *testing.T) {
		got := m.GetKernelPath("")
		want := filepath.Join("/var/lib/vmm/kernels", DefaultKernelName)
		if got != want {
			t.Errorf("GetKernelPath('') = %q, want %q", got, want)
		}
	})

	t.Run("named kernel", func(t *testing.T) {
		got := m.GetKernelPath("k8s-kernel")
		if got != "/var/lib/vmm/kernels/k8s-kernel" {
			t.Errorf("GetKernelPath('k8s-kernel') = %q", got)
		}
	})
}

func TestGetDefaultPaths(t *testing.T) {
	m := &Manager{
		KernelDir: "/var/lib/vmm/kernels",
		RootfsDir: "/var/lib/vmm/rootfs",
	}

	kernelPath := m.GetDefaultKernelPath()
	if kernelPath != filepath.Join("/var/lib/vmm/kernels", DefaultKernelName) {
		t.Errorf("GetDefaultKernelPath() = %q", kernelPath)
	}

	rootfsPath := m.GetDefaultRootfsPath()
	if rootfsPath != filepath.Join("/var/lib/vmm/rootfs", DefaultRootfsName) {
		t.Errorf("GetDefaultRootfsPath() = %q", rootfsPath)
	}
}

func TestDescribeKernel(t *testing.T) {
	tests := []struct {
		name      string
		isDefault bool
		wantSub   string
	}{
		{"k8s-kernel", false, "Kubernetes"},
		{"cifs-vuln-kernel", false, "CIFS"},
		{"security-kernel", false, "Security"},
		{"debug-kernel", false, "Debug"},
		{"minimal-kernel", false, "Minimal"},
		{"custom-kernel", false, "Custom"},
		{"vmlinux.bin", true, "Default"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeKernel(tt.name, tt.isDefault)
			if got == "" {
				t.Error("description should not be empty")
			}
			// Just verify it returns something meaningful
			if len(got) < 5 {
				t.Errorf("description too short: %q", got)
			}
		})
	}
}

func TestDescribeRootfs(t *testing.T) {
	tests := []struct {
		name      string
		isDefault bool
	}{
		{"k8s-1.30", false},
		{"security-rootfs", false},
		{"minimal-rootfs", false},
		{"custom-image", false},
		{"rootfs.ext4", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := describeRootfs(tt.name, tt.isDefault)
			if got == "" {
				t.Error("description should not be empty")
			}
		})
	}
}
