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
│   └── image/image.go        # Kernel/rootfs download and management
├── scripts/
│   ├── install.sh            # Installation script
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

## CLI Commands

```
vmm create <name> [--cpus N] [--memory MB] [--disk MB]
vmm start <name>
vmm stop <name>
vmm delete <name> [-f]
vmm list [-a]
vmm ssh <name> [-u user]
vmm port-forward <name> <host>:<guest>
vmm image list
vmm image pull
vmm config show
vmm config init
vmm autostart   # Hidden, used by systemd
vmm autostop    # Hidden, used by systemd
```

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

### SSH Key Injection (`internal/image/image.go:193-243`)
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

## Future Improvements (Not Yet Implemented)

1. **Cloud-init** - Full cloud-init support for more flexible VM initialization
2. **Jailer integration** - Production security hardening
3. **Resource quotas** - CPU/memory/disk limits
4. **Better IP management** - Persistent IP allocation
5. **Web UI** - Optional browser-based management
6. **VM snapshots** - Save/restore VM state
7. **Custom images** - Support for user-provided kernels/rootfs

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
