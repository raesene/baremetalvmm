package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateDataDir(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{"absolute", "/var/lib/vmm", false},
		{"absolute custom", "/srv/vmm-data", false},
		{"empty", "", true},
		{"whitespace", "   ", true},
		{"relative", "vmm", true},
		{"relative dot", "./vmm", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateDataDir(tt.path)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateDataDir(%q) error = %v, wantErr %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestGetPathsDerivedFromDataDir(t *testing.T) {
	dataDir := "/custom/data/root"
	cfg := &Config{DataDir: dataDir}
	paths := cfg.GetPaths()

	want := map[string]string{
		"Config":    filepath.Join(dataDir, "config"),
		"VMs":       filepath.Join(dataDir, "vms"),
		"Images":    filepath.Join(dataDir, "images"),
		"Kernels":   filepath.Join(dataDir, "images", "kernels"),
		"Rootfs":    filepath.Join(dataDir, "images", "rootfs"),
		"Sockets":   filepath.Join(dataDir, "sockets"),
		"Logs":      filepath.Join(dataDir, "logs"),
		"State":     filepath.Join(dataDir, "state"),
		"Mounts":    filepath.Join(dataDir, "mounts"),
		"Clusters":  filepath.Join(dataDir, "clusters"),
		"SSH":       filepath.Join(dataDir, "ssh"),
		"Snapshots": filepath.Join(dataDir, "snapshots"),
	}

	got := map[string]string{
		"Config":    paths.Config,
		"VMs":       paths.VMs,
		"Images":    paths.Images,
		"Kernels":   paths.Kernels,
		"Rootfs":    paths.Rootfs,
		"Sockets":   paths.Sockets,
		"Logs":      paths.Logs,
		"State":     paths.State,
		"Mounts":    paths.Mounts,
		"Clusters":  paths.Clusters,
		"SSH":       paths.SSH,
		"Snapshots": paths.Snapshots,
	}

	for k, wantPath := range want {
		if got[k] != wantPath {
			t.Errorf("GetPaths().%s = %q, want %q", k, got[k], wantPath)
		}
	}
}

func TestEnsureDirectoriesCustomDataDir(t *testing.T) {
	tmp := t.TempDir()
	dataDir := filepath.Join(tmp, "vmm-data")
	cfg := &Config{DataDir: dataDir}

	if err := cfg.EnsureDirectories(); err != nil {
		t.Fatalf("EnsureDirectories() error = %v", err)
	}

	paths := cfg.GetPaths()
	for _, dir := range []string{paths.VMs, paths.Kernels, paths.Rootfs, paths.Snapshots} {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("expected directory %q to exist: %v", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("expected %q to be a directory", dir)
		}
	}
}
