# CLAUDE.md - Project Context for AI Assistants

This file provides context for Claude or other AI assistants working on this codebase.

## Project Overview

VMM (Bare Metal MicroVM Manager) is a Go-based CLI tool for managing Firecracker microVMs on Ubuntu 24.04. It's designed for development environments supporting 10-50 concurrent VMs. It includes an optional web UI (`vmm-web`) for browser-based management.

**Status**: Core functionality complete and tested. See PLAN.md for implementation status.

## Tech Stack

- **Language**: Go 1.21+
- **VMM Engine**: Firecracker v1.11.0 (via firecracker-go-sdk)
- **CLI Framework**: Cobra (github.com/spf13/cobra)
- **Web UI**: Chi router, html/template, HTMX, Tailwind CSS
- **Networking**: Linux TAP devices, bridges, iptables
- **Storage**: JSON-based VM configs, ext4 rootfs images
- **Service**: systemd for auto-start
- **CI/CD**: GitHub Actions (GoReleaser for binary releases, kernel build workflow, rootfs build workflow)

## Directory Structure

```
baremetalvmm/
├── cmd/vmm/main.go           # CLI entry point, all command definitions
├── cmd/vmm-web/main.go       # Web UI entry point
├── internal/
│   ├── config/config.go      # Global config, paths, defaults
│   ├── vm/vm.go              # VM struct, state machine, persistence
│   ├── cluster/              # Kubernetes cluster management
│   │   ├── cluster.go        # Cluster struct, state, JSON persistence
│   │   ├── provisioner.go    # SSH-based kubeadm/Cilium bootstrap
│   │   └── kubeconfig.go     # Kubeconfig extraction and merging
│   ├── firecracker/client.go # Firecracker SDK wrapper
│   ├── network/network.go    # TAP, bridge, iptables management
│   ├── image/image.go        # Kernel/rootfs download and management
│   ├── mount/mount.go        # Host directory mount management
│   └── web/                  # Web UI (separate binary, optional)
│       ├── server.go         # Chi router, middleware, template rendering
│       ├── auth.go           # Session auth, rate limiting, CSRF
│       ├── handlers_vm.go    # VM CRUD handlers (HTML + JSON API)
│       ├── handlers_cluster.go # Cluster handlers (HTML + JSON API)
│       └── handlers_dashboard.go # Dashboard, SSE health stream
├── web/
│   ├── embed.go              # go:embed directive for assets
│   ├── templates/            # HTML templates (layout, login, dashboard, etc.)
│   └── static/               # htmx.min.js, sse.js, style.css
├── .github/workflows/
│   ├── release.yaml          # GoReleaser binary release on v* tags
│   ├── build-kernel.yml      # Automated kernel build + GitHub release
│   ├── build-rootfs.yml      # Automated rootfs build + GitHub release
│   └── build-k8s-rootfs.yml  # Automated k8s rootfs build + GitHub release
├── scripts/
│   ├── install.sh            # Installation script (binary + Firecracker + kernel + rootfs)
│   ├── uninstall.sh          # Complete removal script
│   ├── install-service.sh    # Systemd service installation (optional)
│   ├── build-kernel.sh       # Custom kernel build script
│   ├── build-rootfs.sh       # Custom rootfs build script
│   ├── build-k8s-rootfs.sh   # K8s rootfs build script (containerd + kubeadm pre-installed)
│   └── vmm.service           # Systemd unit file
├── .goreleaser.yaml          # GoReleaser configuration
├── go.mod, go.sum            # Dependencies
├── Makefile                  # Build with version info for local development
├── README.md                 # User documentation
└── PLAN.md                   # Project requirements and status
```

## Key Components

### 1. Configuration (`internal/config/`)
- Default data dir: `/var/lib/vmm`
- Default bridge: `vmm-br0`
- Default subnet: `172.16.0.0/16`
- Gateway: `172.16.0.1`
- Config file: `~/.config/vmm/config.json`
- `host_interface` is auto-detected from the default route (falls back to `eth0` if detection fails)
- Optional `vm_defaults` section for `vmm create` default values (cpus, memory, disk, image, kernel, ssh_key_path, dns_servers)

### 2. VM Management (`internal/vm/`)
- VM states: `created`, `starting`, `running`, `stopping`, `stopped`, `error`
- Config stored as JSON in `/var/lib/vmm/vms/<name>.json`
- Each VM gets a unique 8-char ID from UUID

### 3. Firecracker Client (`internal/firecracker/`)
- Wraps firecracker-go-sdk
- Manages VM lifecycle via Unix socket API
- Handles process spawning and cleanup
- Configures VM networking via kernel `ip=` parameter

