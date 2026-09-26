package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/raesene/baremetalvmm/internal/firecracker"
	"github.com/raesene/baremetalvmm/internal/upgrade"
)

const (
	shareDir   = "/usr/local/share/vmm"
	systemdDir = "/etc/systemd/system"
	webUnit    = "vmm-web.service"
)

func upgradeCmd() *cobra.Command {
	var (
		check       bool
		rollback    bool
		yes         bool
		force       bool
		updateUnits bool
		target      string
	)

	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Upgrade vmm, vmm-web and Firecracker to the latest release",
		Long: `Upgrade vmm, vmm-web and Firecracker in place from GitHub releases.

The release tarball is verified against the release's checksums.txt before
anything is installed. Binaries are swapped atomically and the previous
versions are kept as <binary>.prev. Running VMs are never stopped: only the
vmm-web service is restarted (if it is running) so it picks up the new binary.

Examples:
  vmm upgrade --check          # Is a newer release available?
  sudo vmm upgrade             # Upgrade to the latest release
  sudo vmm upgrade --version 0.13.1 --force   # Install a specific version
  sudo vmm upgrade --rollback  # Restore the previous binaries`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), 20*time.Minute)
			defer cancel()

			if check {
				return upgradeCheck(ctx)
			}
			if os.Geteuid() != 0 {
				return fmt.Errorf("upgrade must be run as root (sudo vmm upgrade)")
			}
			paths, err := resolveInstallPaths()
			if err != nil {
				return err
			}
			if rollback {
				return upgradeRollback(paths)
			}
			return runUpgrade(ctx, paths, target, yes, force, updateUnits)
		},
	}

	cmd.Flags().BoolVar(&check, "check", false, "Only report whether a newer release is available")
	cmd.Flags().StringVar(&target, "version", "", "Install this version instead of the latest (e.g. 0.13.1)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Do not ask for confirmation")
	cmd.Flags().BoolVar(&force, "force", false, "Reinstall or downgrade, or replace a development build")
	cmd.Flags().BoolVar(&updateUnits, "update-units", false, "Overwrite installed systemd units that differ from the release's")
	cmd.Flags().BoolVar(&rollback, "rollback", false, "Restore the binaries saved by the last upgrade")
	cmd.MarkFlagsMutuallyExclusive("check", "rollback")
	cmd.MarkFlagsMutuallyExclusive("version", "rollback")

	return cmd
}

type installPaths struct {
	VMM, VMMWeb, Firecracker string
}

