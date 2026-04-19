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
sudo vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--kernel NAME] [--image NAME]
sudo vmm start <name>
sudo vmm stop <name>
sudo vmm delete <name> [-f]
vmm list [-a]
sudo vmm ssh <name> [-u user]
```

## VM Lifecycle

### Create and start a VM

```bash
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start myvm
```

Defaults: 1 CPU, 512 MB RAM, 1024 MB disk. Always pass `--ssh-key` — VMs have no password login.

Wait ~5 seconds after start before SSH or network access. The VM needs time to boot and configure networking.

### SSH into a VM

```bash
sudo vmm ssh myvm
```

SSH connects as root. Use `-u <user>` for a different user. Requires the matching private key for the public key passed at create time.

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
- **Port forwarding**: `sudo vmm port-forward <name> <host-port>:<guest-port>`
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

Use `--kernel <name>` on create to select a non-default kernel.

### Rootfs variants

| Name | Description |
|------|-------------|
| `rootfs.ext4` (default) | Ubuntu 24.04, general-purpose |
| `k8s-<version>.ext4` | Ubuntu 24.04 with kubeadm/containerd pre-installed |

Use `--image <name>` on create to select a non-default rootfs (omit the `.ext4` extension).

### Create a reusable image via snapshot

Install software in a VM, then snapshot it as a reusable base image:

```bash
sudo vmm create base --cpus 2 --memory 1024 --disk 4096 --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start base
sudo vmm ssh base
# ... install packages, configure the VM ...
# exit SSH, then:
sudo vmm stop base
sudo vmm image snapshot base --name my-template
```

Now create new VMs from the snapshot:

```bash
sudo vmm create worker1 --image my-template --ssh-key ~/.ssh/id_ed25519.pub
```

The snapshot shrinks the rootfs to minimum size automatically.

## Kubernetes Clusters

For cluster details and workflows, see [references/kubernetes.md](references/kubernetes.md).

Quick start:

```bash
# Single-node cluster (control plane only)
sudo vmm cluster create mycluster --ssh-key ~/.ssh/id_ed25519.pub

# Multi-node cluster
sudo vmm cluster create mycluster --workers 2 --ssh-key ~/.ssh/id_ed25519.pub

# Use the cluster
kubectl --context vmm-mycluster get nodes

# Delete
sudo vmm cluster delete mycluster -f
```

## Common Patterns for Agents

### Spin up a throwaway test VM

```bash
sudo vmm create test-env --cpus 2 --memory 2048 --disk 4096 --ssh-key ~/.ssh/id_ed25519.pub
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
  sudo vmm create node-$i --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
  sudo vmm start node-$i
done
sleep 5
```

## Important Notes

- All create/start/stop/delete operations require `sudo`.
- VMs are persistent across reboots if the systemd service is installed.
- VM configs are stored in `/var/lib/vmm/vms/<name>.json`.
- Firecracker logs are in `/var/lib/vmm/logs/<name>.log` — check these if a VM fails to start.
- VM names must be unique. Attempting to create a duplicate name will fail.
- Disk size is set at creation time and cannot be changed later.
- The `--ssh-key` flag takes a path to a **public** key file (e.g., `~/.ssh/id_ed25519.pub`).
