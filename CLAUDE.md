# CLAUDE.md - Project Context for AI Assistants

## Project Overview

VMM (Bare Metal MicroVM Manager) is a Go-based CLI tool for managing Firecracker microVMs on Ubuntu 24.04 (also tested on Fedora 44). It's designed for development environments supporting 10-50 concurrent VMs. It includes an optional web UI (`vmm-web`) for browser-based management.

**Status**: Core functionality complete and tested. See PLAN.md for implementation status.

## Tech Stack

- **Language**: Go 1.27+
- **VMM Engine**: Firecracker v1.16.0 (via firecracker-go-sdk)
- **CLI Framework**: Cobra (github.com/spf13/cobra)
- **Web UI**: Chi router, html/template, HTMX, hand-written CSS in the Katagami "Caret Ledger" design language (`web/static/style.css`, self-hosted Sora + Azeret Mono fonts)
- **Networking**: Linux TAP devices, bridges, iptables
- **Storage**: JSON-based VM/cluster configs, ext4 rootfs images
- **CI/CD**: GitHub Actions (CI checks on push/PR, GoReleaser for binary releases, kernel/rootfs build workflows, Dependabot for dependency updates)

## Input Validation

- `internal/validate/` — Centralized name validation for VM, cluster, image, kernel, and mount tag names
- Pattern: `^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$` — rejects path traversal, empty, `.`, `..`, control chars
- Applied at all CLI and web/API entry points before names reach filesystem operations

## Code Review Status

A code review was performed (see `codex_code_review/review.md`) with an implementation plan at `codex_code_review/implementation_plan.md`.

Completed fixes (P1, P2, and P3 done):
- gofmt applied to all Go sources
- CSP inline onclick violation fixed (api_key.html → app.js event listener)
- Identifier validation added at all 43 CLI and web/API entry points (path traversal prevention)
- File permissions tightened: directories 0700, state files 0600
- HTTP server timeouts (ReadHeaderTimeout, ReadTimeout, IdleTimeout) and graceful SIGINT/SIGTERM shutdown
- Rate limiter fixed to key on IP without port (net.SplitHostPort)
- CSPRNG errors checked and fail-closed for session tokens and API keys
- WebSocket origin verification enabled (removed InsecureSkipVerify)
- Unit test suite added across 5 packages (validate, network, vm, web, image)
- CSRF token separated from session token, Bearer auth validated before CSRF skip
- Weak/default password rejection at vmm-web startup (min 8 chars, blocklist)
- Resource bounds validation: CPUs (1-32), memory (128-65536 MB), disk (256-1048576 MB)
- DNS server validation via net.ParseIP at CLI and web entry points
- Kubernetes version semver validation at CLI and web entry points
- Server-side URL resolution for image/kernel downloads (no client-supplied URLs)
- Atomic state writes for VM and cluster configs (temp file + fsync + rename)
- SHA256 checksum verification for downloaded kernels and rootfs (best-effort, fail on mismatch)
- Lifecycle cleanup/rollback on VM start failure (TAP, IP, socket cleaned up in reverse order)
- PID verification before SIGKILL (check /proc/<pid>/cmdline for "firecracker")
- Port-forward idempotency (iptables -C check before append, port range validation 1-65535)
- Subnet prefix derived from config CIDR (no more hardcoded /16 and 255.255.0.0)
- Install script uses mktemp -d with trap cleanup instead of fixed /tmp paths
- Uninstall script adds vmm-web.service cleanup and fixes NAT rule removal
- Documentation aligned: Go version, SSH key behavior, listen address
- Firecracker process termination: `Client.Terminate()` escalates Ctrl+Alt+Del → SIGTERM → SIGKILL and waits for the process to actually exit before a VM is marked stopped or deleted (guest kernels without CONFIG_SERIO_I8042 never see Ctrl+Alt+Del, so the old fire-and-forget stop orphaned every VM). VM liveness is determined by matching `/proc/<pid>/cmdline` against the VM's API socket rather than by the socket file's existence

Future work (P4 — significant effort, deferred):
- **Shared service layer**: Extract VM lifecycle into `internal/service` so CLI and web share one implementation
- **Firecracker jailer**: Run Firecracker under the jailer with unprivileged UID, chroot, and cgroup limits

