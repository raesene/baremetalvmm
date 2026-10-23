package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureSudoLinks(t *testing.T) {
	installDir := t.TempDir()
	linkDir := t.TempDir()
	vmmBin := filepath.Join(installDir, "vmm")
	webBin := filepath.Join(installDir, "vmm-web")
	for _, b := range []string{vmmBin, webBin} {
		if err := os.WriteFile(b, []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	elPath := "/usr/local/sbin:/usr/sbin:/usr/bin"

	if msgs := ensureSudoLinks(installDir+":/usr/bin", linkDir, vmmBin, webBin); len(msgs) != 0 {
		t.Errorf("install dir on sudo's PATH: want no links, got %v", msgs)
	}
	if msgs := ensureSudoLinks("", linkDir, vmmBin, webBin); len(msgs) != 0 {
		t.Errorf("unknown sudo PATH: want no links, got %v", msgs)
	}

	msgs := ensureSudoLinks(elPath, linkDir, vmmBin, webBin, filepath.Join(installDir, "missing"))
	if len(msgs) != 2 {
		t.Fatalf("want 2 links, got %v", msgs)
	}
	for _, b := range []string{vmmBin, webBin} {
		if target, err := os.Readlink(filepath.Join(linkDir, filepath.Base(b))); err != nil || target != b {
			t.Errorf("link for %s: target %q, err %v", b, target, err)
		}
	}

	// Again: the links are already right, so nothing to do.
	if msgs := ensureSudoLinks(elPath, linkDir, vmmBin, webBin); len(msgs) != 0 {
		t.Errorf("second run: want no changes, got %v", msgs)
	}

	// Something else already at the link path is left alone.
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "vmm"), []byte("other"), 0o755); err != nil {
		t.Fatal(err)
	}
	msgs = ensureSudoLinks(elPath, other, vmmBin)
	if len(msgs) != 1 || !strings.Contains(msgs[0], "already exists") {
		t.Errorf("existing file: want an 'already exists' warning, got %v", msgs)
	}
	if b, _ := os.ReadFile(filepath.Join(other, "vmm")); string(b) != "other" {
		t.Error("existing file was replaced")
	}
}
