# VMM - Bare Metal MicroVM Manager

A lightweight CLI tool to manage Firecracker microVMs for development environments on Ubuntu 24.04.

## Features

- **Fast Boot Times** - VMs start in under 1 second using Firecracker
- **Low Overhead** - Each VM uses <5MB memory overhead
- **Bridge Networking** - Full network connectivity with NAT and port forwarding
- **Persistent Storage** - VM disks survive restarts
- **Auto-Start** - VMs automatically restart after host reboot
- **Simple CLI** - Intuitive commands for VM lifecycle management

## Requirements

- Ubuntu 24.04 (or compatible Linux distribution)
- KVM support (`/dev/kvm` must be accessible)
- Root access (for networking setup)
- Go 1.21+ (for building from source)

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/raesene/baremetalvmm.git
cd vmm

# Build and install (requires root)
sudo ./scripts/install.sh
```

The install script will:
- Build the `vmm` binary
- Install it to `/usr/local/bin`
- Download Firecracker v1.11.0
- Create data directories in `/var/lib/vmm`
- Install the systemd service

### Basic Usage

```bash
# Initialize configuration
vmm config init

# Download kernel and rootfs images
sudo vmm image pull

# Create a VM with SSH key for access
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub

# Start the VM
sudo vmm start myvm

# List VMs (works as non-root)
vmm list

# SSH into the VM
vmm ssh myvm

# Stop the VM
sudo vmm stop myvm

# Delete the VM
sudo vmm delete myvm
```

## Commands

### VM Lifecycle

| Command | Description |
|---------|-------------|
| `vmm create <name>` | Create a new VM |
| `vmm start <name>` | Start a VM (requires root) |
| `vmm stop <name>` | Stop a running VM (requires root) |
| `vmm delete <name>` | Delete a VM and its resources |
| `vmm list` | List all VMs |

### Create Options

```bash
vmm create <name> [flags]

Flags:
  --cpus int         Number of vCPUs (default 1)
  --memory int       Memory in MB (default 512)
  --disk int         Disk size in MB (default 1024)
  --ssh-key string   Path to SSH public key file for root access
```

### Access

| Command | Description |
|---------|-------------|
| `vmm ssh <name>` | SSH into a VM as root |
| `vmm ssh <name> -u <user>` | SSH as specific user |

**Note**: SSH access requires an SSH public key to be configured when creating the VM using the `--ssh-key` flag. The key is injected into the VM's rootfs at startup.

### Networking

| Command | Description |
|---------|-------------|
| `vmm port-forward <name> <host>:<guest>` | Forward port from host to VM |

Example:
```bash
# Forward host port 8080 to VM port 80
vmm port-forward myvm 8080:80
```

### Images

| Command | Description |
|---------|-------------|
| `vmm image list` | List available images |
| `vmm image pull` | Download default images |

### Configuration

| Command | Description |
|---------|-------------|
| `vmm config show` | Show current configuration |
| `vmm config init` | Initialize directories and config |

## Architecture

```
┌─────────────────────────────────────────────────────────┐
│                      vmm CLI                             │
├─────────────────────────────────────────────────────────┤
│  create | start | stop | delete | list | ssh | ...      │
└─────────────────────────────────────────────────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                  Internal Components                     │
├──────────────┬──────────────┬──────────────┬────────────┤
│   Config     │   Network    │    Image     │ Firecracker│
│   Store      │   Manager    │   Manager    │   Client   │
└──────────────┴──────────────┴──────────────┴────────────┘
                            │
                            ▼
┌─────────────────────────────────────────────────────────┐
│                  Firecracker VMM                         │
│              (One process per microVM)                   │
└─────────────────────────────────────────────────────────┘
```

### Networking

VMs are connected via a bridge network with automatic IP configuration:

```
Host Network (eth0)
       │
       ▼
┌──────────────┐
│   iptables   │  ← NAT/MASQUERADE
│   DNAT/SNAT  │  ← Port forwarding
└──────────────┘
       │
       ▼
