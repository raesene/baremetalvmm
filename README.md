# VMM - Bare Metal MicroVM Manager

**WARNING** This is a vibe-coded piece of software allow for creation of microVMs conveniently. It's still heavily in development, I do not recommend anyone apart from me use it :) !

The goal of the project is to allow for small development VMs to be spun up based on [firecracker](https://github.com/firecracker-microvm/firecracker), so they're lightweight. It can build VM images from a Docker image, allowing for custom VMs.

The goal of the project is to be useful in cases where you want something like Docker, but want some more isolation that Docker provides, or you want to do lower level tasks in the VM that don't suit Docker well. N.B we're not there yet!

Pretty much all of the coding has been done with [Claude code](https://github.com/anthropics/claude-code).

## Requirements

- Ubuntu 24.04 (or compatible Linux distribution). All testing has been done on Ubuntu 24.04, so it's likely only to work with that distro.
- KVM support (`/dev/kvm` must be accessible)
- Root access (for networking setup)
- Go 1.21+ (only if building from source)

## Quick Start

### Installation

```bash
# Clone the repository
git clone https://github.com/raesene/baremetalvmm.git
cd baremetalvmm

# Install (requires root)
sudo ./scripts/install.sh
```

The install script will:
- Download the pre-built `vmm` binary from GitHub releases (amd64/arm64)
- Fall back to building from source if download fails
- Install the binary to `/usr/local/bin`
- Download Firecracker v1.11.0
- Download a pre-built Linux 6.1 kernel from GitHub releases
- Download a pre-built Ubuntu 24.04 rootfs from GitHub releases (falls back to Firecracker S3 URL)
- Create data directories in `/var/lib/vmm`
- Install `build-kernel.sh` and `build-rootfs.sh` to `/usr/local/share/vmm`

### Uninstallation

To completely remove VMM and all associated resources:

```bash
sudo ./scripts/uninstall.sh
```

The uninstall script will:
- Stop all running VMs (Firecracker processes)
- Remove network resources (bridge, TAP devices, iptables rules)
- Remove all VM data (`/var/lib/vmm`)
- Remove user configuration (`~/.config/vmm`)
- Remove the systemd service (if installed)
- Remove binaries (`vmm`, `firecracker`, `build-kernel.sh`, `build-rootfs.sh`)

Use `--yes` or `-y` to skip the confirmation prompt:

```bash
sudo ./scripts/uninstall.sh --yes
```

**Note**: The script is idempotent and safe to run multiple times.

### One time Setup

First up (one time only) run the init command

```bash
vmm config init
```

Next up we need to pull the default kernel and root image. The kernel is a pre-built Linux 6.1 kernel from our GitHub releases, and the rootfs is a pre-built Ubuntu 24.04 image also from our GitHub releases. We can change the rootfs with more commands and also use custom kernels (see Custom Kernels section). Again this is one-time, they should be present for future runs

```bash
sudo vmm image pull
```

### Basic usage

First up we create a VM. Key elements we can configure here are number of CPUs, amount of memory, amount of disk space and importantly an SSH key to use to connect to the VM once it's started. there also also other options for things like custom images (see later in README) and custom DNS servers.

```bash
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
```

Once the VM is created, we can start it up

```bash
sudo vmm start myvm
```

Then once it's started we should be able to SSH in to it. That can be done by name using the `vmm` command as shown below, or you can just use standard `ssh` with a username of `root` and the IP address of the VM. By default it's only reachable from the local machine, but you can use the `port-forward` command to expose the VM to the wider world (using an iptables command under the covers)

```bash
vmm ssh myvm
```

To stop the VM but leave it in place

```bash
sudo vmm stop myvm
```

and then to clean it up

```bash
sudo vmm delete myvm
```

## Custom Rootfs and kernel

By default, `vmm image pull` downloads a pre-built Linux 6.1 kernel and an Ubuntu 24.04 rootfs from our GitHub releases (both built automatically via CI). The default rootfs includes systemd, OpenSSH server, and basic networking tools. If you want to run more complex use-cases it makes sense to get a custom rootfs.

### Custom rootfs

The way this works is that vmm can get a docker image (needs docker installed) and turn it into a vmm base image, by injecting the necessary files for openssh server and the init system. So far this is all ubuntu based, so you want to stick with that for now. 

The vmm image import command will handle that it, you give it the image to pull in docker format and a name to call it

```bash
sudo vmm image import ubuntu:24.04 --name ubuntu-24.04
```

### Custom kernel

If you want a different kernel version, you can build one from source. Be aware it's going to download and compile a Linux kernel, so it'll take a while if you're running on a not very powerful machine and it needs disk space.

This command should give you a relatively modern 6.1 based kernel.

```bash
sudo vmm kernel build --version 6.1 --name kernel-6.1
```

## Commands

### VM Lifecycle

| Command | Description |
|---------|-------------|
| `vmm create <name>` | Create a new VM configuration (VM is not running yet) |
| `vmm start <name>` | Start a VM - assigns IP address, sets up networking, boots VM (requires root) |
| `vmm stop <name>` | Stop a running VM (requires root) |
| `vmm delete <name>` | Delete a VM and its resources |
| `vmm list` | List all VMs |

**Note**: VMs must be explicitly started after creation. IP addresses are assigned at start time, not at creation time.

### Create Options

```bash
vmm create <name> [flags]

Flags:
  --cpus int         Number of vCPUs (default 1)
  --memory int       Memory in MB (default 512)
  --disk int         Disk size in MB (default 1024)
  --ssh-key string   Path to SSH public key file for root access
  --dns string       Custom DNS servers (can be specified multiple times)
  --image string     Name of rootfs image to use (from 'vmm image import')
  --kernel string    Name of kernel to use (from 'vmm kernel import' or 'vmm kernel build')
  --mount string     Mount host directory in VM (format: /host/path:tag[:ro|rw], can be repeated)
```

Example with all options:
```bash
sudo vmm create myvm --cpus 2 --memory 2048 --disk 10000 \
  --ssh-key ~/.ssh/id_ed25519.pub \
  --dns 9.9.9.9 --dns 1.1.1.1 \
  --image ubuntu-base \
  --kernel my-kernel \
  --mount /home/user/code:code:ro
```

### Access

| Command | Description |
|---------|-------------|
| `vmm ssh <name>` | SSH into a VM as root |
| `vmm ssh <name> -u <user>` | SSH as specific user |

**Note**: SSH access requires an SSH public key to be configured when creating the VM using the `--ssh-key` flag. The key is injected into the VM's rootfs at startup.

**Tip**: You can use `sudo vmm ssh <name>` if you prefer consistency with other commands. When run with sudo, VMM automatically detects the original user and uses their SSH keys from their home directory.

### Networking

| Command | Description |
|---------|-------------|
| `vmm port-forward <name> <host>:<guest>` | Forward port from host to VM |

Example:
```bash
# Forward host port 8080 to VM port 80 Needs sudo for iptables rights
sudo vmm port-forward myvm 8080:80
```

### Mounts

| Command | Description |
|---------|-------------|
| `vmm mount list <name>` | List mounts configured for a VM |
| `vmm mount sync <name> <tag>` | Sync mount image from host directory (VM must be stopped) |

Example:
```bash
# List mounts for a VM
vmm mount list myvm

# Sync mount contents after making changes on host
sudo vmm mount sync myvm code
```

### Images

| Command | Description |
|---------|-------------|
| `vmm image list` | List available images |
| `vmm image pull` | Download default images |
| `vmm image import <docker-image> --name <name>` | Import a Docker image as rootfs |
| `vmm image delete <name>` | Delete an imported image |

### Kernels

| Command | Description |
|---------|-------------|
| `vmm kernel list` | List available kernels |
| `vmm kernel import <path> --name <name>` | Import a custom kernel binary |
| `vmm kernel build --version <ver> --name <name>` | Build a kernel from source |
| `vmm kernel delete <name>` | Delete a custom kernel |

### Configuration

| Command | Description |
|---------|-------------|
| `vmm config show` | Show current configuration |
| `vmm config init` | Initialize directories and config |

## Configurable VM Defaults

You can set default values for `vmm create` parameters in your config file (`~/.config/vmm/config.json`). This is useful if you typically use the same settings for most VMs.

### Available Default Settings

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `cpus` | int | 1 | Number of vCPUs |
| `memory_mb` | int | 512 | Memory in MB |
| `disk_size_mb` | int | 1024 | Disk size in MB |
| `image` | string | (default rootfs) | Rootfs image name |
| `kernel` | string | (default kernel) | Kernel name |
| `ssh_key_path` | string | (none) | Path to SSH public key |
| `dns_servers` | []string | [8.8.8.8, 8.8.4.4, 1.1.1.1] | DNS servers |

### Example Configuration

Edit `~/.config/vmm/config.json` to add a `vm_defaults` section:

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
    "kernel": "kernel-6.1",
    "dns_servers": ["9.9.9.9", "1.1.1.1"]
  }
}
```

### How Defaults Work

When you run `vmm create`, values are resolved in this order:
1. **CLI flag** - If you specify a flag (e.g., `--cpus 4`), it takes priority
2. **Config default** - If no flag is given, uses the value from `vm_defaults`
3. **Built-in default** - If neither is set, uses the built-in default

### Usage Examples

```bash
# With the example config above, this creates a VM with:
# - 2 CPUs, 1024 MB memory, 4096 MB disk (from config)
# - SSH key from ~/.ssh/id_ed25519.pub (from config)
# - kernel-6.1 kernel (from config)
sudo vmm create myvm

