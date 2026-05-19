# CLAUDE.md - Project Context for AI Assistants

## Project Overview

VMM (Bare Metal MicroVM Manager) is a Go-based CLI tool for managing Firecracker microVMs on Ubuntu 24.04. It's designed for development environments supporting 10-50 concurrent VMs. It includes an optional web UI (`vmm-web`) for browser-based management.

**Status**: Core functionality complete and tested. See PLAN.md for implementation status.

## Tech Stack

- **Language**: Go 1.21+
- **VMM Engine**: Firecracker v1.11.0 (via firecracker-go-sdk)
- **CLI Framework**: Cobra (github.com/spf13/cobra)
- **Web UI**: Chi router, html/template, HTMX, Tailwind CSS via CDN
- **Networking**: Linux TAP devices, bridges, iptables
- **Storage**: JSON-based VM/cluster configs, ext4 rootfs images
- **CI/CD**: GitHub Actions (GoReleaser for binary releases, kernel/rootfs build workflows)

## Key Architecture

- `cmd/vmm/main.go` — CLI entry point, all command definitions
- `cmd/vmm-web/main.go` — Web UI entry point (separate binary)
- `internal/config/` — Global config, paths, defaults. Config file: `~/.config/vmm/config.json`
- `internal/vm/` — VM struct, state machine, JSON persistence in `/var/lib/vmm/vms/`
- `internal/firecracker/` — Firecracker SDK wrapper, socket API
- `internal/network/` — TAP, bridge, iptables, NAT (MASQUERADE with `! -o vmm-br0`)
- `internal/image/` — Kernel/rootfs download, Docker import, snapshots
- `internal/mount/` — Host directory mount as ext4 block devices
- `internal/sshkey/` — VMM-managed Ed25519 SSH key pair (auto-generated in `/var/lib/vmm/ssh/`)
- `internal/cluster/` — Kubernetes cluster management (kubeadm + Cilium)
- `internal/web/` — Web UI handlers, auth, SSE, WebSocket terminal
- `web/` — Embedded templates and static assets (`go:embed`)
- `scripts/vmm.service` — Systemd unit for VM auto-start on boot
- `scripts/vmm-web.service` — Systemd unit for web UI (reads password from `/etc/vmm-web/environment`)

Data directory: `/var/lib/vmm` (vms, images, kernels, logs, sockets, mounts, clusters, ssh)

## CLI Commands

```
vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--dns SERVER] [--image NAME] [--kernel NAME] [--mount PATH:TAG[:ro|rw]]
vmm start <name>
vmm stop <name>
vmm delete <name> [-f]
vmm list [-a]
vmm ssh <name> [-u user]
vmm port-forward add|list|remove <name> <host>:<guest>
vmm mount list|sync <name> [tag]
vmm image list|pull|import|snapshot|delete
vmm kernel list|import|delete|build
vmm cluster create <name> [--workers N] [--cpus N] [--memory MB] [--disk MB] [--k8s-version VER] [--ssh-key PATH] [--image NAME] [--kernel NAME]
vmm cluster delete|list|kubeconfig <name>
vmm config show|init
vmm version [--json]
```

Create flag defaults can be set in `~/.config/vmm/config.json` under `vm_defaults`. Resolution order: CLI flag > config default > hardcoded fallback.

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
- Key functions: `EnsureBridge()`, `CreateTap()`, `AllocateIP()`, `AddPortForward()`

### Adding new image/kernel variants
- Prefix-based naming drives descriptions in `describeKernel()`/`describeRootfs()` in `internal/image/image.go`
- Prefixes: `k8s-`, `security-`, `debug-`, `minimal-`, or custom

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

## Web UI Constraints

- **CSP**: `script-src 'self' https://cdn.jsdelivr.net` — no inline `<script>` tags or inline event handlers (`onclick`, etc.). All JS must go in `web/static/` files using `addEventListener`/event delegation. Use `data-` attributes to pass server data to JS.
- Auth via `VMM_WEB_PASSWORD` env var, session cookies, or Bearer token for API.
- Templates use `{{template "layout.html" .}}` with `{{define "content"}}` blocks.
- **Web Terminal**: `handlers_terminal.go` provides WebSocket-to-SSH bridge for in-browser terminal access. Uses xterm.js from CDN, `nhooyr.io/websocket`, and `golang.org/x/crypto/ssh`. Standalone template (no layout) at `vm_terminal.html`.

## GitHub Release Tags

- `v*` — binary releases (GoReleaser)
- `kernel-*` — default kernel (6.1 series)
- `k8s-kernel-*` — Kubernetes kernel (6.6 series)
- `security-kernel-*` — security testing kernel (6.12 LTS, broad module coverage)
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

Both `vmm ssh` and config loading detect `SUDO_USER` to use the original user's home directory (SSH keys, config file) rather than `/root/`. Cluster provisioning supports ssh-agent via `sudo -E` to preserve `SSH_AUTH_SOCK`.

## SSH Key Management

VMM automatically generates and manages an Ed25519 key pair (`/var/lib/vmm/ssh/vmm_ed25519`). This managed key is always injected into VMs via `sshkey.BuildAuthorizedKeys()`, so `--ssh-key` is optional for both VM and cluster creation. User-provided keys are added alongside the managed key. The managed key is also used for cluster provisioning (kubeadm over SSH) when no user key is specified.
