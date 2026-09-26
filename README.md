# VMM - Bare Metal MicroVM Manager

**Project site:** https://raesene.github.io/baremetalvmm/

**WARNING** This is a vibe-coded piece of software allow for creation of microVMs conveniently. It has been designed as "Personal Software" which basically means it works for me, but I have no idea how well it'll work in any other environment, caveat user !

The goal of the project is to allow for small development VMs to be spun up based on [firecracker](https://github.com/firecracker-microvm/firecracker), so they're lightweight. It can build VM images from a Docker image, allowing for custom VMs.

The goal of the project is to be useful in cases where you want something like Docker, but want some more isolation that Docker provides, or you want to do lower level tasks in the VM that don't suit Docker well. N.B we're not there yet!

Pretty much all of the coding has been done with [Claude code](https://github.com/anthropics/claude-code).

## Requirements

- Ubuntu 24.04 (or compatible Linux distribution). All testing has been done on Ubuntu 24.04, so it's likely only to work with that distro.
- An x86_64 (amd64) host. Pre-built binaries, kernels and rootfs images are published for amd64 only
- KVM support (`/dev/kvm` must be accessible)
- Root access (for networking setup)
- Go 1.25+ (only if building from source)

## Quick Start

### Installation

No clone needed:

```bash
curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash

# Or also install and start the systemd services (VM auto-start + web console)
curl -fsSL https://raesene.github.io/baremetalvmm/install.sh | sudo bash -s -- --with-services
```

The installer:
- Downloads the latest `vmm` and `vmm-web` release (linux/amd64) and verifies it against the release's `checksums.txt`
- Installs the binaries to `/usr/local/bin` and helper scripts to `/usr/local/share/vmm`
- Installs Firecracker v1.16.0 (verified against its published SHA256)
- Downloads the default kernel, Kubernetes and security kernels, and an Ubuntu 24.04 rootfs
- With `--with-services`: installs `vmm.service` and `vmm-web.service`, generates a random web console password in `/etc/vmm-web/environment` and starts the console on `127.0.0.1:8080`

Other options: `--version X` to pin a release, `--no-images` to skip image downloads, and `--build-from-source` (run from a checkout as `sudo ./scripts/install.sh --build-from-source`). Re-running the installer is safe.

### Upgrading

```bash
vmm upgrade --check      # is a newer release available?
sudo vmm upgrade         # upgrade vmm, vmm-web (and Firecracker if the release needs it)
sudo vmm upgrade --rollback   # restore the previous binaries
```

Upgrades are verified against the release checksums and swap the binaries in place, so **you don't need to stop anything first**: running VMs keep running and only `vmm-web.service` is restarted. Don't stop `vmm.service` to upgrade, because stopping it stops every running VM. The web console shows when a new release is available. (`vmm upgrade` is available from v0.14.0; to upgrade an older install, re-run the installer.)

### Uninstallation

```bash
sudo /usr/local/share/vmm/uninstall.sh
```

Use `--yes` or `-y` to skip the confirmation prompt. The script is idempotent and safe to run multiple times.

### One-time Setup (optional)

The installer already downloads the default images. To write a config file or keep VM data somewhere else:

```bash
# Initialize config
vmm config init

# (Optional) store VM data somewhere other than the default /var/lib/vmm
sudo vmm config init --data-dir /srv/vmm-data

# Pull the default kernel and rootfs images (if you used --no-images or changed data_dir)
sudo vmm image pull
```

The data directory (default `/var/lib/vmm`) can be changed later with
`sudo vmm config set data_dir <path>`. See [docs/configuration.md](docs/configuration.md#data-directory)
for details. Changing it does not move existing data.

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
