# Kubernetes Clusters

VMM can create Kubernetes clusters from multiple Firecracker VMs, similar to [kind](https://kind.sigs.k8s.io/) but with VM-level isolation for each node. Clusters are bootstrapped with kubeadm and use Cilium as the CNI plugin.

## Prerequisites

A Kubernetes-compatible kernel (`k8s-kernel`) is downloaded automatically during installation. This is a 6.6 LTS kernel with BPF JIT, VXLAN, and cgroups v2 bandwidth control enabled for Cilium CNI support. If you don't have it, you can build one manually:

```bash
sudo vmm kernel build --version 6.6 --name k8s-kernel
```

You also need an SSH key for VM access:

```bash
ssh-keygen -t ed25519  # if you don't already have one
```

If your SSH key has a passphrase, make sure it's loaded in ssh-agent and use `sudo -E` to preserve the agent socket:

```bash
eval "$(ssh-agent -s)"
ssh-add ~/.ssh/id_ed25519
sudo -E vmm cluster create ...
```

## Creating a Cluster

```bash
# Single-node cluster (control plane only, like 'kind create cluster')
sudo vmm cluster create mycluster --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel

# Multi-node cluster with 2 workers (3 VMs total)
sudo vmm cluster create mycluster --workers 2 --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel

# With custom resources
sudo vmm cluster create mycluster --workers 2 \
  --cpus 4 --memory 8192 --disk 20480 \
  --ssh-key ~/.ssh/id_ed25519.pub \
  --kernel k8s-kernel \
  --k8s-version 1.30.0
```

The create command:
1. Creates Firecracker VMs (`{name}-control-plane`, `{name}-worker-1`, etc.)
2. Installs containerd, kubeadm, kubelet, and kubectl via SSH
3. Runs `kubeadm init` on the control plane
4. Installs Cilium CNI (with kube-proxy replacement)
5. Joins worker nodes to the cluster
6. Merges kubeconfig into `~/.kube/config` as context `vmm-{name}`

## Using a Cluster

Once created, the cluster is immediately usable via kubectl:

```bash
# Use the cluster context
kubectl --context vmm-mycluster get nodes
kubectl --context vmm-mycluster get pods -n kube-system

# Or set it as the default context
kubectl config use-context vmm-mycluster
kubectl get nodes
```

You can also SSH into individual nodes for debugging:

```bash
vmm ssh mycluster-control-plane
vmm ssh mycluster-worker-1
```

## Create Options

```bash
vmm cluster create <name> [flags]

Flags:
  --workers int        Number of worker nodes (default 0, control-plane only)
  --cpus int           vCPUs per node (default 2)
  --memory int         Memory per node in MB (default 4096)
  --disk int           Disk per node in MB (default 10240)
  --k8s-version string Kubernetes version (default "1.36.0")
  --ssh-key string     Path to SSH public key (required)
  --kernel string      Kernel name (k8s-kernel recommended)
  --image string       Rootfs image name
```

## Deleting a Cluster

```bash
# Delete with confirmation
sudo vmm cluster delete mycluster

# Force delete without confirmation
sudo vmm cluster delete mycluster -f
```

This stops and deletes all VMs in the cluster and removes the kubeconfig context.

## Cluster Defaults

| Setting | Default | Notes |
|---------|---------|-------|
| Workers | 0 | Control plane only (single-node cluster) |
| CPUs | 2 | Minimum 2 required for kubeadm |
| Memory | 4096 MB | Minimum 2048 MB required |
| Disk | 10240 MB (10 GB) | Needs space for container images |
| Kubernetes | 1.36.0 | Any version available from pkgs.k8s.io |
| CNI | Cilium | With kube-proxy replacement enabled |
| Pod CIDR | 10.244.0.0/16 | Doesn't conflict with VM bridge network |
| Service CIDR | 10.96.0.0/12 | Standard Kubernetes default |

## What Gets Installed in Each VM

- **containerd** (from Ubuntu repos) with SystemdCgroup enabled
- **kubeadm**, **kubelet**, **kubectl** (from pkgs.k8s.io)
- **Cilium CLI** (on control plane only)
- Kernel modules and sysctl settings for networking and cgroups
- BPF filesystem mount for Cilium
- Shared mount propagation for Kubernetes volumes
