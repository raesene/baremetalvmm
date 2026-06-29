# Images and Kernels

## Available Kernels and Root Filesystems

VMM ships with two kernels and two root filesystems, each designed for a specific use case. The `vmm image list` command shows all available options with descriptions:

```
$ vmm image list
Kernels:
  - k8s-kernel             32.4 MB  Kubernetes cluster kernel (Linux 6.6 LTS, Cilium/BPF)
  - security-kernel        85.2 MB  Security testing kernel (Linux 6.12 LTS, broad module coverage)
  - vmlinux.bin            72.7 MB  General-purpose VM kernel (Linux 6.1 LTS) (default)

Root filesystems:
  - k8s-1.36.0           2048.0 MB  Kubernetes image (kubeadm/containerd pre-installed)
  - rootfs                512.0 MB  Ubuntu 24.04 base image for general-purpose VMs (default)
```

### Which to Use

| Use case | Kernel | Rootfs | Command |
|----------|--------|--------|---------|
| Standalone VMs | `vmlinux.bin` (default) | `rootfs` (default) | `sudo vmm create myvm` |
| Kubernetes clusters | `k8s-kernel` | `k8s-<version>` (auto-detected) | `sudo vmm cluster create mycluster` |
| Security/vuln testing | `security-kernel` | `rootfs` (default) or `k8s-<version>` | `sudo vmm create testvm --kernel security-kernel` |
| Memory bug detection | `kasan-kernel` | `rootfs` (default) | `sudo vmm create testvm --kernel kasan-kernel --memory 1024` |

### Naming Convention

Kernels and rootfs images follow a prefix-based naming convention so that `vmm image list` and the web UI can automatically show descriptions:

| Prefix | Kernel meaning | Rootfs meaning |
|--------|---------------|----------------|
| *(default)* | General-purpose (Linux 6.1 LTS, all built-in) | Ubuntu 24.04 base (systemd, SSH, networking) |
| `k8s-` | Kubernetes/Cilium (Linux 6.6 LTS, BPF JIT, VXLAN, modules) | Kubernetes (kubeadm/containerd pre-installed) |
| `security-` | Security testing (Linux 6.12 LTS, broad module coverage) | *(not yet used)* |
| `kasan-` | KASAN security kernel (memory sanitizer + broad modules) | *(not yet used)* |
| `debug-` | Debug kernel (extra logging and debug options) | *(not yet used)* |
| `minimal-` | Minimal kernel (reduced feature set) | Minimal image (reduced package set) |

When adding new kernel or rootfs variants, use an appropriate prefix so that the description auto-populates. Custom user-imported images without a recognized prefix show as "Custom kernel" or "Custom image".

## Discovering and Downloading Remote Images

Use `--remote` on the list commands to see what kernels and rootfs images are available on GitHub releases, along with whether each is already downloaded locally:

```bash
$ vmm kernel list --remote
Querying GitHub releases...
Available kernels on GitHub:
  ✓ security-kernel       Security testing kernel (security-kernel-6.6.143, broad module coverage)
  ✓ vmlinux.bin           General-purpose VM kernel (kernel-6.1.176)
    kasan-kernel          KASAN security kernel (kasan-kernel-6.6.143, memory sanitizer)
  ✓ k8s-kernel            Kubernetes cluster kernel (k8s-kernel-6.6.143, Cilium/BPF)

  ✓ = already downloaded

$ vmm image list --remote
Querying GitHub releases...
Available rootfs images on GitHub:
  ✓ rootfs                Ubuntu 24.04 base rootfs (rootfs-24.04-20260629)
  ✓ k8s-1.36.2            Kubernetes rootfs (k8s-rootfs-1.36.2, kubeadm/containerd)
    k8s-1.35.6            Kubernetes rootfs (k8s-rootfs-1.35.6, kubeadm/containerd)

  ✓ = already downloaded
```

For each series (kernel, k8s-kernel, security-kernel, etc.) only the latest release is shown. For k8s-rootfs, the latest patch version of each Kubernetes minor release is shown.

Download a specific kernel or image by name:

```bash
# Download a kernel
sudo vmm kernel pull kasan-kernel

# Download a rootfs image
sudo vmm image pull k8s-1.35.6

# Overwrite an existing local copy
sudo vmm kernel pull security-kernel --force
```