┌──────────────┐
│   vmm-br0    │  ← Bridge (172.16.0.1/16)
└──────────────┘
    │  │  │
    ▼  ▼  ▼
  tap0 tap1 tap2  ← One TAP per VM
    │  │  │
    ▼  ▼  ▼
  VM1 VM2 VM3     ← 172.16.0.2, 172.16.0.3, ...
```

IP addresses are configured via kernel command line parameters, so VMs get network connectivity immediately on boot.

## Directory Structure

```
/var/lib/vmm/
├── config/           # Global configuration
├── vms/              # VM configurations and rootfs
├── images/
│   ├── kernels/      # Linux kernel images
│   └── rootfs/       # Root filesystem images
├── sockets/          # Firecracker API sockets
├── logs/             # VM logs
└── state/            # Runtime state
```

## Auto-Start on Boot

To enable VMs to automatically start after a host reboot:

```bash
# Enable the systemd service
sudo systemctl enable vmm

# Check status
sudo systemctl status vmm
```

VMs with `auto_start: true` (the default) will be started automatically.

## SSH Key Injection

VMM automatically injects SSH public keys into VMs at startup. When you create a VM with the `--ssh-key` flag, the specified public key is stored in the VM configuration. When the VM starts, VMM:

1. Mounts the VM's rootfs image
2. Creates `/root/.ssh/` directory if needed
3. Writes the public key to `/root/.ssh/authorized_keys`
4. Sets correct permissions (700 for directory, 600 for file)
5. Unmounts and boots the VM

This allows passwordless SSH access as root using your existing SSH key pair.

**Note**: SSH key injection requires root privileges (for mounting the rootfs image).

## Troubleshooting

### KVM not available

```
Error: /dev/kvm not found
```

Ensure:
1. Your CPU supports virtualization (Intel VT-x or AMD-V)
2. Virtualization is enabled in BIOS
3. KVM modules are loaded: `sudo modprobe kvm_intel` or `sudo modprobe kvm_amd`

### Permission denied on /dev/kvm

```bash
# Add your user to the kvm group
sudo usermod -aG kvm $USER
# Log out and back in
```

### Network not working in VM

Ensure IP forwarding is enabled:
```bash
sudo sysctl -w net.ipv4.ip_forward=1
```

Check iptables rules:
```bash
sudo iptables -t nat -L -n
```

Test connectivity from host:
```bash
ping 172.16.0.2
```

### VM won't start

Check the VM log:
```bash
cat /var/lib/vmm/logs/<vmname>.log
```

Check Firecracker socket:
```bash
ls -la /var/lib/vmm/sockets/
```

### VM shows as stopped when running

Ensure you're checking with `vmm list` (no sudo required). The tool correctly detects running VMs even when run as non-root.

## Development

### Building from Source

```bash
# Install Go 1.21+
# Clone the repo
git clone https://github.com/raesene/baremetalvmm.git
cd vmm

# Build
go build -o vmm ./cmd/vmm/

# Run tests
go test ./...
```

### Project Structure

```
├── cmd/vmm/main.go           # CLI entry point
├── internal/
│   ├── config/               # Configuration management
│   ├── vm/                   # VM struct and persistence
│   ├── firecracker/          # Firecracker SDK wrapper
│   ├── network/              # TAP/bridge networking
│   └── image/                # Kernel/rootfs management
├── scripts/
│   ├── install.sh            # Installation script
│   └── vmm.service           # Systemd service
└── go.mod                    # Go modules
```

## Known Limitations

1. **Linux only** - Firecracker only runs on Linux with KVM
2. **Root required** - VM start/stop and networking require root privileges
3. **No GPU passthrough** - Firecracker limitation
4. **No live migration** - VMs must be stopped to move

## License

MIT License - see LICENSE file for details.

## Acknowledgments

- [Firecracker](https://github.com/firecracker-microvm/firecracker) - The microVM engine
- [firecracker-go-sdk](https://github.com/firecracker-microvm/firecracker-go-sdk) - Go SDK for Firecracker
- [Cobra](https://github.com/spf13/cobra) - CLI framework