### 4. Networking (`internal/network/`)
- Creates vmm-br0 bridge on first VM start
- TAP device per VM (named `vmm-<id>`)
- IP allocation: next-free from 172.16.0.2, scans existing VMs to skip in-use addresses
- NAT via iptables MASQUERADE
- Port forwarding via DNAT rules

### 5. Image Management (`internal/image/`)
- Downloads default kernel from GitHub releases (`kernel-*` tagged releases), falls back to Firecracker S3 URL
- Downloads default rootfs from Firecracker quickstart URLs
- Queries GitHub API (`api.github.com/repos/raesene/baremetalvmm/releases`) for latest kernel
- Creates per-VM rootfs copies for persistence
- Stored in `/var/lib/vmm/images/`

### 6. Mount Management (`internal/mount/`)
- Creates ext4 images from host directories for VM mounts
- Mount images stored in `/var/lib/vmm/mounts/`
- Supports read-only and read-write mounts
- Auto-mounts in guest via fstab injection

### 7. Cluster Management (`internal/cluster/`)
- Creates Kubernetes clusters from multiple Firecracker VMs using kubeadm
- SSH-based provisioning via `golang.org/x/crypto/ssh`
- Cilium CNI with kube-proxy replacement
- Cluster state stored as JSON in `/var/lib/vmm/clusters/<name>.json`
- Kubeconfig merging into `~/.kube/config` using `gopkg.in/yaml.v3`
- Cluster states: `creating`, `running`, `stopped`, `error`
- VM naming convention: `{cluster}-control-plane`, `{cluster}-worker-N`
- Requires kernel 6.6+ with BPF JIT, VXLAN, cgroups v2 bandwidth support

## CLI Commands

```
vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--dns SERVER] [--image NAME] [--kernel NAME] [--mount PATH:TAG[:ro|rw]]
vmm start <name>
vmm stop <name>
vmm delete <name> [-f]
vmm list [-a]
vmm ssh <name> [-u user]
vmm port-forward <name> <host>:<guest>
vmm mount list <name>
vmm mount sync <name> <tag>
vmm image list
vmm image pull
vmm image import <docker-image> --name <name> [--size MB]
vmm image delete <name>
vmm kernel list
vmm kernel import <path> --name <name> [-f]
vmm kernel delete <name>
vmm kernel build --version <version> --name <name>
vmm cluster create <name> [--workers N] [--cpus N] [--memory MB] [--disk MB] [--k8s-version VER] [--ssh-key PATH] [--image NAME] [--kernel NAME]
vmm cluster delete <name> [-f]
vmm cluster list
vmm cluster kubeconfig <name>
vmm config show
vmm config init
vmm version [--json]
vmm autostart   # Hidden, used by systemd
vmm autostop    # Hidden, used by systemd
```

### Create flags
- `--cpus` - Number of vCPUs (default: 1, configurable)
- `--memory` - Memory in MB (default: 512, configurable)
- `--disk` - Disk size in MB (default: 1024, configurable) - rootfs is resized to this size
- `--ssh-key` - Path to SSH public key file for root access (configurable)
- `--dns` - Custom DNS server (can be repeated for multiple servers, configurable)
- `--image` - Name of custom rootfs image (from `vmm image import`, configurable)
- `--kernel` - Name of custom kernel (from `vmm kernel import`, configurable)
- `--mount` - Mount host directory in VM (format: `/host/path:tag[:ro|rw]`, can be repeated)

Note: Flags marked "configurable" can have defaults set in `~/.config/vmm/config.json` under `vm_defaults`. See "Configurable VM Defaults" section below.

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
1. Edit `internal/network/network.go`
2. Key functions: `EnsureBridge()`, `CreateTap()`, `AllocateIP()`, `AddPortForward()`

### Adding new image sources
1. Edit `internal/image/image.go`
2. Modify URL constants or add new download functions

## Testing

### Manual testing flow
```bash
# Build (with version info)
make build

# Or build quickly without version info
go build -o vmm ./cmd/vmm/

# Verify version info
./vmm version

# Test basic commands
./vmm config init
sudo ./vmm image pull
sudo ./vmm create test1 --cpus 1 --memory 512
./vmm list
sudo ./vmm start test1
./vmm list                    # Works as non-root
ping 172.16.0.2               # Verify network
sudo ./vmm stop test1
sudo ./vmm delete test1
```

### Requirements for full testing
- Root access (for TAP devices, bridge, iptables)
- KVM support (`/dev/kvm`)
- Firecracker binary in PATH or `/usr/local/bin`
- Downloaded kernel and rootfs images

## Dependencies

Key Go packages:
- `github.com/spf13/cobra` - CLI framework
- `github.com/firecracker-microvm/firecracker-go-sdk` - Firecracker API
- `github.com/google/uuid` - VM ID generation
- `github.com/sirupsen/logrus` - Logging (via SDK)
- `golang.org/x/crypto` - SSH client for cluster provisioning
- `gopkg.in/yaml.v3` - Kubeconfig YAML manipulation