Without arguments, `vmm image pull` downloads the default kernel and rootfs if they are not already present (backward-compatible behavior).

## Custom Rootfs from Docker

VMM can import Docker images as VM root filesystems. The import process exports the container filesystem, installs systemd/openssh-server/networking tools, configures it for Firecracker, and creates an ext4 filesystem image.

```bash
# Import Ubuntu 22.04 as a base image
sudo vmm image import ubuntu:22.04 --name ubuntu-base

# Import with a larger size (default is 2GB)
sudo vmm image import ubuntu:22.04 --name ubuntu-large --size 4096

# Import a custom image from a registry
sudo vmm image import myregistry/myapp:latest --name myapp
```

### Using Custom Images

```bash
sudo vmm create myvm --image ubuntu-base --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start myvm
```

### Requirements

- Docker must be installed and accessible
- Only Debian/Ubuntu-based images are currently supported
- The import process requires root privileges

## VM Rootfs Snapshots

You can snapshot a VM's root filesystem and save it as a reusable base image. This is useful for installing tools and configuring a VM once, then creating multiple VMs from that template.

### Creating a Snapshot

The VM must be stopped before snapshotting. The snapshot is automatically shrunk to minimum size to save disk space.

```bash
# Set up a template VM
sudo vmm create template --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start template

# SSH in and install your tools
vmm ssh template
# root@template:~# apt-get install -y python3 nodejs git
# root@template:~# exit

# Stop the VM and snapshot it
sudo vmm stop template
sudo vmm image snapshot template --name dev-tools
```

### Using a Snapshot

```bash
# Create new VMs from the snapshot
sudo vmm create dev1 --image dev-tools --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm create dev2 --image dev-tools --ssh-key ~/.ssh/id_ed25519.pub

# Each VM gets its own copy of the rootfs, resized to the configured disk size
sudo vmm start dev1
sudo vmm start dev2
```

### How It Works

1. Copies the VM's rootfs (ext4 image) to the shared images directory
2. Runs `e2fsck` to verify filesystem consistency
3. Runs `resize2fs -M` to shrink the filesystem to minimum size
4. Truncates the file to match (e.g., a 1024 MB rootfs might shrink to ~160 MB)
5. When a new VM is created from the snapshot, the image is copied and resized back to the VM's `--disk` size

Only the rootfs is captured -- mounts and kernel selection are not included. SSH keys and DNS config are re-injected at VM start time, so the new VM gets its own configuration.

## Custom Kernels

### Importing a Pre-built Kernel

If you have a pre-built vmlinux binary (uncompressed kernel), you can import it directly:

```bash
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
# Build a 6.1 LTS kernel (default profile)
sudo vmm kernel build --version 6.1 --name kernel-6.1

# Supported versions: 5.10, 6.1, 6.6, 6.12
```

For the security testing profile with broad subsystem coverage, or the KASAN profile for memory corruption detection, use the build script directly:

```bash
# Security kernel — broad module coverage for exploit testing
sudo bash scripts/build-kernel.sh --version 6.12 --name security-kernel --config-profile security

# KASAN kernel — security modules + Kernel Address Sanitizer
sudo bash scripts/build-kernel.sh --version 6.12 --name kasan-kernel --config-profile security-kasan
```

The KASAN kernel includes all security kernel subsystems plus `CONFIG_KASAN` (generic mode), `CONFIG_KASAN_STACK`, and `CONFIG_KASAN_VMALLOC`. It detects use-after-free, out-of-bounds, and double-free bugs, reporting them to the serial console (captured by `vmm console`). KASAN roughly doubles kernel memory usage — use `--memory 1024` or higher when creating VMs. See [security-testing.md](security-testing.md#kasan-security-kernel) for details.

#### Build Requirements

```bash
sudo apt-get install build-essential flex bison bc libelf-dev libssl-dev wget
```

The build script downloads kernel source from kernel.org, applies Firecracker's recommended config, and builds an uncompressed vmlinux binary with all drivers built-in. Build time is typically 5-15 minutes.

### Using a Custom Kernel

```bash
sudo vmm create myvm --kernel kernel-6.1 --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start myvm
vmm ssh myvm -- uname -r
```

### Deleting a Kernel

```bash
sudo vmm kernel delete kernel-6.1
```

You cannot delete the default kernel (`vmlinux.bin`). If VMs are configured to use a kernel you're deleting, they will fail to start until reconfigured.