# Override specific defaults with CLI flags:
# - 4 CPUs (from flag), 1024 MB memory (from config)
sudo vmm create myvm --cpus 4

# Override multiple defaults:
sudo vmm create myvm --cpus 4 --memory 2048 --kernel kernel-5.10
```

### Viewing Current Defaults

Use `vmm config show` to see current defaults and their source:

```bash
vmm config show
# Output shows each setting and whether it comes from config or built-in default
```

**Note**: The `vm_defaults` section is optional. Existing configs without it will continue to work unchanged, using the built-in defaults.

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

IP addresses are allocated sequentially from 172.16.0.2 when a VM is started (not when created). The IP is configured via kernel command line parameters, so VMs get network connectivity immediately on boot.

## Directory Structure

```
/var/lib/vmm/
├── config/           # Global configuration
├── vms/              # VM configurations and rootfs
├── images/
│   ├── kernels/      # Linux kernel images
│   └── rootfs/       # Root filesystem images
├── mounts/           # Mount images (ext4 images from host directories)
├── sockets/          # Firecracker API sockets
├── logs/             # VM logs
└── state/            # Runtime state
```

## Auto-Start on Boot

To enable VMs to automatically start after a host reboot, first install the systemd service:

```bash
# Install the systemd service (if not already installed)
sudo ./scripts/install-service.sh

