# CLAUDE.md - Project Context for AI Assistants

This file provides context for Claude or other AI assistants working on this codebase.

## Project Overview

VMM (Bare Metal MicroVM Manager) is a Go-based CLI tool for managing Firecracker microVMs on Ubuntu 24.04. It's designed for development environments supporting 10-50 concurrent VMs.

**Status**: Core functionality complete and tested. See PLAN.md for implementation status.

## Tech Stack

- **Language**: Go 1.21+
- **VMM Engine**: Firecracker v1.11.0 (via firecracker-go-sdk)
- **CLI Framework**: Cobra (github.com/spf13/cobra)
- **Networking**: Linux TAP devices, bridges, iptables
- **Storage**: JSON-based VM configs, ext4 rootfs images
- **Service**: systemd for auto-start

## Directory Structure

```
baremetalvmm/
├── cmd/vmm/main.go           # CLI entry point, all command definitions
├── internal/
│   ├── config/config.go      # Global config, paths, defaults
│   ├── vm/vm.go              # VM struct, state machine, persistence
│   ├── firecracker/client.go # Firecracker SDK wrapper
│   ├── network/network.go    # TAP, bridge, iptables management
│   ├── image/image.go        # Kernel/rootfs download and management
│   └── mount/mount.go        # Host directory mount management
├── scripts/
│   ├── install.sh            # Installation script (binary + Firecracker)
│   ├── install-service.sh    # Systemd service installation (optional)
│   ├── build-kernel.sh       # Custom kernel build script
│   └── vmm.service           # Systemd unit file
├── go.mod, go.sum            # Dependencies
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
- IP allocation: sequential from 172.16.0.2
- NAT via iptables MASQUERADE
- Port forwarding via DNAT rules

### 5. Image Management (`internal/image/`)
- Downloads kernel/rootfs from Firecracker quickstart URLs
- Creates per-VM rootfs copies for persistence
- Stored in `/var/lib/vmm/images/`

### 6. Mount Management (`internal/mount/`)
- Creates ext4 images from host directories for VM mounts
- Mount images stored in `/var/lib/vmm/mounts/`
- Supports read-only and read-write mounts
- Auto-mounts in guest via fstab injection

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
vmm config show
vmm config init
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
# Build
go build -o vmm ./cmd/vmm/

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
- iptables MASQUERADE rule for outbound traffic
- FORWARD rules for bridge-to-host traffic
- Uses `host_interface` from config (must match actual network interface)

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
- Used for both VMM binary and Firecracker downloads
- Provides clear error if neither is available

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
- Downloads kernel source from kernel.org
- Creates Firecracker-compatible kernel configuration
- Supports versions: 5.10, 6.1, 6.6
- Includes all required options: virtio, serial console, ext4, networking

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

## Future Improvements (Not Yet Implemented)

1. **Cloud-init** - Full cloud-init support for more flexible VM initialization
2. **Jailer integration** - Production security hardening
3. **Resource quotas** - CPU/memory/disk limits
4. **Better IP management** - Persistent IP allocation
5. **Web UI** - Optional browser-based management
6. **VM snapshots** - Save/restore VM state

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