func resolveInstallPaths() (installPaths, error) {
	exe, err := os.Executable()
	if err != nil {
		return installPaths{}, fmt.Errorf("locating vmm binary: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	fc := firecracker.DefaultFirecrackerBin
	if _, err := os.Stat(fc); err != nil {
		if p, err := exec.LookPath("firecracker"); err == nil {
			fc = p
		}
	}
	return installPaths{
		VMM:         exe,
		VMMWeb:      filepath.Join(filepath.Dir(exe), "vmm-web"),
		Firecracker: fc,
	}, nil
}

func upgradeCheck(ctx context.Context) error {
	rel, err := upgrade.NewClient().LatestRelease(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("installed: %s\n", version)
	fmt.Printf("latest:    %s (released %s)\n", rel.Version, rel.PublishedAt.Format("2006-01-02"))
	switch _, ok := upgrade.ParseVersion(version); {
	case !ok:
		fmt.Printf("\nThis is a development build. Run 'sudo vmm upgrade --force' to install v%s.\n", rel.Version)
	case upgrade.Compare(rel.Version, version) > 0:
		fmt.Printf("\nAn upgrade is available: sudo vmm upgrade\n%s\n", rel.URL)
	default:
		fmt.Println("\nvmm is up to date.")
	}
	return nil
}

func runUpgrade(ctx context.Context, paths installPaths, target string, yes, force, updateUnits bool) error {
	client := upgrade.NewClient()

	var rel *upgrade.Release
	var err error
	if target != "" {
		rel, err = client.ReleaseByVersion(ctx, target)
	} else {
		rel, err = client.LatestRelease(ctx)
	}
	if err != nil {
		return err
	}

	if _, ok := upgrade.ParseVersion(version); !ok {
		if !force {
			return fmt.Errorf("this is a development build (%s); use --force to replace it with v%s", version, rel.Version)
		}
	} else if c := upgrade.Compare(rel.Version, version); c == 0 && !force {
		fmt.Printf("vmm %s is already installed. Use --force to reinstall.\n", version)
		return nil
	} else if c < 0 && !force {
		return fmt.Errorf("v%s is older than the installed %s; use --force to downgrade", rel.Version, version)
	}

	workDir, err := os.MkdirTemp("", "vmm-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(workDir)

	fmt.Printf("Downloading vmm %s...\n", rel.Version)
	bundle, err := client.Fetch(ctx, rel, upgrade.Arch(), workDir)
	if err != nil {
		return err
	}
	fmt.Println("Checksum verified.")

	fcCurrent := firecracker.NewClient().Version()
	fcWanted := bundle.FirecrackerVersion
	fcUpgrade := fcWanted != "" && fcCurrent != fcWanted
	webActive := unitActive(webUnit)

	fmt.Println("\nUpgrade plan:")
	fmt.Printf("  vmm          %s -> %s  (%s)\n", version, rel.Version, paths.VMM)
	if bundle.VMMWeb != "" {
		note := ""
		if webActive {
			note = ", service will be restarted"
		}
		fmt.Printf("  vmm-web      -> %s  (%s%s)\n", rel.Version, paths.VMMWeb, note)
	}
	switch {
	case fcUpgrade && fcCurrent == "":
		fmt.Printf("  firecracker  install %s  (%s)\n", fcWanted, paths.Firecracker)
	case fcUpgrade:
		fmt.Printf("  firecracker  %s -> %s  (%s)\n", fcCurrent, fcWanted, paths.Firecracker)
	case fcCurrent != "":
		fmt.Printf("  firecracker  %s (unchanged)\n", fcCurrent)
	}
	fmt.Println("  Running VMs are not stopped.")

	if !yes && !confirm("\nProceed?") {
		fmt.Println("Upgrade cancelled.")
		return nil
	}
	fmt.Println()

	// vmm first: if the new binary doesn't run, put the old one back and stop.
	if err := upgrade.ReplaceFile(bundle.VMM, paths.VMM, 0o755, true); err != nil {
		return fmt.Errorf("installing vmm: %w", err)
	}
	if got := binaryVersion(paths.VMM, "version", "--json"); got != rel.Version {
		rbErr := upgrade.Rollback(paths.VMM)
		return fmt.Errorf("new vmm binary reported version %q, expected %s; rolled back (rollback error: %v)", got, rel.Version, rbErr)
	}
	fmt.Printf("  [ok] vmm %s\n", rel.Version)

	if bundle.VMMWeb != "" {
		if err := upgrade.ReplaceFile(bundle.VMMWeb, paths.VMMWeb, 0o755, true); err != nil {
			return fmt.Errorf("installing vmm-web: %w", err)
		}
		if got := binaryVersion(paths.VMMWeb, "--version"); got != rel.Version {
			rbErr := upgrade.Rollback(paths.VMMWeb)
			return fmt.Errorf("new vmm-web binary reported version %q, expected %s; rolled back vmm-web (rollback error: %v). vmm is already upgraded; run 'sudo vmm upgrade --rollback' to revert it", got, rel.Version, rbErr)
		}
		fmt.Printf("  [ok] vmm-web %s\n", rel.Version)
	}

	if bundle.Scripts != "" {
		n, err := installScripts(bundle.Scripts)
		if err != nil {
			fmt.Printf("  [warn] updating scripts in %s: %v\n", shareDir, err)
		} else if n > 0 {
			fmt.Printf("  [ok] %d helper scripts in %s\n", n, shareDir)
		}
		for _, msg := range syncUnits(bundle.Scripts, updateUnits) {
			fmt.Println("  " + msg)
		}
	}

	if fcUpgrade {
		fmt.Printf("  Downloading Firecracker %s...\n", fcWanted)
		if err := client.InstallFirecracker(ctx, fcWanted, upgrade.Arch(), paths.Firecracker, workDir); err != nil {
			fmt.Printf("  [warn] Firecracker not upgraded: %v\n", err)
		} else {
			fmt.Printf("  [ok] firecracker %s (running VMs keep %s until restarted; snapshots taken with the old version may not restore)\n", fcWanted, fcCurrent)
		}
	}

	if webActive {
		if err := exec.Command("systemctl", "restart", webUnit).Run(); err != nil {
			fmt.Printf("  [warn] restarting %s: %v\n", webUnit, err)
		} else {
			fmt.Printf("  [ok] %s restarted\n", webUnit)
		}
	} else if exec.Command("pgrep", "-x", "vmm-web").Run() == nil {
		fmt.Println("  [note] vmm-web is running outside systemd; restart it to use the new version")
	}

	fmt.Printf("\nvmm upgraded to %s. Previous binaries are kept as *%s; undo with 'sudo vmm upgrade --rollback'.\n", rel.Version, upgrade.PrevSuffix)
	return nil
}

func upgradeRollback(paths installPaths) error {
	restored := 0
	for _, p := range []string{paths.VMM, paths.VMMWeb, paths.Firecracker} {
		if _, err := os.Stat(p + upgrade.PrevSuffix); err != nil {
			continue
		}
		if err := upgrade.Rollback(p); err != nil {
			return fmt.Errorf("rolling back %s: %w", p, err)
		}
		fmt.Printf("  [ok] restored %s\n", p)
		restored++
	}
	if restored == 0 {
		return fmt.Errorf("nothing to roll back (no *%s binaries found)", upgrade.PrevSuffix)
	}
	if unitActive(webUnit) {
		if err := exec.Command("systemctl", "restart", webUnit).Run(); err != nil {
			fmt.Printf("  [warn] restarting %s: %v\n", webUnit, err)
		} else {
			fmt.Printf("  [ok] %s restarted\n", webUnit)
		}
	}
	fmt.Printf("\nNow running vmm %s.\n", binaryVersion(paths.VMM, "version", "--json"))
	return nil
}

// installScripts copies the release's helper scripts (kernel/rootfs builders,
// installer, uninstaller) and reference systemd units into shareDir.
func installScripts(src string) (int, error) {
	entries, err := os.ReadDir(src)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		switch {
		case e.IsDir():
			continue
		case strings.HasSuffix(name, ".sh"):
			if err := upgrade.ReplaceFile(filepath.Join(src, name), filepath.Join(shareDir, name), 0o755, false); err != nil {
				return n, err
			}
		case strings.HasSuffix(name, ".service"):
			if err := upgrade.ReplaceFile(filepath.Join(src, name), filepath.Join(shareDir, "systemd", name), 0o644, false); err != nil {
				return n, err
			}
		default:
			continue
		}
		n++
	}
	return n, nil
}

// syncUnits compares installed systemd units with the release's. Units that
// are not installed are left alone. Differing units are only overwritten with
// --update-units, because users commonly edit them (e.g. the listen address);
// customisations belong in a drop-in (systemctl edit) so they survive.
func syncUnits(scriptsDir string, overwrite bool) []string {
	var msgs []string
	changed := false
	for _, unit := range []string{"vmm.service", webUnit} {
		newPath := filepath.Join(scriptsDir, unit)
		installed := filepath.Join(systemdDir, unit)
		newData, err := os.ReadFile(newPath)
		if err != nil {
			continue
		}
		oldData, err := os.ReadFile(installed)
		if err != nil || bytes.Equal(oldData, newData) {
			continue
		}
		if !overwrite {
			msgs = append(msgs, fmt.Sprintf("[note] %s differs from the release's unit; compare with: diff %s %s  (apply with --update-units)",
				unit, installed, filepath.Join(shareDir, "systemd", unit)))
			continue
		}
		if err := upgrade.ReplaceFile(newPath, installed, 0o644, true); err != nil {
			msgs = append(msgs, fmt.Sprintf("[warn] updating %s: %v", unit, err))
			continue
		}
		msgs = append(msgs, fmt.Sprintf("[ok] %s updated (old unit saved as %s%s)", unit, installed, upgrade.PrevSuffix))
		changed = true
	}
	if changed {
		if err := exec.Command("systemctl", "daemon-reload").Run(); err != nil {
			msgs = append(msgs, fmt.Sprintf("[warn] systemctl daemon-reload: %v", err))
		}
	}
	return msgs
}

func unitActive(unit string) bool {
	return exec.Command("systemctl", "is-active", "--quiet", unit).Run() == nil
}

// binaryVersion runs a vmm or vmm-web binary and returns the version it
// reports, or "" if it cannot be run.
func binaryVersion(bin string, args ...string) string {
	out, err := exec.Command(bin, args...).Output()
	if err != nil {
		return ""
	}
	var info struct {
		Version string `json:"version"`
	}
	if json.Unmarshal(out, &info) == nil && info.Version != "" {
		return info.Version
	}
	// vmm-web --version prints "vmm-web version X".
	fields := strings.Fields(strings.SplitN(string(out), "\n", 2)[0])
	if len(fields) > 0 {
		return fields[len(fields)-1]
	}
	return ""
}

func confirm(prompt string) bool {
	fmt.Printf("%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil {
		fmt.Println()
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