# Enable the service
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

## DNS Configuration

VMM automatically configures DNS in VMs at startup. By default, VMs use public DNS servers:
- 8.8.8.8 (Google)
- 8.8.4.4 (Google)
- 1.1.1.1 (Cloudflare)

To use custom DNS servers, specify them when creating the VM:

```bash
# Use Quad9 and Cloudflare DNS
sudo vmm create myvm --dns 9.9.9.9 --dns 1.0.0.1

# Use corporate DNS
sudo vmm create myvm --dns 10.0.0.53 --dns 10.0.0.54
```

DNS configuration is written to `/etc/resolv.conf` in the VM's rootfs each time the VM starts.

## Host Directory Mounting

VMM can mount host directories inside VMs, making them accessible as block devices. This is useful for sharing code, data, or configuration between the host and VMs.

### How It Works

Since Firecracker doesn't support virtio-fs, VMM uses a block device approach:

1. At VM start, an ext4 image is created from each host directory
2. The image is attached as an additional block device (`/dev/vdb`, `/dev/vdc`, etc.)
3. Fstab entries are injected into the VM rootfs for auto-mounting
4. The VM boots with mounts available at `/mnt/<tag>`

### Creating a VM with Mounts

```bash
# Single mount (read-write by default)
sudo vmm create myvm --mount /home/user/code:code --ssh-key ~/.ssh/id_ed25519.pub

# Multiple mounts with different modes
sudo vmm create myvm \
  --mount /home/user/code:code:ro \
  --mount /home/user/output:output:rw \
  --ssh-key ~/.ssh/id_ed25519.pub

# Start the VM
sudo vmm start myvm
```

The mount format is: `/host/path:tag[:ro|rw]`
- `/host/path` - Absolute path to the directory on the host
- `tag` - Name for the mount (alphanumeric, dashes, underscores only)
- `ro|rw` - Optional mode, defaults to `rw` (read-write)

### Accessing Mounts in the VM

After the VM starts, mounts are available at `/mnt/<tag>`:

```bash
# SSH into the VM
vmm ssh myvm

# Inside the VM:
ls /mnt/code      # Your mounted directory
cat /mnt/code/README.md
```

### Syncing Mount Contents

If you make changes to the host directory while the VM is stopped, the changes will be included when you start the VM (the mount image is recreated from the host directory at each start).

To explicitly sync a mount image:

```bash
# Stop the VM first
sudo vmm stop myvm

# Sync the mount
sudo vmm mount sync myvm code

# Start the VM
sudo vmm start myvm
```

### Listing Mounts

```bash
vmm mount list myvm
# Output:
# Mounts for VM 'myvm':
#   code: /home/user/code -> /mnt/code (ro) [/dev/vdb]
#   output: /home/user/output -> /mnt/output (rw) [/dev/vdc]
```

### Limitations

- Mount images are snapshots - changes inside the VM are not reflected back to the host
- The VM must be stopped to sync mount contents from the host
- Mount tags must be unique within a VM

