---
name: vmm-usage
description: >
  Create, manage, and interact with Firecracker microVMs and Kubernetes clusters using the vmm CLI tool.
  Use when an agent needs to: spin up a Linux VM for testing or development, create a Kubernetes cluster
  from microVMs, snapshot a VM's rootfs for reuse, manage VM lifecycle (create/start/stop/delete),
  SSH into a running VM, or work with custom kernels. Triggers on tasks involving virtual machines,
  test environments, isolated Linux environments, Kubernetes cluster creation, or VM-based infrastructure.
  Assumes vmm is already installed and configured on the host.
---

# vmm — Bare Metal MicroVM Manager

vmm manages Firecracker microVMs on Linux. All VM operations require `sudo`. Read-only commands (`list`, `image list`, `kernel list`) work without sudo.

## Quick Reference

```
sudo vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--kernel NAME] [--image NAME] [--dns SERVER] [--mount PATH:TAG[:ro|rw]]
sudo vmm start <name>
sudo vmm stop <name>
sudo vmm delete <name> [-f]
vmm list [-a]
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
vmm list        # running VMs
vmm list -a     # all VMs including stopped
```

## Networking

VMs get IPs from 172.16.0.0/16, allocated sequentially from 172.16.0.2. The host can reach VMs directly by IP. VMs have outbound internet via NAT.

To find a VM's IP, use `vmm list` — the IP column shows each VM's address.

Additional networking features exist but are not covered in detail here:
- **Port forwarding**: `sudo vmm port-forward add <name> <host-port>:<guest-port>`
- **List port forwards**: `vmm port-forward list <name>`
- **Remove port forward**: `sudo vmm port-forward remove <name> <host-port>:<guest-port>`
- **Custom DNS**: `--dns <server>` flag on create (repeatable)
- **Host directory mounts**: `--mount /host/path:tag[:ro|rw]` flag on create

## Images and Kernels

### Pull default images

```bash
sudo vmm image pull    # downloads default kernel + rootfs
```

This downloads `vmlinux.bin` (Linux 6.1 LTS kernel) and `rootfs.ext4` (Ubuntu 24.04) from GitHub releases.

### List available images and kernels

```bash
vmm image list
vmm kernel list
```

### Kernel variants

| Name | Series | Use case |
|------|--------|----------|
| `vmlinux.bin` (default) | 6.1 LTS | General-purpose VMs |
| `k8s-kernel` | 6.6 LTS | Kubernetes clusters (Cilium/BPF support) |
| `security-kernel` | 6.12 LTS | Security testing (broad module coverage) |

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

This downloads and compiles a kernel with the specified version. Use `--name` to set the name it will be stored under. The built kernel appears in `vmm kernel list`.

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

## Kubernetes Clusters

For cluster details and workflows, see [references/kubernetes.md](references/kubernetes.md).

Quick start:

```bash
# Single-node cluster (control plane only)
sudo vmm cluster create mycluster

# Multi-node cluster
sudo vmm cluster create mycluster --workers 2

# Custom resources and K8s version
sudo vmm cluster create mycluster --workers 2 --cpus 4 --memory 8192 --k8s-version 1.36.0

# Use the cluster
kubectl --context vmm-mycluster get nodes

# Delete
sudo vmm cluster delete mycluster -f
```

`--ssh-key` is optional for clusters too — the VMM-managed key is used for cluster provisioning (kubeadm over SSH) by default. If `k8s-kernel` is available locally, it is auto-selected for clusters.

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
VM_IP=$(vmm list | grep myvm | awk '{print $3}')
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

### Use a custom SSH key alongside the managed key

```bash
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
```

Your key is added alongside the VMM-managed key. Both can be used to access the VM.

## Important Notes

- All create/start/stop/delete operations require `sudo`.
- VMs are persistent across reboots if the systemd service is installed.
- VM configs are stored in `/var/lib/vmm/vms/<name>.json`.
- Firecracker logs are in `/var/lib/vmm/logs/<name>.log` — check these if a VM fails to start.
- VM names must be unique. Attempting to create a duplicate name will fail.
- Disk size is set at creation time and cannot be changed later.
- VMM auto-manages an Ed25519 SSH key pair — `--ssh-key` is optional and adds your key alongside the managed one.
- The `--ssh-key` flag takes a path to a **public** key file (e.g., `~/.ssh/id_ed25519.pub`).
- Create flag defaults can be set in `~/.config/vmm/config.json` under `vm_defaults` (resolution: CLI flag > config default > hardcoded fallback).
