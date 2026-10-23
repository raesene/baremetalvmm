package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
)

// sudoLinkDir is where vmm links its binaries when sudo's secure_path leaves
// out the directory they're installed in, as RHEL and its rebuilds (Rocky,
// Alma) do for /usr/local/bin. install.sh does the same for new installs;
// upgrade does it for installs that predate that.
const sudoLinkDir = "/usr/bin"

// sudoPath returns the PATH sudo gives the commands it runs, or "" if it
// can't tell (no sudo, or sudo wants a password).
func sudoPath() string {
	out, err := exec.Command("sudo", "-n", "/usr/bin/env").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if p, ok := strings.CutPrefix(line, "PATH="); ok {
			return p
		}
	}
	return ""
}

// ensureSudoLinks links each binary into linkDir when sudoPATH doesn't
// include the binary's directory, so 'sudo vmm' finds it. It never replaces
// anything already at the link's path, and returns one message per link it
// made or couldn't make.
func ensureSudoLinks(sudoPATH, linkDir string, bins ...string) []string {
	if sudoPATH == "" {
		return nil
	}
	var msgs []string
	for _, bin := range bins {
		dir := filepath.Dir(bin)
		if slices.Contains(filepath.SplitList(sudoPATH), dir) {
			continue
		}
		if _, err := os.Stat(bin); err != nil {
			continue
		}
		link := filepath.Join(linkDir, filepath.Base(bin))
		if target, err := os.Readlink(link); err == nil && target == bin {
			continue
		}
		if _, err := os.Lstat(link); err == nil {
			msgs = append(msgs, fmt.Sprintf("[warn] sudo's PATH leaves out %s and %s already exists: run sudo %s", dir, link, bin))
			continue
		}
		if err := os.Symlink(bin, link); err != nil {
			msgs = append(msgs, fmt.Sprintf("[warn] linking %s: %v", link, err))
			continue
		}
		msgs = append(msgs, fmt.Sprintf("[ok] linked %s -> %s (sudo's PATH leaves out %s)", link, bin, dir))
	}
	return msgs
}