## Custom Docker Images

VMM can import Docker images as VM root filesystems. This allows you to use your existing Docker images as the base for VMs.

### Importing an Image

```bash
# Import Ubuntu 22.04 as a base image
sudo vmm image import ubuntu:22.04 --name ubuntu-base

# Import with a larger size (default is 2GB)
sudo vmm image import ubuntu:22.04 --name ubuntu-large --size 4096

# Import a custom image from a registry
sudo vmm image import myregistry/myapp:latest --name myapp
```

The import process:
1. Exports the Docker container filesystem
2. Installs systemd, openssh-server, and networking tools
3. Configures the image for Firecracker (serial console, SSH, networking)
4. Creates an ext4 filesystem image

### Using Custom Images

```bash
# Create a VM using the imported image
sudo vmm create myvm --image ubuntu-base --ssh-key ~/.ssh/id_ed25519.pub

# Start the VM
sudo vmm start myvm
```

### Requirements

- Docker must be installed and accessible
- Only Debian/Ubuntu-based images are currently supported
- The import process requires root privileges

### Managing Images

```bash
# List all available images
vmm image list

# Delete an imported image
sudo vmm image delete ubuntu-base
```

## Custom Kernels

VMM supports custom Linux kernels, allowing you to run newer kernel versions or kernels with specific configurations in your VMs.

### Listing Available Kernels

```bash
vmm kernel list
# Output:
# Available kernels:
#   - kernel-6.1 (68.15 MB)
#   - vmlinux.bin (default) (20.45 MB)
```

### Importing a Pre-built Kernel

If you have a pre-built vmlinux binary (uncompressed kernel), you can import it directly:

```bash
# Import a kernel binary
sudo vmm kernel import /path/to/vmlinux --name my-kernel

# Force overwrite an existing kernel
sudo vmm kernel import /path/to/vmlinux --name my-kernel --force
```

The kernel must be:
- An uncompressed vmlinux ELF binary (not bzImage or zImage)
- Built for the same architecture as the host (x86_64 or aarch64)
- Configured with Firecracker-compatible options (virtio, serial console, etc.)

### Building a Kernel from Source

VMM includes a build script that compiles Firecracker-compatible kernels from source:

```bash
# Build a 6.1 LTS kernel
sudo vmm kernel build --version 6.1 --name kernel-6.1

# Supported versions: 5.10, 6.1, 6.6 (all LTS kernels)
```

#### Build Requirements

The build script requires these packages:

```bash
sudo apt-get install build-essential flex bison bc libelf-dev libssl-dev wget
```

#### What the Build Script Does

1. Downloads kernel source from kernel.org
2. Downloads Firecracker's recommended kernel configuration
3. Builds an uncompressed vmlinux binary with all drivers built-in
4. Installs the kernel to `/var/lib/vmm/images/kernels/<name>`

Build time is typically 5-15 minutes depending on your system.

### Using a Custom Kernel

```bash
# Create a VM with a custom kernel
sudo vmm create myvm --kernel kernel-6.1 --ssh-key ~/.ssh/id_ed25519.pub

# Start the VM
sudo vmm start myvm

# Verify the kernel version
vmm ssh myvm -- uname -r
# Output: 6.1.119
```

### Deleting a Kernel

```bash
# Delete a custom kernel
sudo vmm kernel delete kernel-6.1
```

**Note**: You cannot delete the default kernel (`vmlinux.bin`). If VMs are configured to use a kernel you're deleting, they will fail to start until reconfigured.

### When to Use Custom Kernels

- **Newer kernel features**: Run a different kernel version than the default (6.1 LTS)
- **Security patches**: Use a specific LTS kernel with security fixes
- **Custom configurations**: Build kernels with specific options enabled
- **Testing**: Test your application against different kernel versions

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

### VM can't reach the internet

Verify the `host_interface` in your config matches your actual network interface:
```bash
# Find your network interface
ip route | grep default
# Example output: default via 192.168.1.1 dev wlp3s0

# Check your config
cat ~/.config/vmm/config.json

# Update host_interface if needed (e.g., change "eth0" to "wlp3s0")
```

After updating the config, restart your VM for the NAT rules to be recreated with the correct interface.

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
cd baremetalvmm

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
│   ├── image/                # Kernel/rootfs management
│   └── mount/                # Host directory mount management
├── scripts/
│   ├── install.sh            # Installation script
│   ├── uninstall.sh          # Uninstallation script
│   ├── install-service.sh    # Systemd service installation (optional)
│   ├── build-kernel.sh       # Custom kernel build script
│   ├── build-rootfs.sh       # Custom rootfs build script
│   └── vmm.service           # Systemd service unit file
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
