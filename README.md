# VMM - Bare Metal MicroVM Manager

**WARNING** This is a vibe-coded piece of software allow for creation of microVMs conveniently. It has been designed as "Personal Software" which basically means it works for me, but I have no idea how well it'll work in any other environment, caveat user !

The goal of the project is to allow for small development VMs to be spun up based on [firecracker](https://github.com/firecracker-microvm/firecracker), so they're lightweight. It can build VM images from a Docker image, allowing for custom VMs.

The goal of the project is to be useful in cases where you want something like Docker, but want some more isolation that Docker provides, or you want to do lower level tasks in the VM that don't suit Docker well. N.B we're not there yet!

Pretty much all of the coding has been done with [Claude code](https://github.com/anthropics/claude-code).

## Requirements

- Ubuntu 24.04 (or compatible Linux distribution). All testing has been done on Ubuntu 24.04, so it's likely only to work with that distro.
- KVM support (`/dev/kvm` must be accessible)
- Root access (for networking setup)
- Go 1.25+ (only if building from source)

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
- Download the pre-built `vmm` and `vmm-web` binaries from GitHub releases (amd64/arm64)
- Fall back to building from source if download fails
- Install the binaries to `/usr/local/bin`
- Download Firecracker v1.16.0
- Download pre-built kernels and an Ubuntu 24.04 rootfs from GitHub releases
- Create data directories in `/var/lib/vmm`

### Uninstallation

```bash
sudo ./scripts/uninstall.sh
```

Use `--yes` or `-y` to skip the confirmation prompt. The script is idempotent and safe to run multiple times.

### One-time Setup

```bash
# Initialize config
vmm config init

# Pull the default kernel and rootfs images
sudo vmm image pull
```

### Basic Usage

```bash
# Create a VM (uses vmm-managed SSH key by default, or pass --ssh-key for your own)
sudo vmm create myvm --cpus 2 --memory 1024

# Start it
sudo vmm start myvm

# SSH in (also works with standard ssh as root@<vm-ip>)
vmm ssh myvm

# Stop and clean up
sudo vmm stop myvm
sudo vmm delete myvm
```

By default VMs are only reachable from the local machine. Use `vmm port-forward` to expose them externally.

## Documentation

| Guide | Description |
|-------|-------------|
| [CLI Command Reference](docs/commands.md) | Full list of commands, flags, and options |
| [Configuration](docs/configuration.md) | Config file, VM defaults, shell completion |
| [Images and Kernels](docs/images-and-kernels.md) | Available images, custom rootfs from Docker, custom kernels, snapshots |
| [Networking and Mounts](docs/networking.md) | Network architecture, port forwarding, DNS, SSH keys, host directory mounts |
| [Kubernetes Clusters](docs/kubernetes.md) | Creating and managing Kubernetes clusters with kubeadm + Cilium |
| [OpenShift Clusters](docs/openshift.md) | Single-node OpenShift-derived clusters via MicroShift |
| [Web UI](docs/web-ui.md) | Browser-based dashboard, web terminal, and JSON API |
| [Security Testing](docs/security-testing.md) | Security kernel for vulnerability research and exploit testing |
| [Development](docs/development.md) | Building from source, project structure, systemd services |
| [Troubleshooting](docs/troubleshooting.md) | Common issues and debugging |

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
