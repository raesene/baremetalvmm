package mount

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

// writeTree creates files of the given size (in bytes) spread across
// subdirectories, numPerDir per directory, plus some subdirectories.
func writeTree(t *testing.T, dirs, numPerDir, fileSize int) string {
	t.Helper()
	root := t.TempDir()
	for d := range dirs {
		dir := filepath.Join(root, "d"+string(rune('a'+d%26))+string(rune('a'+d/26)))
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		for i := range numPerDir {
			path := filepath.Join(dir, "f"+string(rune('a'+i%26))+string(rune('a'+i/26))+string(rune('a'+i/676)))
			if err := os.WriteFile(path, make([]byte, fileSize), 0644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

// The inode count must cover every entry plus headroom; the size budget must
// cover block-rounded content, inode tables, the journal and ext4 metadata.
func TestCalculateMountBudget(t *testing.T) {
	tests := []struct {
		name      string
		dirs      int
		numPerDir int
		fileSize  int
		minInodes int64
		minSizeMB int
		maxSizeMB int
	}{
		// 1GB content: overhead must stay proportional, not flat 4KB/entry.
		{name: "few large files", dirs: 2, numPerDir: 4, fileSize: 512 * 1024 * 1024,
			minInodes: 1025, maxSizeMB: 4400},
		// ~20MB content in 4KB blocks: inode tables dominate.
		{name: "many small files", dirs: 20, numPerDir: 1000, fileSize: 1024,
			minInodes: 22000, minSizeMB: 100, maxSizeMB: 700},
		{name: "tiny tree", dirs: 1, numPerDir: 3, fileSize: 10,
			minInodes: 1025, minSizeMB: 64, maxSizeMB: 64},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			budget, err := calculateMountBudget(writeTree(t, tt.dirs, tt.numPerDir, tt.fileSize))
			if err != nil {
				t.Fatal(err)
			}
			if budget.Inodes < tt.minInodes {
				t.Errorf("Inodes = %d, want >= %d", budget.Inodes, tt.minInodes)
			}
			if budget.SizeMB < tt.minSizeMB || budget.SizeMB > tt.maxSizeMB {
				t.Errorf("SizeMB = %d, want within [%d, %d]", budget.SizeMB, tt.minSizeMB, tt.maxSizeMB)
			}
		})
	}
}

// The budget is checked against what ext4 actually does: mkfs.ext4 -d
// populates a fresh image from the tree and fails if the budget was wrong
// (out of inodes or blocks). Needs no root.
func TestCalculateMountBudgetMkfs(t *testing.T) {
	if _, err := exec.LookPath("mkfs.ext4"); err != nil {
		t.Skip("mkfs.ext4 not on PATH")
	}

	tests := []struct {
		name      string
		dirs      int
		numPerDir int
		fileSize  int
	}{
		{name: "many small files", dirs: 10, numPerDir: 2000, fileSize: 1024},
		{name: "many tiny dirs", dirs: 1000, numPerDir: 10, fileSize: 100},
		{name: "mixed", dirs: 50, numPerDir: 200, fileSize: 32 * 1024},
		{name: "few large files", dirs: 1, numPerDir: 2, fileSize: 48 * 1024 * 1024},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := writeTree(t, tt.dirs, tt.numPerDir, tt.fileSize)
			budget, err := calculateMountBudget(root)
			if err != nil {
				t.Fatal(err)
			}

			image := filepath.Join(t.TempDir(), "img.ext4")
			f, err := os.Create(image)
			if err != nil {
				t.Fatal(err)
			}
			if err := f.Truncate(int64(budget.SizeMB) * 1024 * 1024); err != nil {
				t.Fatal(err)
			}
			f.Close()

			// Non-empty file data so blocks must really be allocated.
			out, err := exec.Command("mkfs.ext4", "-q", "-F",
				"-N", strconv.FormatInt(budget.Inodes, 10),
				"-d", root, image).CombinedOutput()
			if err != nil {
				t.Fatalf("mkfs.ext4 with budget (size %dMB, %d inodes) failed: %v: %s",
					budget.SizeMB, budget.Inodes, err, out)
			}
		})
	}
}

func TestParseInodeCount(t *testing.T) {
	out := "Filesystem features: extent\nInode count:              221136\nBlock count:              69376\nInode count: should be the first match only\n"
	if got := parseInodeCount(out); got != 221136 {
		t.Errorf("parseInodeCount = %d, want 221136", got)
	}
	if got := parseInodeCount("no match here"); got != 0 {
		t.Errorf("parseInodeCount without match = %d, want 0", got)
	}
}

func TestCalculateMountBudgetMissingPath(t *testing.T) {
	if _, err := calculateMountBudget(filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("expected error for missing path")
	}
}