## Key Architecture

- `cmd/vmm/main.go` — CLI entry point, all command definitions
- `cmd/vmm-web/main.go` — Web UI entry point (separate binary)
- `internal/config/` — Global config, paths, defaults. Config file: `/etc/vmm/config.json` (`SystemConfigPath`), used by everything running as root (sudo, `vmm.service`, `vmm-web.service`); non-root commands read it too and only fall back to `~/.config/vmm/config.json` when it doesn't exist. Services have no `SUDO_USER`, so before this they read a nonexistent `/root/.config` file and booted VMs with the default subnet. `MigrateSystemConfig` (called at startup by both binaries) copies an old per-user config there on the first root run
- `internal/vm/` — VM struct, state machine, JSON persistence in `/var/lib/vmm/vms/`
- `internal/hypervisor/` — VM backend selection. `Backend` (Start/Terminate/UpdateVMState/Version) and optional `Snapshotter`; `For(v)` picks the backend from `vm.VM.Backend` (empty = Firecracker, so older VM JSON is unchanged). Callers use `hypervisor.UpdateVMState`/`Terminate`/`SnapshotterFor`, never the Firecracker client directly (only `upgrade` does, to install the binary). A backend name this build doesn't know gets a stand-in that refuses start/stop and leaves recorded state alone rather than orphaning the process. Launch types (`vm.LaunchConfig`, `vm.MountDrive`, `vm.ConsoleLogPath`) live in `internal/vm` so backends don't import `hypervisor`
- `internal/firecracker/` — Firecracker backend: SDK wrapper, socket API
- `internal/cloudhypervisor/` — Cloud Hypervisor backend, for nested VMs (`vmm create --nested`, which implies `--backend cloud-hypervisor`). Runs the static `/usr/local/bin/cloud-hypervisor` binary (falls back to PATH) in its own session, waits for `vm.info` to report `Running`, and stops it via the ACPI power button then signals. No snapshots yet. Same kernels and rootfs as Firecracker (PVH boot, virtio-pci; drives are vda, vdb... in attach order; `image_type=raw` is required on every `--disk`). Nesting needs `--cpus ...,nested=on`, nested KVM on the host (`HostNested`) and a guest kernel with KVM: the `nested` build profile, installed as `nested-kernel`, which nested VMs use unless `--kernel` is given (`lifecycle.ChooseBackend`, shared by CLI, web form and API). The rootfs has no udev, so systemd-logind ignores the power button: `image.InjectPowerButtonService` adds a guest `vmm-powerbutton.service` that powers off on it (needs the nested profile's evdev + ACPI button drivers; otherwise stop falls back to SIGTERM after 30s)
- `internal/vmproc/` — Shared hypervisor process handling: matches a VM's process by binary name (argv[0], since `comm` truncates `cloud-hypervisor`) plus API socket path, refreshes VM state, and the graceful → SIGTERM → SIGKILL `Terminate` both backends use. `gracefulWait` is 30s: a signal loses the guest's unflushed writes, and Kubernetes nodes take 5-7s to shut down. Clean guests return as soon as they exit; `vmm autostop` terminates VMs in parallel so a host shutdown stays inside `vmm.service`'s `TimeoutStopSec=180`
- `internal/lifecycle/` — `Start` is the one way a VM boots (CLI `start`, autostart, cluster provisioning, web): rootfs/SSH key/DNS/mount prep, TAP and IP, launch, port forwards, and rollback on failure (an IP the VM already had is kept). `Options.BestEffort` is autostart's mode, where guest-prep failures warn instead of failing. `image.PrepareRootfs` removes `/.dockerenv` (left by `docker export`, it makes systemd think the VM is a container: no securityfs, no AppArmor) and boots images without `apparmor_parser` with `apparmor=0` (`LaunchConfig.ExtraKernelArgs`), because containerd and Docker fail to create containers on an AppArmor kernel without it. Don't add start logic to the callers
- `internal/network/` — TAP, bridge, iptables, NAT (MASQUERADE with `! -o vmm-br0`)
- `internal/image/` — Kernel/rootfs download, Docker import, snapshots
- `internal/mount/` — Host directory mount as ext4 block devices. `Manager.PrepareVM` is the one place mounts are set up for a start (CLI, web, autostart): builds the images, writes guest fstab and returns drives numbered by attached position. The rootfs has no udev, so systemd skips fstab mounts of `/dev/vdX`; `image.InjectMountFstab` also enables a guest `vmm-mounts.service` that runs `mount -a`
- `internal/snapshot/` — Full VM snapshots (guest memory + device state via Firecracker's snapshot API, plus a copy of the rootfs and mount images taken while the VM is paused). Restore is in-place: it rolls a VM back to one of its own snapshots, reusing the frozen IP/MAC/TAP identity. `snapshot.json` records the backend (`backend`, empty = Firecracker) and its version (`fc_version` key, kept for older snapshots); `Metadata.Compatible` refuses restores under a different backend and is checked before a running VM is stopped. Stored under `/var/lib/vmm/snapshots/<vm>/<name>/` (`memory`, `vmstate`, `rootfs.ext4`, `snapshot.json`). Snapshot primitives (`PauseVM`, `CreateSnapshotFiles`, `ResumeVM`, `RestoreVM`) are the `hypervisor.Snapshotter` interface, implemented in `internal/firecracker/client.go`.
- `internal/upgrade/` — Release lookup, checksum-verified download/extract, atomic binary swap (`.prev` backups) and Firecracker install, used by `vmm upgrade` and the web UI's update notice (`internal/web/updates.go`), which also counts kernel/rootfs updates: `image.Updates` flags a local artifact whose matching release asset (`updated_at`) is newer than the file's mtime, plus newer k8s patches. `ListAvailableReleasesCached` caches the GitHub listing in `<kernels>/.github-releases.json` (the 60/hr API limit is shared with `vmm upgrade`). `security-kernel`/`kasan-kernel` are pinned to the 6.12 series (`defaultSeries`); other series are `<name>-<series>`
- `internal/sshkey/` — VMM-managed Ed25519 SSH key pair (auto-generated in `/var/lib/vmm/ssh/`)
- `internal/cluster/` — Cluster management. `--type kubeadm` (default): multi-node Kubernetes via kubeadm with CNI selection (`--cni cilium` default, or `--cni calico` for Calico via Tigera operator). `--type openshift`: single-node OpenShift-derived cluster via upstream MicroShift (`internal/cluster/openshift.go`), installed on the base Ubuntu rootfs over SSH using OKD payload images (no Red Hat subscription). Admin workstation support for both. `--nested` (kubeadm only) runs the nodes nested under Cloud Hypervisor for Kata Containers/KubeVirt: `Cluster.Nested` + `Cluster.ConfigureNodeVM` set each node VM's backend (the admin workstation stays on Firecracker), `lifecycle.CheckNestedCluster` validates up front (needs a k8s kernel, never falls back to nested-kernel), and `checkNodesHaveKVM` fails provisioning before kubeadm if a node has no `/dev/kvm` (a k8s-kernel from before KVM was added to the k8s profile). Node prep installs `vmm-k8s-mounts.service` (rshared `/` + bpffs at every boot, before containerd/kubelet); without it Cilium broke on any node restart ("/sys/fs/bpf ... not a shared mount")
- `internal/guestfile/` — SFTP access to a running VM's filesystem (list, download, atomic upload via temp file + rename), used by the web UI file browser
- `internal/web/` — Web UI handlers, auth, SSE, WebSocket terminal, file browser (`handlers_files.go`)
- `web/` — Embedded templates and static assets (`go:embed`)
- `scripts/vmm.service` — Systemd unit for VM auto-start on boot
- `scripts/vmm-web.service` — Systemd unit for web UI (reads password from `/etc/vmm-web/environment`)

Data directory: `/var/lib/vmm` (vms, images, kernels, logs, sockets, mounts, clusters, ssh, snapshots). Configurable via the `data_dir` field in `config.json`, set with `vmm config init --data-dir PATH` or `vmm config set data_dir PATH`. All subpaths derive from it via `Config.GetPaths()`; changing it repoints future operations only and does not move existing data.

## CLI Commands

```
vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--dns SERVER] [--image NAME] [--kernel NAME] [--mount PATH:TAG[:ro|rw]] [--no-autostart] [--backend firecracker|cloud-hypervisor] [--nested] [--restrict-local-networks]
vmm start <name>
vmm stop <name>
vmm delete <name> [-f]
vmm list [-a]
vmm set <name> autostart|restrict-local-networks on|off
vmm ssh <name> [-u user]
vmm cp [-r] [-u user] <src> <dest>     # one side is <vm>:<path>
vmm console <name> [--full] [-f] [-n LINES]
vmm port-forward add|remove <name> <host>:<guest>[/udp]|list [name]|apply <name>
vmm mount list|sync <name> [tag]
vmm snapshot create <vm> <name> [--stop]|list [vm]|restore <vm> <name> [-f] [--no-start]|show <vm> <name>|delete <vm> <name>
vmm image list [--remote]|pull [name]|import|snapshot|delete
vmm kernel list [--remote]|pull <name>|import|delete|build [--profile default|k8s|nested|security|security-kasan]
vmm cluster create <name> [--type kubeadm|openshift] [--cni cilium|calico] [--workers N] [--cpus N] [--memory MB] [--disk MB] [--k8s-version VER] [--openshift-version VER] [--ssh-key PATH] [--image NAME] [--kernel NAME] [--admin-workstation] [--nested]
vmm cluster delete|list|kubeconfig <name>
vmm cluster start <name> [--no-wait] [--timeout DUR]|stop <name>
vmm config show|init [--data-dir PATH]|set <key> <value>   # set supports: data_dir
vmm upgrade [--check] [--version X] [-y] [--force] [--rollback] [--update-units] [--with-cloud-hypervisor]
vmm version [--json]
```

Create flag defaults can be set in `/etc/vmm/config.json` under `vm_defaults`. Resolution order: CLI flag > config default > hardcoded fallback.

## Common Development Tasks

### Adding a new CLI command
1. Add command function in `cmd/vmm/main.go`
2. Register in `rootCmd.AddCommand()` in `main()`
3. Follow existing patterns (load config, get paths, load VM, etc.)

### Adding VM configuration options
1. Add field to `VM` struct in `internal/vm/vm.go`
2. Update `NewVM()` with default value
3. Add CLI flag in relevant command

### Modifying network behavior
- Edit `internal/network/network.go`
- On hosts running firewalld (Fedora/RHEL), `EnsureBridge()` puts the bridge in the `trusted` zone (runtime + permanent). firewalld's nftables table rejects inter-zone forwarding independently of our iptables rules, so without this VMs lose egress whenever the host NIC isn't in the default zone
- On RHEL 10 and its rebuilds (Rocky, Alma) every xtables extension vmm uses (MASQUERADE, DNAT, conntrack, addrtype, and `nft_compat` itself) is in `kernel-modules-extra`. Full installs have it (the `kernel` and `iptables-nft` packages require it), cloud/minimal images may lack it and iptables. `install.sh` installs `iptables-nft` + `kernel-modules-extra-$(uname -r)` on dnf hosts missing either, and `runCmd` appends a hint naming the package when iptables says "missing kernel module?" (`missingExtensionHint`)
- `EnsureBridge()` sets the gateway address on every call and removes any other IPv4 address on the bridge (`ensureGatewayAddr`): the bridge outlives vmm runs and can carry an address from an older config
- Key functions: `EnsureBridge()`, `CreateTap()`, `AllocateIP()`, `AddPortForward()`
- Local network restriction (`--restrict-local-networks`, `internal/network/restrict.go`): nftables tables `bridge vmm_restrict` (per-TAP anti-spoofing MAC/IP binding, IPv4/ARP only, no VM-to-VM) and `ip vmm_restrict` (drop new connections from the VM's IP to private ranges and the host), with VMs as set elements. Kept out of iptables because an nftables drop is final whatever Docker/ufw/firewalld accept. `lifecycle.Start` and snapshot restore call it for every VM (fail closed for restricted ones; for others it clears a stale address, and is a no-op when the tables don't exist, so hosts without restricted VMs never need `nft`). Delete removes the VM's elements. Not offered for clusters
- Port forwards go through `internal/portforward` (CLI and web): `Apply` on start/restore, `Clear` on stop/delete, `Add`/`Remove` for live changes, `List` for status. Don't call `AddPortForward`/`RemovePortForward` directly from lifecycle code. iptables rules are unit-tested against a fake runner (`Manager.run`) in `internal/network/portforward_test.go`

### Adding new image/kernel variants
- Prefix-based naming drives descriptions in `describeKernel()`/`describeRootfs()` in `internal/image/image.go`
- Prefixes: `k8s-`, `security-`, `kasan-`, `debug-`, `minimal-`, or custom

### Kernel build profiles (`scripts/build-kernel.sh --config-profile`)

All profiles start from Firecracker's minimal CI config and layer options on top.

- **`default`** — Minimal Firecracker config with Docker/K8s networking (BPF JIT, cgroups, namespaces, iptables/nftables, overlay FS). Used for standalone VMs (6.1 LTS). Also enables the i8042 controller and AT keyboard (`CONFIG_INPUT_KEYBOARD` + `CONFIG_KEYBOARD_ATKBD`, enforced: the build fails without them) so Firecracker's Ctrl+Alt+Del shutdown reaches the guest; before v0.17.0 ATKBD was silently dropped and every Firecracker stop was a SIGTERM.
- **`k8s`** — Extends `default` with Datadog and eBPF observability support, plus the shared nested-virtualization options (`add_nested_virt_options`: KVM, vhost-net/vsock, evdev + ACPI button, and the u32/mirred tc redirects Kata's default network model needs), so the same kernel serves Firecracker and nested (Cloud Hypervisor) cluster nodes. Adds BTF (`CONFIG_DEBUG_INFO_BTF`, requires `dwarves` package for `pahole`), dynamic ftrace (`CONFIG_FUNCTION_TRACER`, `CONFIG_DYNAMIC_FTRACE`), kprobes/uprobes, perf events, traffic-control BPF, conntrack netlink, and securityfs. Also matches an Ubuntu 24.04 node's security setup: AppArmor as the active LSM (`add_apparmor_lsm_options`: `CONFIG_LSM` set to Ubuntu's order, since SELinux and AppArmor are exclusive and the first listed wins), `STRICT_DEVMEM`, and device-mapper thin provisioning (`add_container_storage_options`) for containerd's devmapper snapshotter, which Kata's Firecracker shim needs. Build fails if any mandatory option is missing after `olddefconfig` or `CONFIG_LSM` isn't Ubuntu's. Used for k8s clusters (6.6 LTS).
- **`security`** — Extends `default` with broad subsystem coverage for vulnerability research (IPsec, io_uring, SCTP, userfaultfd, AppArmor/SELinux, ftrace/kprobes, etc.). AppArmor is the active LSM, as on Ubuntu (before the -r3 kernels a bad `CONFIG_LSM` line left SELinux first). No BTF.
- **`security-kasan`** — Extends `security` with KASAN memory sanitizer. Requires ~2x memory.
- **`nested`** — Extends `default` with the shared nested-virtualization options (`add_nested_virt_options`, checked via `NESTED_VIRT_REQUIRED`) and device-mapper (`add_container_storage_options`). Used for nested VMs; install it as `nested-kernel`.

## CI/CD

### CI Checks (`.github/workflows/ci.yml`)

Runs on push to `main` and all PRs. Three parallel jobs:
- **Test** — `go mod tidy` check, build, `go test -race -shuffle=on` across Go 1.27 + stable
- **Lint** — `go vet` + `golangci-lint`
- **Vulnerability Check** — `govulncheck` for known CVEs in dependency call graph

### Dependency Updates (`.github/dependabot.yml`)

Dependabot opens weekly PRs for Go module and GitHub Actions version updates. Minor/patch Go updates are grouped into a single PR.

### Project Site (`.github/workflows/pages.yml`)

Static GitHub Pages site in `site/` (single `index.html` + `assets/`), deployed on pushes to `main` that touch `site/**`. It shares the Caret Ledger tokens, fonts and component styles with the web UI but has its own `site/assets/site.css`. Web UI screenshots in `site/assets/img/` were taken against a demo data dir (neutral VM names) — keep real VM names off the public site. Pages source must be set to "GitHub Actions" in repo settings. The workflow also copies `scripts/install.sh` to `<site>/install.sh` (the `curl | sudo bash` installer), so installer changes redeploy the site.

### Release Workflows

- `release.yaml` — GoReleaser binary release on `v*` tags. Users upgrade with `sudo vmm upgrade`, which verifies against `checksums.txt`. To require a new Firecracker version, bump `FC_VERSION` in `scripts/install.sh`: both the installer and `vmm upgrade` (which reads it from the release tarball) use it. Cloud Hypervisor is pinned the same way with `CH_VERSION` plus `CH_SHA256_X86_64` (upstream publishes no checksums, so bump both: `sha256sum` the new `cloud-hypervisor-static` asset). It is optional: the installer adds it with `--with-cloud-hypervisor` and `vmm upgrade` with `--with-cloud-hypervisor`; both keep an existing install up to date and refuse a binary that doesn't match the pin. `TestCloudHypervisorFromInstaller` fails if the pin lines in `scripts/install.sh` stop parsing.
- `build-kernel.yml` — kernel compilation (default, k8s, nested, security, kasan, cifs-vuln variants). The k8s job requires `dwarves` for BTF generation. Jobs skip a kernel version that already has a release, so after a build profile change run it manually with `gh workflow run build-kernel.yml -f only=<variant> -f revision=N`, which publishes `<tag>-rN`; `vmm kernel pull` takes the newest release, so the rebuild replaces the original.
- `build-rootfs.yml` / `build-k8s-rootfs.yml` / `build-security-rootfs.yml` — rootfs image builds. k8s rootfs tags are `k8s-rootfs-<version>` (no `-rN`: the version is parsed from the tag), so after a `build-k8s-rootfs.sh` change run `gh workflow run build-k8s-rootfs.yml -f replace=true`, which rebuilds the existing versions and replaces their images in place. Hosts that already pulled an image keep their copy

## Building

```bash
make build          # With version info via ldflags
go build -o vmm ./cmd/vmm/   # Quick build without version info
```

## Testing

```bash
sudo ./vmm image pull
sudo ./vmm create test1 --cpus 1 --memory 512
sudo ./vmm start test1
./vmm list
ping 172.16.0.2
sudo ./vmm ssh test1
sudo ./vmm stop test1
sudo ./vmm delete test1
```

Requirements: root access, KVM (`/dev/kvm`), Firecracker in PATH.

### Other distributions (`scripts/distro-test.sh`)

Runs vmm on stock distros, each in a throwaway QEMU/KVM VM from the distro's official cloud image (qcow2 overlay on a checksum-verified base, so every `up` is a clean install). Run it on the test server (nested KVM): `scripts/distro-test.sh run fedora-44` boots the VM, installs the given release with `scripts/install.sh`, runs the smoke test (image pull, boot, DNS/egress, port forward from inside and outside, `--restrict-local-networks`, stop/delete cleanup) and discards it. `--binaries DIR` swaps in an unreleased `vmm`/`vmm-web` build; `--keep`, `up`/`ssh`/`down` for poking around; `--no-prep` boots the bare cloud image. Profiles: ubuntu-24.04, ubuntu-26.04, debian-13, fedora-44, rocky-10, alma-10; each profile's `PREP` adds what a normal install has that the cloud image lacks (firewalld on Fedora and the EL rebuilds). QEMU user-mode networking, so nothing is added to the host's bridges or firewall. State lives in `/var/tmp/vmm-distro-test`.

## Web UI Constraints

- **Design system**: "Caret Ledger" from katagami.ai — graphite panels with clipped corners, semantic colour only (amber `--color-accent-command` = commands/actions, cyan `--color-accent-data` = data values, violet `--color-accent` = selection). All styling lives in `web/static/style.css` (no Tailwind); reuse its classes (`panel`, `ledger`, `chip chip-<state>`, `btn`, `input`, `kv`, `cmdline`) rather than adding one-off styles. Fonts are self-hosted in `web/static/fonts/` because the CSP has no `font-src`.
- **Activity stream**: `internal/web/events.go` — `SSEBroker` keeps the last 50 VM/cluster events in memory (reset on restart). Handlers call `Record` (state changes, de-duplicated against last-known state) or `Note` (one-off actions: snapshot, restore, failed) with `requestSource(r)` (web/api), and wrap long actions in `Hold` so the 5s poller doesn't log the outcome as `detected`. The poller logs everything else (CLI, crashes). New state-changing handlers must record their events. `app.js` listens for `activity` events on `/events`, updates `[data-vm]`/`[data-cluster]` `[data-state]` chips, and prepends to the dashboard log, which the server pre-renders from `Recent()`.
- **CSP**: `script-src 'self' https://cdn.jsdelivr.net` — no inline `<script>` tags or inline event handlers (`onclick`, etc.). All JS must go in `web/static/` files using `addEventListener`/event delegation. Use `data-` attributes to pass server data to JS.
- Auth via `VMM_WEB_PASSWORD` env var, session cookies, or Bearer token for API.
- Templates use `{{template "layout.html" .}}` with `{{define "content"}}` blocks.
- **File browser**: `handlers_files.go` + `vm_files.html` (HTMX fragment swapped into `#vm-files` on the VM detail page). Uploads are the raw request body (not multipart) sent by XHR with the CSRF token in `X-CSRF-Token`; `csrfMiddleware` checks that header before `FormValue` so it never buffers an upload. Transfer handlers call `clearReadDeadline` to lift the server's 30s `ReadTimeout`. Build URLs in Go (`filesURL`) because html/template doesn't URL-escape `hx-get` attributes.
- **Web Terminal**: `handlers_terminal.go` provides WebSocket-to-SSH bridge for in-browser terminal access. Uses xterm.js from CDN, `nhooyr.io/websocket`, and `golang.org/x/crypto/ssh`. Standalone template (no layout) at `vm_terminal.html`.

## GitHub Release Tags

- `v*` — binary releases (GoReleaser)
- `kernel-*` — default kernel (6.1 series)
- `k8s-kernel-*` — Kubernetes kernel (6.6 series, BTF/BPF/tracing for Datadog eBPF agents)
- `security-kernel-*` — security testing kernels (5.10, 5.15, 6.1, 6.6, 6.12, 6.18 LTS series, broad module coverage)
- `kasan-kernel-*` — KASAN security kernels (same LTS series, memory sanitizer enabled)
- `rootfs-*` — default rootfs (format: `rootfs-24.04-YYYYMMDD`)
- `k8s-rootfs-*` — Kubernetes rootfs (format: `k8s-rootfs-<k8s-version>`)

## Code Style

- Follow standard Go conventions
- Error wrapping: `fmt.Errorf("context: %w", err)`
- User-friendly error messages
- Hidden commands (autostart/autostop) for systemd only

## Debugging

```bash
cat /var/lib/vmm/vms/<name>.json          # VM state
cat /var/lib/vmm/logs/<name>.log          # Firecracker logs
sudo vmm console <name> --full            # Serial console output (kernel boot, panics)
sudo vmm console <name>                   # Tail + follow console output live
ip link show vmm-br0                       # Bridge
sudo iptables -t nat -L -n -v             # NAT rules
ps aux | grep firecracker                  # Processes
vmm ssh myvm -- 'getent hosts google.com'  # DNS from VM
```

## Known Limitations

1. **Linux only** — Firecracker requires Linux with KVM
2. **Root required** — network setup and VM lifecycle need elevated privileges
3. **No GPU passthrough** — Firecracker limitation
4. **No live migration** — VMs must be stopped to move

## Sudo Behavior

`vmm ssh` detects `SUDO_USER` to use the original user's home directory (SSH keys) rather than `/root/`. Cluster provisioning supports ssh-agent via `sudo -E` to preserve `SSH_AUTH_SOCK`.

## SSH Key Management

VMM automatically generates and manages an Ed25519 key pair (`/var/lib/vmm/ssh/vmm_ed25519`). This managed key is always injected into VMs via `sshkey.BuildAuthorizedKeys()`, so `--ssh-key` is optional for both VM and cluster creation. User-provided keys are added alongside the managed key. The managed key is also used for cluster provisioning (kubeadm over SSH) when no user key is specified.

Clusters with an admin workstation also get their own key pair (`/var/lib/vmm/ssh/clusters/<cluster>_ed25519`, `sshkey.GenerateClusterKeyPair`). Its public key is added to the node VMs' `SSHPublicKey`, so it's re-injected on every start. `cluster.InstallAdminKey` copies the private key to the workstation with an `/etc/ssh/ssh_config.d/vmm-cluster.conf` for the nodes. Never give the workstation the managed key: that key logs in to every VM on the host. Cluster delete removes the pair (CLI and both web paths).