## Known Limitations

1. **Linux only** - Firecracker only runs on Linux with KVM
2. **Root required** - Network setup and VM start/stop require elevated privileges
3. **No GPU passthrough** - Firecracker limitation
4. **Serial I/O limits** - ~13k IOPS max (Firecracker limitation)
5. **No live migration** - VMs must be stopped to move

## Bugs Fixed (January 2026)

### 1. IsRunning() permission bug (`internal/firecracker/client.go:194-217`)
**Problem**: `vmm list` showed VMs as "stopped" when run as non-root user, even if VM was running.
**Cause**: `IsRunning()` used `process.Signal(0)` to check if process exists, but this returns EPERM when a non-root user checks a root-owned process.
**Fix**: Handle EPERM as "process exists" (return true) rather than "process not found".

### 2. Missing IP configuration (`internal/firecracker/client.go:40-78`)
**Problem**: VMs started but had no network connectivity (couldn't ping from host).
**Cause**: IP address wasn't being configured inside the VM - the rootfs doesn't have DHCP.
**Fix**: Added `IPAddress` and `Gateway` to `VMConfig`, pass IP configuration via kernel `ip=` parameter.

### 3. TAP device not cleaned up on stop (`cmd/vmm/main.go:335-395`)
**Problem**: Restarting a VM failed with "Resource busy" error.
**Cause**: TAP device wasn't deleted when VM was stopped, so Firecracker couldn't reattach on restart.
**Fix**: Delete TAP device in `stopCmd()` after VM shutdown completes.

## Features Added (January 2026)

### SSH Key Injection (`internal/image/image.go`)
**Feature**: Inject SSH public keys into VM rootfs for passwordless access.
**Implementation**:
- Added `SSHPublicKey` field to VM struct
- Added `--ssh-key` flag to `vmm create` command
- `InjectSSHKey()` function mounts ext4 rootfs, writes authorized_keys file
- SSH key injection happens in `vmm start` after rootfs is copied

**Usage**:
```bash
vmm create myvm --ssh-key ~/.ssh/id_ed25519.pub
vmm start myvm
vmm ssh myvm
```

### Disk Resize (`internal/image/image.go`)
**Feature**: VM rootfs is resized to the requested `--disk` size.
**Implementation**:
- `CreateVMRootfs()` now accepts disk size parameter
- Uses `truncate` to expand the file to requested size
- Uses `resize2fs` to expand the ext4 filesystem
- Resize happens when VM is first started (rootfs created)

**Usage**:
```bash
vmm create myvm --disk 20000  # 20GB disk
```

### Sudo-aware SSH (`cmd/vmm/main.go`)
**Feature**: `vmm ssh` works correctly when run with sudo.
**Problem**: Running `sudo vmm ssh` looked for SSH keys in `/root/.ssh/` instead of the user's home.
**Fix**: Detect `SUDO_USER` environment variable and use original user's SSH keys.

### Sudo-aware Config (`internal/config/config.go`)
**Feature**: Config loading works correctly when running with sudo.
**Problem**: Running `sudo vmm start` loaded config from `/root/.config/` instead of user's config.
**Fix**: `ConfigPath()` detects `SUDO_USER` and uses original user's home directory.

### Outbound Network Connectivity (`internal/network/network.go`)
**Feature**: VMs have full outbound internet access via NAT.
**Implementation**:
- `EnsureBridge()` always ensures NAT rules are in place (not just on bridge creation)
- iptables MASQUERADE rule for outbound traffic (uses `! -o vmm-br0`, interface-agnostic)
- FORWARD rules for bridge-to-host traffic
- MASQUERADE rule no longer depends on `host_interface` matching correctly

### DNS Configuration (`internal/image/image.go`)
**Feature**: Automatic DNS configuration with customizable servers.
**Implementation**:
- Added `DNSServers` field to VM struct
- Added `--dns` flag to `vmm create` (can be repeated)
- `InjectDNSConfig()` writes `/etc/resolv.conf` in VM rootfs
- Default DNS: 8.8.8.8, 8.8.4.4, 1.1.1.1

**Usage**:
```bash
# Default DNS (Google, Cloudflare)
vmm create myvm

# Custom DNS
vmm create myvm --dns 9.9.9.9 --dns 1.0.0.1
```

### Docker Image Import (`internal/image/image.go`)
**Feature**: Import Docker images as VM root filesystems.
**Implementation**:
- `ImportDockerImage()` exports Docker container filesystem
- Installs systemd, openssh-server, and networking tools via chroot
- Configures image for Firecracker (serial console, SSH, networking)
- Creates ext4 filesystem image from exported container
- Added `Image` field to VM struct for image selection
- Added `--image` flag to `vmm create`
- Added `vmm image import` and `vmm image delete` commands

**Process**:
1. Creates a temporary container from the Docker image
2. Exports the container filesystem to a tarball
3. Extracts to a temporary directory
4. Installs required packages: systemd, systemd-sysv, openssh-server, iproute2, iputils-ping, dnsutils
5. Configures systemd as init, enables SSH, sets up serial console
6. Creates ext4 image and copies files into it

**Requirements**:
- Docker must be installed and accessible
- Only Debian/Ubuntu-based images are supported
- Requires root privileges

**Usage**:
```bash
# Import a Docker image
sudo vmm image import ubuntu:22.04 --name ubuntu-base

# Import with custom size (default 2GB)
sudo vmm image import ubuntu:22.04 --name ubuntu-large --size 4096

# Create VM with custom image
sudo vmm create myvm --image ubuntu-base --ssh-key ~/.ssh/id_ed25519.pub

# List available images
vmm image list

# Delete an imported image
sudo vmm image delete ubuntu-base
```

### Host Directory Mounting (`internal/mount/mount.go`)
**Feature**: Mount host directories inside VMs as block devices.
**Implementation**:
- Added `Mount` struct to VM with `HostPath`, `GuestTag`, `ReadOnly`, `ImagePath` fields
- Added `--mount` flag to `vmm create` command (can be repeated for multiple mounts)
- `CreateMountImage()` creates ext4 image from host directory contents
- `SyncMountImage()` refreshes mount image with current host directory contents
- Mount images attached as additional block devices (/dev/vdb, /dev/vdc, etc.)
- Auto-mounted in guest via `/etc/fstab` injection
- Added `vmm mount list` and `vmm mount sync` commands

**How it works**:
1. At `vmm create`, mount specifications are parsed and stored in VM config
2. At `vmm start`, ext4 images are created from each host directory
3. Fstab entries are injected into the VM rootfs for auto-mounting
4. Mount images are attached as additional Firecracker block devices
5. Guest boots with mounts available at `/mnt/<tag>`

**Requirements**:
- Host directories must exist at creation time
- Mount tags must contain only alphanumeric characters, dashes, and underscores
- Requires root privileges (for mounting images and VM operations)

**Usage**:
```bash
# Create VM with a single mount (read-write by default)
sudo vmm create myvm --mount /home/user/code:code --ssh-key ~/.ssh/id_ed25519.pub

# Create VM with multiple mounts
sudo vmm create myvm --mount /home/user/code:code:ro --mount /home/user/output:output:rw

# List mounts for a VM
vmm mount list myvm

# Sync mount contents from host (VM must be stopped)
sudo vmm mount sync myvm code

# Start VM - mounts will be available at /mnt/code, /mnt/output, etc.
sudo vmm start myvm
```

**Guest behavior**:
- Mounts appear as `/dev/vdb`, `/dev/vdc`, etc. (vda is the rootfs)
- Auto-mounted to `/mnt/<tag>` via fstab at boot
- Read-only mounts are enforced at both fstab level and Firecracker block device level

### Host Interface Auto-Detection (`internal/config/config.go`)
**Feature**: Automatically detect the correct network interface for NAT.
**Problem**: Default config hardcoded `eth0`, but systems use various interface names (e.g., `wlp3s0`, `ens33`, `enp0s3`).
**Implementation**:
- Added `detectDefaultInterface()` function that reads `/proc/net/route`
- Finds the interface associated with the default route (destination `00000000`)
- Falls back to `eth0` if detection fails
- Called by `DefaultConfig()` when creating new configurations

**Result**: `vmm config init` now automatically uses the correct interface without manual editing.

### Install Script wget Fallback (`scripts/install.sh`)
**Feature**: Install script works on systems without curl.
**Problem**: Original script required curl, but some minimal systems only have wget.
**Implementation**:
- Added `download_file()` helper function
- Tries curl first, falls back to wget if curl unavailable
- Used for VMM binary, Firecracker, and kernel downloads
- Provides clear error if neither is available
- Also downloads pre-built kernel from GitHub releases (queries API for latest `kernel-*` release)

### Custom Kernel Support (`internal/image/image.go`, `cmd/vmm/main.go`, `scripts/build-kernel.sh`)
**Feature**: Import and manage custom Linux kernels for VMs.
**Implementation**:
- Added `Kernel` field to VM struct (stores custom kernel name)
- Added `--kernel` flag to `vmm create` command
- Added `vmm kernel` commands: `list`, `import`, `delete`, `build`
- `ImportKernel()` validates ELF binary and copies to `/var/lib/vmm/images/kernels/`
- `GetKernelPath()` returns kernel path, falls back to default if empty
- `ListKernelsWithInfo()` returns detailed kernel information
- `validateKernelBinary()` uses `debug/elf` to verify architecture and executable type
- `build-kernel.sh` script for compiling Firecracker-compatible kernels from source

**Kernel validation**:
- Checks ELF magic bytes
- Verifies architecture matches host (x86_64 or aarch64)
- Verifies it's an executable (ET_EXEC)

**Build script features**:
- Dynamically resolves latest patch version from `kernel.org/releases.json` (falls back to hardcoded versions if `jq` unavailable)
- Downloads kernel source from kernel.org
- Creates Firecracker-compatible kernel configuration
- Supports series: 5.10, 6.1, 6.6
- Includes all required options: virtio, serial console, ext4, networking, Docker support (overlay, cgroups, namespaces, iptables/nftables)

**Usage**:
```bash
# Import an existing kernel
sudo vmm kernel import /path/to/vmlinux --name my-kernel

# Build a kernel from source
sudo vmm kernel build --version 6.1 --name kernel-6.1

# List available kernels
vmm kernel list

# Create VM with custom kernel
sudo vmm create myvm --kernel my-kernel --ssh-key ~/.ssh/id_ed25519.pub

# Delete a kernel
sudo vmm kernel delete my-kernel
```

### Configurable VM Defaults (`internal/config/config.go`, `cmd/vmm/main.go`)
**Feature**: Set default values for `vmm create` parameters in the config file.
**Implementation**:
- Added `VMDefaults` struct with fields: `CPUs`, `MemoryMB`, `DiskSizeMB`, `Image`, `Kernel`, `SSHKeyPath`, `DNSServers`
- Added `vm_defaults` section to config file (optional, omitted if empty)
- Added `GetVMDefaults()` helper method to Config
- Updated `createCmd()` to resolve: CLI flag → config default → hardcoded fallback
- Updated `vmm config show` to display VM defaults with source indication
- SSH key path supports `~` expansion for home directory

**Config fields**:
| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpus` | int | 1 | Number of vCPUs |
| `memory_mb` | int | 512 | Memory in MB |
| `disk_size_mb` | int | 1024 | Disk size in MB |
| `image` | string | (default) | Rootfs image name |
| `kernel` | string | (default) | Kernel name |
| `ssh_key_path` | string | (none) | Path to SSH public key |
| `dns_servers` | []string | [8.8.8.8, 8.8.4.4, 1.1.1.1] | DNS servers |

**Example config** (`~/.config/vmm/config.json`):
```json
{
  "data_dir": "/var/lib/vmm",
  "bridge_name": "vmm-br0",
  "subnet": "172.16.0.0/16",
  "gateway": "172.16.0.1",
  "host_interface": "eth0",
  "vm_defaults": {
    "cpus": 2,
    "memory_mb": 1024,
    "disk_size_mb": 4096,
    "ssh_key_path": "~/.ssh/id_ed25519.pub",
    "kernel": "kernel-6.1"
  }
}
```

**Usage**:
```bash
# View current defaults (shows source: config or default)
vmm config show

# Create VM using config defaults
sudo vmm create myvm

# Override config default with CLI flag
sudo vmm create myvm --cpus 4 --memory 2048
```

**Backward compatibility**: Existing configs without `vm_defaults` continue to work unchanged.

### Version Command (`cmd/vmm/main.go`)
**Feature**: Display version, git commit, and build date information.
**Implementation**:
- Added `version`, `commit`, and `date` variables set at build time via ldflags
- Added `vmm version` command with optional `--json` flag
- Added `Version` field to root command for `--version` flag support
- GoReleaser injects these values during release builds
- Makefile provides version injection for local development builds

**Build-time injection**:
- `main.version` - Version string (e.g., "0.2.0" or "dev")
- `main.commit` - Git commit hash (e.g., "abc1234")
- `main.date` - Build timestamp (e.g., "2026-01-18T10:30:00Z")

**Usage**:
```bash
# Default output
vmm version
# vmm version 0.2.0
# commit: abc1234
# built: 2026-01-18T10:30:00Z

# JSON output
vmm version --json
# {"version":"0.2.0","commit":"abc1234","date":"2026-01-18T10:30:00Z"}

# Short version flag
vmm --version
# vmm version 0.2.0

# Build with version info (local development)
make build && ./vmm version
```

### CI Kernel Build (`.github/workflows/build-kernel.yml`)
**Feature**: Automated kernel builds via GitHub Actions, published as GitHub releases.
**Implementation**:
- Workflow triggers: manual dispatch (configurable `kernel_series`), push to `scripts/build-kernel.sh` or workflow file, weekly schedule (Monday 06:00 UTC)
- Queries `kernel.org/releases.json` for latest patch version in the 6.1 series
- Skips build if a release with tag `kernel-<version>` already exists
- Runs `scripts/build-kernel.sh` to compile kernel
- Creates GitHub release with tag `kernel-<version>`, attaches `vmlinux.bin`
- Workflow has `contents: write` permission for creating releases

**GitHub release tagging convention**:
- `v*` tags → software releases (GoReleaser binary builds)
- `kernel-*` tags → default kernel releases (6.1 series, built by kernel workflow)
- `k8s-kernel-*` tags → Kubernetes kernel releases (6.6 series, built by kernel workflow)
- `rootfs-*` tags → rootfs releases (built by rootfs workflow, format: `rootfs-24.04-YYYYMMDD`)
- `k8s-rootfs-*` tags → Kubernetes rootfs releases (built by k8s-rootfs workflow, format: `k8s-rootfs-<k8s-version>`)

**Kernel download chain**:
- `scripts/install.sh` → queries GitHub API for latest `kernel-*` release (downloads `vmlinux.bin`) and latest `k8s-kernel-*` release (downloads `k8s-vmlinux.bin` as `k8s-kernel`)
- `vmm image pull` (Go) → `findLatestKernelURL()` queries GitHub API, falls back to `FallbackKernelURL` (S3)
- Both use `api.github.com/repos/raesene/baremetalvmm/releases` to find kernel assets

**Rootfs download chain**:
- `scripts/install.sh` → queries GitHub API for latest `rootfs-*` release, downloads `rootfs.ext4.gz`, decompresses with `gunzip`, falls back to S3 URL
- `vmm image pull` (Go) → `findLatestRootfsURL()` queries GitHub API, downloads gzipped rootfs, decompresses with `compress/gzip`, falls back to `FallbackRootfsURL` (S3)
- Both use `api.github.com/repos/raesene/baremetalvmm/releases` to find rootfs assets

### Dynamic Kernel Version Resolution (`scripts/build-kernel.sh`)
**Feature**: Build script resolves the latest patch version dynamically from kernel.org.
**Problem**: Kernel patch versions were hardcoded, causing builds to use stale versions.
**Implementation**:
- `get_kernel_url()` queries `kernel.org/releases.json` via `jq` to find latest patch in a series
- Falls back to hardcoded versions if `jq` is unavailable or query fails
- Logs the resolved version: "Resolved kernel series 6.1 to version 6.1.162"

### Default Rootfs Build (`scripts/build-rootfs.sh`, `.github/workflows/build-rootfs.yml`)
**Feature**: Build Ubuntu 24.04-based rootfs images for Firecracker, published as GitHub releases.
**Implementation**:
- `scripts/build-rootfs.sh` creates ext4 rootfs from Docker base image
- Exports Docker container filesystem, configures via chroot (systemd, SSH, networking), creates ext4 image, gzip compresses output
- `.github/workflows/build-rootfs.yml` automates builds: manual dispatch, push to script/workflow, weekly Monday 07:00 UTC
- Tag format: `rootfs-24.04-YYYYMMDD` — skips build if release already exists
- Go code (`findLatestRootfsURL()`) and install script query GitHub API for `rootfs-*` releases, fall back to S3 URL

**Build script parameters**:
- `--name` (required) — output filename
- `--output` (default: `/var/lib/vmm/images/rootfs`) — output directory
- `--size` (default: 512) — image size in MB
- `--base-image` (default: `ubuntu:24.04`) — Docker base image
- `--no-cleanup` — keep temporary directory

**Rootfs contents**:
- systemd init, serial console on ttyS0, OpenSSH server (root key-only login)
- iproute2, iputils-ping, dbus
- systemd-networkd for eth0, /etc/fstab for /dev/vda
- Locked root password (SSH key login only)

**Usage**:
```bash
# Build locally
sudo bash scripts/build-rootfs.sh --name rootfs.ext4 --size 512 --output /tmp

# Decompress for use
gunzip /tmp/rootfs.ext4.gz
```

### Kubernetes Cluster Management (`internal/cluster/`, `cmd/vmm/main.go`)
**Feature**: Create Kubernetes clusters from multiple Firecracker VMs (like kind but with VM isolation).
**Implementation**:
- `internal/cluster/cluster.go` - Cluster state model with JSON persistence (mirrors vm.go pattern)
- `internal/cluster/provisioner.go` - SSH-based bootstrap: containerd, kubeadm, Cilium CNI
- `internal/cluster/kubeconfig.go` - Extract kubeconfig from control plane, merge into `~/.kube/config`
- `cmd/vmm/main.go` - `vmm cluster` command group (create, delete, list, kubeconfig)

**Dependencies added**: `golang.org/x/crypto` (SSH), `gopkg.in/yaml.v3` (kubeconfig YAML)

**Cluster provisioning sequence**:
1. Create VMs: `{name}-control-plane`, `{name}-worker-1..N`
2. Start all VMs, wait for SSH (poll every 2s, 120s timeout)
3. Install containerd on all nodes (parallel)
4. Install kubeadm/kubelet/kubectl on all nodes (parallel)
5. `kubeadm init` on control plane with `--skip-phases=addon/kube-proxy --ignore-preflight-errors=SystemVerification`
6. Install Cilium CLI, `cilium install --set kubeProxyReplacement=true`
7. Join workers with `kubeadm join` (parallel)
8. Wait for all nodes Ready (poll, 5min timeout)
9. Extract kubeconfig, merge as context `vmm-{name}`

**Kernel requirements**: Kernel 6.6+ required (Cilium 1.19+ needs tcx links). Must have: `CONFIG_BPF_JIT`, `CONFIG_MODULES`, `CONFIG_VXLAN`, `CONFIG_CFS_BANDWIDTH`, `CONFIG_CGROUPS`, all netfilter/xtables modules. See `scripts/build-kernel.sh`.

**VM bootstrap details**:
- `mount --make-rshared /` for Kubernetes volume mounts and Cilium
- `mount -t bpf bpf /sys/fs/bpf` for Cilium BPF programs
- containerd with `SystemdCgroup = true`
- `--ignore-preflight-errors=SystemVerification` because Firecracker VMs have no `/lib/modules/`

**Defaults**: 0 workers, 2 CPUs, 4096 MB RAM, 10240 MB disk, k8s version 1.31.4

**Usage**:
```bash
# Build a Kubernetes-compatible kernel first
sudo vmm kernel build --version 6.6 --name k8s-kernel

# Single-node cluster
sudo vmm cluster create test1 --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel

# Multi-node cluster
sudo vmm cluster create test2 --workers 2 --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel

# Use cluster
kubectl --context vmm-test1 get nodes

# Delete cluster
sudo vmm cluster delete test1 -f
```

### Shell Completion (`cmd/vmm/main.go`)
**Feature**: Tab completion for commands, VM names, cluster names, kernel names, and image names.
**Implementation**:
- Cobra's built-in `completion` subcommand generates scripts for bash, zsh, and fish
- `ValidArgsFunction` on all commands that take positional names (start, stop, delete, ssh, port-forward, mount list/sync, cluster delete, cluster kubeconfig)
- `RegisterFlagCompletionFunc` for `--kernel` and `--image` flags on create commands
- All completions return `ShellCompDirectiveNoFileComp` to suppress file suggestions

**Usage**:
```bash
source <(vmm completion bash)   # or zsh/fish
```

### SSH Agent Support (`internal/cluster/provisioner.go`)
**Feature**: Passphrase-protected SSH keys work via ssh-agent for cluster provisioning.
**Implementation**:
- SSH client tries `SSH_AUTH_SOCK` agent first, then falls back to reading key file directly
- If key is encrypted and no agent is available, error message guides user to `ssh-add`
- Requires `sudo -E` to preserve `SSH_AUTH_SOCK` when running with sudo

### Interface-Agnostic NAT (`internal/network/network.go`)
**Feature**: MASQUERADE rule works regardless of host network interface name.
**Problem**: Previous rule used `-o <host_interface>`, which silently failed if config didn't match actual interface.
**Fix**: Changed to `! -o vmm-br0` — NATs traffic from VMs going out any interface except back to the bridge.

### Next-Free IP Allocation (`internal/network/network.go`, `cmd/vmm/main.go`)
**Feature**: IP allocation scans existing VMs and picks the next unused address.
**Problem**: Previous allocation used VM list index, causing IP collisions when running multiple clusters.
**Fix**: `AllocateIP()` now takes a list of used IPs and finds the first free address from 172.16.0.2 upward.

### Web UI (`cmd/vmm-web/`, `internal/web/`, `web/`)
**Feature**: Browser-based dashboard for managing VMs and clusters, plus a JSON REST API.
**Implementation**:
- Separate binary (`vmm-web`) using Chi router, HTMX + Go templates, Tailwind CSS via CDN
- `internal/web/server.go` - Chi router setup, middleware stack (logging, security headers, auth, CSRF)
- `internal/web/auth.go` - Session-based auth with `VMM_WEB_PASSWORD` env var, rate-limited login, Bearer token support for API
- `internal/web/handlers_vm.go` - VM CRUD (HTML pages + `/api/v1/` JSON endpoints), HTMX partial updates for start/stop
- `internal/web/handlers_cluster.go` - Cluster CRUD (HTML + JSON API)
- `internal/web/handlers_dashboard.go` - Dashboard with stats, SSE broker for live VM status polling
- `web/embed.go` - `go:embed` directive embeds templates and static assets into the binary
- `web/templates/` - 9 HTML templates using `{{template "layout.html" .}}` pattern with `{{define "content"}}` blocks
- `web/static/` - HTMX 2.0.4, SSE extension, custom CSS with status badges
- Default listen: `127.0.0.1:8080`, explicit `--listen 0.0.0.0:8080` for remote access
- Security: HttpOnly SameSite=Strict cookies, CSRF on forms/HTMX, CSP headers, login rate limiting (5/min)
- All VM/cluster operations reuse the same `internal/` packages as the CLI

**Dependencies added**: `github.com/go-chi/chi/v5`

**Usage**:
```bash
VMM_WEB_PASSWORD=mypassword sudo -E vmm-web --listen 0.0.0.0:8080
```

**API endpoints**: `/api/v1/vms`, `/api/v1/vms/{name}`, `/api/v1/vms/{name}/start`, `/api/v1/vms/{name}/stop`, `/api/v1/clusters`, `/api/v1/health`

## Future Improvements (Not Yet Implemented)

1. **Cloud-init** - Full cloud-init support for more flexible VM initialization
2. **Jailer integration** - Production security hardening
3. **Resource quotas** - CPU/memory/disk limits
4. **VM snapshots** - Save/restore VM state

## Code Style

- Follow standard Go conventions
- Error messages should be user-friendly
- Use `fmt.Errorf("context: %w", err)` for error wrapping
- Commands should provide clear feedback on success/failure
- Hidden commands (autostart/autostop) for internal use only

## Debugging

### Check VM state
```bash
cat /var/lib/vmm/vms/<name>.json
```

### Check Firecracker logs
```bash
cat /var/lib/vmm/logs/<name>.log
```

### Check network setup
```bash
ip link show vmm-br0
ip link show vmm-<id>
sudo iptables -t nat -L -n -v
```

### Check Firecracker process
```bash
ps aux | grep firecracker
ls -la /var/lib/vmm/sockets/
```

### Test VM network connectivity
```bash
ping 172.16.0.2  # First VM's IP
```

### Verify host interface configuration
```bash
# Check which interface has internet access
ip route | grep default
# Example output: default via 192.168.1.1 dev wlp3s0

# Check config (host_interface is auto-detected on config init)
cat ~/.config/vmm/config.json
# If needed, manually edit "host_interface" to match the interface name
```

### Test outbound connectivity from VM
```bash
vmm ssh myvm -- 'getent hosts google.com'  # Test DNS
vmm ssh myvm -- 'curl -s http://example.com'  # Test HTTP
```

### Pre-built Kubernetes Rootfs (`scripts/build-k8s-rootfs.sh`, `.github/workflows/build-k8s-rootfs.yml`)
**Feature**: Pre-bake containerd + kubeadm/kubelet/kubectl into rootfs images to speed up cluster creation.
**Problem**: Cluster creation spent 30-60s per node downloading and installing packages at runtime.
**Implementation**:
- `scripts/build-k8s-rootfs.sh` extends the base rootfs build to also install containerd, kubeadm, kubelet, kubectl, and configure SystemdCgroup + k8s sysctl
- `.github/workflows/build-k8s-rootfs.yml` builds and publishes as GitHub releases with tag `k8s-rootfs-<version>`
- Workflow queries `dl.k8s.io/release/stable.txt` for latest stable version; runs weekly Monday 08:00 UTC
- `findLatestK8sRootfsURL()` in image.go queries GitHub API for matching `k8s-rootfs-*` releases
- `FindK8sRootfs()` checks locally for existing k8s rootfs images matching the requested version
- `DownloadK8sRootfs()` downloads from GitHub releases if not available locally
- `cluster create` auto-detects k8s rootfs: checks local → downloads from GitHub → falls back to installing at runtime
- `provisioner.go` detects pre-installed kubeadm via `which kubeadm` and skips `installContainerd`/`installKubeadm`, running only the lightweight `preparePreinstalledNode` instead (modprobe, sysctl, mount, restart containerd)
- A marker file `/etc/vmm-k8s-version` is written into the rootfs for future version verification

**GitHub release tagging**: `k8s-rootfs-<version>` (e.g., `k8s-rootfs-1.35.3`)

**Default k8s version**: Updated from 1.31.4 to 1.35.3

**Usage**:
```bash
# Build locally
sudo bash scripts/build-k8s-rootfs.sh --k8s-version 1.35.3 --name k8s-rootfs.ext4 --size 2048 --output /tmp

# Import manually
gunzip /tmp/k8s-rootfs.ext4.gz
sudo cp /tmp/k8s-rootfs.ext4 /var/lib/vmm/images/rootfs/k8s-1.35.3.ext4

# Cluster create auto-detects (or downloads) k8s rootfs
sudo vmm cluster create test1 --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel

# Or specify explicitly
sudo vmm cluster create test1 --image k8s-1.35.3 --kernel k8s-kernel --ssh-key ~/.ssh/id_ed25519.pub
```
