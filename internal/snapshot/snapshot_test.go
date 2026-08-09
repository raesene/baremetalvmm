package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeSnapshot creates a snapshot directory with metadata and dummy data files
// so the file-layer methods can be exercised without a running VM.
func writeSnapshot(t *testing.T, m *Manager, vmName, snapName string, created time.Time) {
	t.Helper()
	dir := m.Dir(vmName, snapName)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Dummy payload files so dirSize has something to measure.
	for _, f := range []string{memFile, stateFile, rootfsFile} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("data-"+f), 0600); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}
	meta := &Metadata{
		Name:       snapName,
		VMName:     vmName,
		VMID:       "id-" + vmName,
		CreatedAt:  created,
		CPUs:       1,
		MemoryMB:   512,
		MemFile:    memFile,
		StateFile:  stateFile,
		RootfsFile: rootfsFile,
	}
	if err := m.saveMetadata(dir, meta); err != nil {
		t.Fatalf("saveMetadata: %v", err)
	}
}

func TestExistsAndGet(t *testing.T) {
	m := NewManager(t.TempDir())

	if m.Exists("vm1", "snap1") {
		t.Fatal("Exists returned true for missing snapshot")
	}
	if _, err := m.Get("vm1", "snap1"); err == nil {
		t.Fatal("Get should error for missing snapshot")
	}

	writeSnapshot(t, m, "vm1", "snap1", time.Now())

	if !m.Exists("vm1", "snap1") {
		t.Fatal("Exists returned false for existing snapshot")
	}
	meta, err := m.Get("vm1", "snap1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if meta.Name != "snap1" || meta.VMName != "vm1" {
		t.Fatalf("unexpected metadata: %+v", meta)
	}
	if meta.SizeBytes != 0 {
		// SizeBytes is only set by Create; writeSnapshot leaves it zero.
		t.Fatalf("expected SizeBytes 0 from test fixture, got %d", meta.SizeBytes)
	}
}

func TestListSortedNewestFirst(t *testing.T) {
	m := NewManager(t.TempDir())

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	writeSnapshot(t, m, "vm1", "old", base)
	writeSnapshot(t, m, "vm1", "new", base.Add(time.Hour))
	writeSnapshot(t, m, "vm2", "other", base.Add(30*time.Minute))

	// Per-VM listing.
	vm1, err := m.List("vm1")
	if err != nil {
		t.Fatalf("List(vm1): %v", err)
	}
	if len(vm1) != 2 {
		t.Fatalf("expected 2 snapshots for vm1, got %d", len(vm1))
	}
	if vm1[0].Name != "new" || vm1[1].Name != "old" {
		t.Fatalf("expected newest-first ordering, got %s then %s", vm1[0].Name, vm1[1].Name)
	}

	// All-VM listing.
	all, err := m.List("")
	if err != nil {
		t.Fatalf("List(all): %v", err)
	}
	if len(all) != 3 {
		t.Fatalf("expected 3 snapshots across all VMs, got %d", len(all))
	}
	if !all[0].CreatedAt.After(all[len(all)-1].CreatedAt) {
		t.Fatalf("all-VM list not sorted newest first")
	}
}

func TestListEmpty(t *testing.T) {
	m := NewManager(filepath.Join(t.TempDir(), "does-not-exist"))
	snaps, err := m.List("")
	if err != nil {
		t.Fatalf("List on missing base dir should not error: %v", err)
	}
	if len(snaps) != 0 {
		t.Fatalf("expected empty list, got %d", len(snaps))
	}
}

func TestDelete(t *testing.T) {
	m := NewManager(t.TempDir())
	writeSnapshot(t, m, "vm1", "snap1", time.Now())
	writeSnapshot(t, m, "vm1", "snap2", time.Now())

	if err := m.Delete("vm1", "snap1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if m.Exists("vm1", "snap1") {
		t.Fatal("snap1 still exists after delete")
	}
	// The VM directory should survive while snap2 remains.
	if _, err := os.Stat(m.VMDir("vm1")); err != nil {
		t.Fatalf("VM dir removed while snapshots remain: %v", err)
	}

	if err := m.Delete("vm1", "missing"); err == nil {
		t.Fatal("Delete should error for missing snapshot")
	}

	// Deleting the last snapshot should remove the now-empty VM directory.
	if err := m.Delete("vm1", "snap2"); err != nil {
		t.Fatalf("Delete snap2: %v", err)
	}
	if _, err := os.Stat(m.VMDir("vm1")); !os.IsNotExist(err) {
		t.Fatalf("empty VM dir not removed: %v", err)
	}
}

func TestDeleteAllForVM(t *testing.T) {
	m := NewManager(t.TempDir())
	writeSnapshot(t, m, "vm1", "snap1", time.Now())
	writeSnapshot(t, m, "vm1", "snap2", time.Now())

	if err := m.DeleteAllForVM("vm1"); err != nil {
		t.Fatalf("DeleteAllForVM: %v", err)
	}
	if _, err := os.Stat(m.VMDir("vm1")); !os.IsNotExist(err) {
		t.Fatalf("VM dir not removed: %v", err)
	}

	// No-op for a VM with no snapshots.
	if err := m.DeleteAllForVM("nonexistent"); err != nil {
		t.Fatalf("DeleteAllForVM on missing VM should be a no-op: %v", err)
	}
}

func TestCopyFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	dst := filepath.Join(dir, "dst")
	content := []byte("hello snapshot")
	if err := os.WriteFile(src, content, 0644); err != nil {
		t.Fatalf("write src: %v", err)
	}

	if err := copyFile(src, dst); err != nil {
		t.Fatalf("copyFile: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read dst: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("content mismatch: got %q want %q", got, content)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("stat dst: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("expected dst perms 0600, got %v", info.Mode().Perm())
	}
}
