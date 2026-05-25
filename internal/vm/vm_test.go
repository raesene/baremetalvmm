package vm

import (
	"testing"
)

func TestNewVM(t *testing.T) {
	v := NewVM("test-vm")

	if v.Name != "test-vm" {
		t.Errorf("Name = %q, want %q", v.Name, "test-vm")
	}
	if v.ID == "" {
		t.Error("ID should not be empty")
	}
	if len(v.ID) != 8 {
		t.Errorf("ID length = %d, want 8", len(v.ID))
	}
	if v.State != StateCreated {
		t.Errorf("State = %q, want %q", v.State, StateCreated)
	}
	if v.CPUs != 1 {
		t.Errorf("CPUs = %d, want 1", v.CPUs)
	}
	if v.MemoryMB != 512 {
		t.Errorf("MemoryMB = %d, want 512", v.MemoryMB)
	}
	if v.DiskSizeMB != 1024 {
		t.Errorf("DiskSizeMB = %d, want 1024", v.DiskSizeMB)
	}
	if !v.AutoStart {
		t.Error("AutoStart should be true by default")
	}
	if v.CreatedAt.IsZero() {
		t.Error("CreatedAt should be set")
	}
}

func TestGenerateMacAddress(t *testing.T) {
	v := NewVM("test")
	mac := v.GenerateMacAddress()

	if len(mac) != 17 {
		t.Errorf("MAC length = %d, want 17", len(mac))
	}
	if mac[:8] != "AA:FC:00" {
		t.Errorf("MAC prefix = %q, want %q", mac[:8], "AA:FC:00")
	}
}

func TestGenerateMacAddress_Deterministic(t *testing.T) {
	v := &VM{ID: "abcdef12"}
	mac1 := v.GenerateMacAddress()
	mac2 := v.GenerateMacAddress()

	if mac1 != mac2 {
		t.Errorf("MAC should be deterministic: %q != %q", mac1, mac2)
	}
}

func TestNewVM_UniqueIDs(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		v := NewVM("test")
		if ids[v.ID] {
			t.Fatalf("duplicate ID: %s", v.ID)
		}
		ids[v.ID] = true
	}
}

func TestSaveLoadDelete(t *testing.T) {
	dir := t.TempDir()
	v := NewVM("persist-test")
	v.CPUs = 4
	v.MemoryMB = 2048

	if err := v.Save(dir); err != nil {
		t.Fatalf("Save error: %v", err)
	}

	if !Exists(dir, "persist-test") {
		t.Fatal("Exists returned false after Save")
	}

	loaded, err := Load(dir, "persist-test")
	if err != nil {
		t.Fatalf("Load error: %v", err)
	}
	if loaded.Name != "persist-test" {
		t.Errorf("loaded Name = %q", loaded.Name)
	}
	if loaded.CPUs != 4 {
		t.Errorf("loaded CPUs = %d, want 4", loaded.CPUs)
	}
	if loaded.MemoryMB != 2048 {
		t.Errorf("loaded MemoryMB = %d, want 2048", loaded.MemoryMB)
	}

	if err := Delete(dir, "persist-test"); err != nil {
		t.Fatalf("Delete error: %v", err)
	}
	if Exists(dir, "persist-test") {
		t.Error("Exists returned true after Delete")
	}
}

func TestLoadNotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := Load(dir, "nonexistent")
	if err == nil {
		t.Error("expected error loading nonexistent VM")
	}
}

func TestList(t *testing.T) {
	dir := t.TempDir()

	vms, err := List(dir)
	if err != nil {
		t.Fatalf("List empty dir error: %v", err)
	}
	if len(vms) != 0 {
		t.Errorf("expected 0 VMs, got %d", len(vms))
	}

	NewVM("vm-a").Save(dir)
	NewVM("vm-b").Save(dir)

	vms, err = List(dir)
	if err != nil {
		t.Fatalf("List error: %v", err)
	}
	if len(vms) != 2 {
		t.Errorf("expected 2 VMs, got %d", len(vms))
	}
}
