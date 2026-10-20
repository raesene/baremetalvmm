---
name: vmm-usage
description: >
  Create, manage, and interact with Firecracker microVMs and Kubernetes clusters using the vmm CLI tool.
  Use when an agent needs to: spin up a Linux VM for testing or development, create a Kubernetes cluster
  from microVMs, snapshot a VM's rootfs for reuse, manage VM lifecycle (create/start/stop/delete),
  SSH into a running VM, work with custom kernels, or create a nested VM that can run its own VMs
  (Cloud Hypervisor backend, --nested). Triggers on tasks involving virtual machines, test environments,
  isolated Linux environments, Kubernetes cluster creation, nested virtualization, /dev/kvm inside a VM,
  KubeVirt/Kata/hypervisor testing, or VM-based infrastructure.
  Assumes vmm is already installed and configured on the host.
---

# vmm — Bare Metal MicroVM Manager

vmm manages Firecracker microVMs on Linux. VMs that need to run VMs of their own (nested virtualization) run under Cloud Hypervisor instead; see [Nested VMs](#nested-vms-vms-inside-vms). Run every vmm command with `sudo`, including read-only ones like `list`: the data directory (`/var/lib/vmm`) is root-only, so without sudo they fail with "permission denied".

## Quick Reference

```
sudo vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--kernel NAME] [--image NAME] [--dns SERVER] [--mount PATH:TAG[:ro|rw]] [--nested] [--backend firecracker|cloud-hypervisor]
sudo vmm start <name>
sudo vmm stop <name>
sudo vmm delete <name> [-f]
sudo vmm list [-a]
sudo vmm ssh <name> [-u user]
```

## VM Lifecycle

### Create and start a VM

```bash
sudo vmm create myvm --cpus 2 --memory 1024
sudo vmm start myvm
```

Defaults: 1 CPU, 512 MB RAM, 1024 MB disk. `--ssh-key` is optional — VMM automatically generates and manages an Ed25519 key pair (`/var/lib/vmm/ssh/vmm_ed25519`) that is injected into every VM. If you pass `--ssh-key`, your key is added alongside the managed key.

Wait ~5 seconds after start before SSH or network access. The VM needs time to boot and configure networking.

### SSH into a VM

```bash
sudo vmm ssh myvm
```

SSH connects as root using the VMM-managed key. Use `-u <user>` for a different user. If you provided a custom `--ssh-key` at create time, the managed key is still tried first.

### Stop and delete

```bash
sudo vmm stop myvm
sudo vmm delete myvm
# Or force-delete (stops if running, no confirmation):
sudo vmm delete myvm -f
```

### List VMs

```bash
sudo vmm list        # all VMs, including stopped ones (-a is on by default)
```

## Networking

VMs get IPs from 172.16.0.0/16, allocated sequentially from 172.16.0.2. The host can reach VMs directly by IP. VMs have outbound internet via NAT.

To find a VM's IP, use `sudo vmm list` — the IP column shows each VM's address. The columns are `NAME ID STATE CPUs MEMORY IP BACKEND`, and MEMORY prints as two words (`512 MB`), so with awk the IP is field 7: `sudo vmm list | awk '$1=="myvm" {print $7}'`.

Additional networking features exist but are not covered in detail here:
- **Port forwarding**: `sudo vmm port-forward add <name> <host-port>:<guest-port>`
- **List port forwards**: `sudo vmm port-forward list <name>`
- **Remove port forward**: `sudo vmm port-forward remove <name> <host-port>:<guest-port>`
- **Custom DNS**: `--dns <server>` flag on create (repeatable)
- **Host directory mounts**: `--mount /host/path:tag[:ro|rw]` flag on create
- **Internet-only VM for untrusted code**: `--restrict-local-networks` on create (or `sudo vmm set <name> restrict-local-networks on|off`, live). The VM can't open connections to the host, private/LAN ranges or other VMs; `vmm ssh`/`cp` and port forwards still work. Its DNS servers must be public. Not for clusters

## Images and Kernels

### Pull default images

```bash
sudo vmm image pull    # downloads default kernel + rootfs
```

This downloads `vmlinux.bin` (Linux 6.1 LTS kernel) and `rootfs.ext4` (Ubuntu 24.04) from GitHub releases.

### List available images and kernels

```bash
sudo vmm image list
sudo vmm kernel list
```

### Kernel variants

| Name | Series | Use case |
|------|--------|----------|
| `vmlinux.bin` (default) | 6.1 LTS | General-purpose VMs |
| `k8s-kernel` | 6.6 LTS | Kubernetes clusters (Cilium/BPF support) |
| `security-kernel` | 6.12 LTS | Security testing (broad module coverage) |
| `kasan-kernel` | 6.12 LTS | KASAN memory sanitizer (detects use-after-free, OOB, etc.) |
| `nested-kernel` | 6.1 LTS | Nested VMs: default kernel + KVM, auto-selected by `--nested` |

Use `--kernel <name>` on create to select a non-default kernel.

### Rootfs variants

| Name | Description |
|------|-------------|
| `rootfs.ext4` (default) | Ubuntu 24.04, general-purpose |
| `k8s-<version>.ext4` | Ubuntu 24.04 with kubeadm/containerd pre-installed |
| `security-<name>.ext4` | Security testing image (container/K8s security tools) |

Use `--image <name>` on create to select a non-default rootfs (omit the `.ext4` extension).

### Build a custom kernel from source

```bash
sudo vmm kernel build --version 6.6 --name my-custom-kernel
```

This downloads and compiles a kernel with the specified version. Use `--name` to set the name it will be stored under, and `--profile` to pick a config profile (`default`, `k8s`, `nested`, `security`, `security-kasan`). The built kernel appears in `sudo vmm kernel list`. Prefer `sudo vmm kernel pull <name>` when a pre-built release exists — building takes several minutes.

### Import a kernel with force overwrite

```bash
sudo vmm kernel import /path/to/vmlinux --name my-kernel --force
```

The `--force`/`-f` flag overwrites an existing kernel with the same name.

### Import a Docker image as rootfs

```bash
sudo vmm image import myimage:latest --name my-rootfs --size 4096
```

The `--size` flag sets the image size in MB (default: 2048).

### Create a reusable image via snapshot

Install software in a VM, then snapshot it as a reusable base image:

```bash
sudo vmm create base --cpus 2 --memory 1024 --disk 4096
sudo vmm start base
sudo vmm ssh base
# ... install packages, configure the VM ...
# exit SSH, then:
sudo vmm stop base
sudo vmm image snapshot base --name my-template
```

Now create new VMs from the snapshot:

```bash
sudo vmm create worker1 --image my-template
```

The snapshot shrinks the rootfs to minimum size automatically.

## Nested VMs (VMs inside VMs)

Use a nested VM when the guest itself needs `/dev/kvm`: running Firecracker/QEMU/Cloud Hypervisor inside a VM, Kata Containers, KubeVirt, hypervisor or KVM testing, or vmm itself. Firecracker can't expose VT-x/AMD-V to guests, so `--nested` runs the VM under **Cloud Hypervisor**. Firecracker VMs and Cloud Hypervisor VMs run side by side on the same bridge.

### Check prerequisites first

```bash
cat /sys/module/kvm_intel/parameters/nested /sys/module/kvm_amd/parameters/nested 2>/dev/null   # want Y or 1
command -v cloud-hypervisor            # installed by: install.sh --with-cloud-hypervisor, or sudo vmm upgrade --with-cloud-hypervisor
sudo vmm kernel list | grep nested-kernel   # if missing: sudo vmm kernel pull nested-kernel
```

`vmm create --nested` checks all three and fails with a specific message if one is missing, so you can also just try it and act on the error. Enabling nested KVM on the host or installing Cloud Hypervisor changes the host — ask the user before doing either.

### Create and use a nested VM

```bash
sudo vmm create lab --nested --cpus 2 --memory 2048 --disk 8192
sudo vmm start lab
sleep 8
sudo vmm ssh lab -- 'ls -l /dev/kvm'
```

- `--nested` implies `--backend cloud-hypervisor` and the `nested-kernel` kernel (unless `--kernel` is given; a kernel without KVM gives a guest with no `/dev/kvm`). `--backend firecracker --nested` is an error.
- Give it 2 GB+ memory so it has room for its own guests. Nested guests are noticeably slower.
- `sudo vmm list` shows `cloud-hypervisor+nested` in the BACKEND column.
- SSH, `vmm cp`, `sudo vmm console`, port forwards, `--mount`, `--dns` and autostart all work as for Firecracker VMs.
- To run vmm *inside* a nested VM, set a different `subnet`/`gateway` (e.g. `172.17.0.0/16` / `172.17.0.1`) in the inner `~/.config/vmm/config.json` first — the default 172.16.0.0/16 clashes with the nested VM's own network.
- `sudo vmm create plain --backend cloud-hypervisor` runs an ordinary (non-nested) VM under Cloud Hypervisor.
- Whole clusters can be nested too (e.g. for Kata Containers): `sudo vmm cluster create lab --nested --workers 1 --cpus 4 --memory 8192`. See [references/kubernetes.md](references/kubernetes.md#nested-clusters-kata-containers).

### Differences from Firecracker VMs

- **No memory snapshots**: `vmm snapshot create` is refused for Cloud Hypervisor VMs. `vmm image snapshot` (rootfs only) still works.
- **Stop**: a clean ACPI power-off, usually under a second with `nested-kernel` (5-7s for a Kubernetes node). With kernels lacking the ACPI button driver the guest ignores the power button and is stopped with SIGTERM after 30 seconds.
- **Logs**: `/var/lib/vmm/logs/<name>.log` holds Cloud Hypervisor's own output (check it if start fails); the serial console is in `sudo vmm console <name> --full` as usual.
- The backend is fixed at create time.

## Kubernetes Clusters

For cluster details and workflows, see [references/kubernetes.md](references/kubernetes.md).

Quick start:

```bash
# Single-node cluster (control plane only)
sudo vmm cluster create mycluster

# Multi-node cluster
sudo vmm cluster create mycluster --workers 2

# Custom resources and K8s version
sudo vmm cluster create mycluster --workers 2 --cpus 4 --memory 8192 --k8s-version 1.36   # newest 1.36 patch; or exact, e.g. 1.36.5

# Use the cluster
kubectl --context vmm-mycluster get nodes

# Stop and start (workers stop first; start waits for every node to report Ready)
sudo vmm cluster stop mycluster
sudo vmm cluster start mycluster

# Delete
sudo vmm cluster delete mycluster -f
```

`--ssh-key` is optional for clusters too — the VMM-managed key is used for cluster provisioning (kubeadm over SSH) by default. A public key passed with `--ssh-key` is added to the nodes; its private half provisions the cluster only if it sits next to the `.pub` file. If `k8s-kernel` is available locally, it is auto-selected for clusters.

## Common Patterns for Agents

### Spin up a throwaway test VM

```bash
sudo vmm create test-env --cpus 2 --memory 2048 --disk 4096
sudo vmm start test-env
sleep 5
sudo vmm ssh test-env -- 'apt-get update && apt-get install -y <packages>'
# ... do work ...
sudo vmm delete test-env -f
```

### Run a command in a VM non-interactively

```bash
sudo vmm ssh myvm -- 'cat /etc/os-release'
sudo vmm ssh myvm -- 'systemctl status ssh'
```

### Wait for VM to be reachable

```bash
VM_IP=$(sudo vmm list | awk '$1=="myvm" {print $7}')
until ping -c1 -W1 "$VM_IP" &>/dev/null; do sleep 1; done
```

### Create multiple VMs

```bash
for i in 1 2 3; do
  sudo vmm create node-$i --cpus 2 --memory 1024
  sudo vmm start node-$i
done
sleep 5
```

### Spin up a security testing environment

```bash
sudo vmm create sectest --cpus 2 --memory 2048 --disk 4096 --kernel security-kernel --image security
sudo vmm start sectest
sleep 5
sudo vmm ssh sectest
```

The security kernel (6.12 LTS) provides broad module coverage for security testing scenarios. The security rootfs comes pre-loaded with container and Kubernetes security tools.

### Spin up a KASAN memory sanitizer environment

```bash
# KASAN needs extra memory (~2x overhead) — use --memory 1024+
sudo vmm create kasan-test --cpus 2 --memory 2048 --kernel kasan-kernel
sudo vmm start kasan-test
sleep 8

# Monitor for KASAN reports via serial console
sudo vmm console kasan-test

# Or grep for KASAN bugs after running an exploit
sudo vmm console kasan-test --full | grep -A 30 "BUG: KASAN"
```

The KASAN kernel has the same module coverage as the security kernel, plus Kernel Address Sanitizer instrumentation. It detects use-after-free, out-of-bounds access, double-free, and other memory corruption bugs, reporting them to the serial console. Available across all LTS series (5.10–6.18) as `kasan-kernel-<series>`. Build locally with `--config-profile security-kasan`.

### Use a custom SSH key alongside the managed key

```bash
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
```

Your key is added alongside the VMM-managed key. Both can be used to access the VM.

## Important Notes

- All create/start/stop/delete operations require `sudo`.
- VMs are persistent across reboots if the systemd service is installed.
- VM configs are stored in `/var/lib/vmm/vms/<name>.json`.
- Hypervisor logs (Firecracker or Cloud Hypervisor) are in `/var/lib/vmm/logs/<name>.log` — check these if a VM fails to start.
- `sudo vmm console <name>` follows the log until interrupted; use `sudo vmm console <name> --full` in scripts or it will hang.
- VM names must be unique. Attempting to create a duplicate name will fail.
- Disk size is set at creation time and cannot be changed later.
- VMM auto-manages an Ed25519 SSH key pair — `--ssh-key` is optional and adds your key alongside the managed one.
- The `--ssh-key` flag takes a path to a **public** key file (e.g., `~/.ssh/id_ed25519.pub`).
- Create flag defaults can be set in `~/.config/vmm/config.json` under `vm_defaults` (resolution: CLI flag > config default > hardcoded fallback).
