# Kubernetes Cluster Management with vmm

## Overview

`vmm cluster` creates Kubernetes clusters from Firecracker microVMs using kubeadm and Cilium CNI. Each cluster gets a control plane VM and optional worker VMs.

## Cluster Defaults

| Parameter | Default | Flag |
|-----------|---------|------|
| Workers | 0 (control-plane only) | `--workers N` |
| CPUs per node | 2 | `--cpus N` |
| Memory per node | 4096 MB | `--memory MB` |
| Disk per node | 10240 MB | `--disk MB` |
| Kubernetes version | 1.35.3 | `--k8s-version VER` |

## Kernel and Rootfs Requirements

Clusters need the `k8s-kernel` (Linux 6.6 LTS) for Cilium BPF support. If `k8s-kernel` is available locally, it is auto-selected. Otherwise, the default kernel is used — but Cilium may fail without 6.6+ BPF features.

Pre-built K8s rootfs images (`k8s-<version>.ext4`) include containerd and kubeadm pre-installed, cutting provisioning time significantly. If not available locally, vmm attempts to download from GitHub releases. If that fails, packages are installed at runtime over the network (~30-60s extra per node).

## Commands

```bash
# Create a single-node cluster
sudo vmm cluster create dev --ssh-key ~/.ssh/id_ed25519.pub

# Create a 3-node cluster (1 control plane + 2 workers)
sudo vmm cluster create prod --workers 2 --cpus 4 --memory 8192 --ssh-key ~/.ssh/id_ed25519.pub

# Explicitly specify kernel and image
sudo vmm cluster create prod --kernel k8s-kernel --image k8s-1.35.3 --workers 2 --ssh-key ~/.ssh/id_ed25519.pub

# List clusters
vmm cluster list

# Extract kubeconfig (also merges into ~/.kube/config)
vmm cluster kubeconfig mycluster

# Delete cluster and all its VMs
sudo vmm cluster delete mycluster -f
```

## VM Naming Convention

Cluster VMs follow a fixed naming pattern:

- Control plane: `{cluster-name}-control-plane`
- Workers: `{cluster-name}-worker-1`, `{cluster-name}-worker-2`, ...

These VMs appear in `vmm list` alongside standalone VMs.

## Kubeconfig

After creation, the kubeconfig is automatically merged into `~/.kube/config` as context `vmm-{cluster-name}`.

```bash
kubectl --context vmm-mycluster get nodes
kubectl --context vmm-mycluster get pods -A
```

## Provisioning Sequence

1. Create VMs (control plane + workers)
2. Start all VMs, wait for SSH connectivity
3. Install containerd on all nodes (skipped if pre-installed rootfs)
4. Install kubeadm/kubelet/kubectl (skipped if pre-installed rootfs)
5. `kubeadm init` on control plane (skips kube-proxy)
6. Install Cilium CNI with kube-proxy replacement
7. Join workers via `kubeadm join`
8. Wait for all nodes to reach Ready state (up to 5 minutes)
9. Extract and merge kubeconfig

## Troubleshooting

- **Cluster creation fails at SSH**: VMs may not have booted yet. Check `vmm list` to verify VMs are running, then check `/var/lib/vmm/logs/<vm-name>.log`.
- **Cilium not starting**: Ensure `k8s-kernel` (6.6 LTS) is being used. The default 6.1 kernel lacks required BPF features.
- **Node not joining**: SSH into the worker VM and check `journalctl -u kubelet` for errors.
- **Cluster stuck in creating state**: If provisioning was interrupted, delete and recreate. Cluster state is in `/var/lib/vmm/clusters/<name>.json`.

## Resource Planning

Each cluster node uses the configured CPU/memory/disk. A 3-node cluster with defaults consumes:
- 6 vCPUs (2 per node)
- 12 GB RAM (4 GB per node)
- 30 GB disk (10 GB per node)

Plan host resources accordingly when creating multiple clusters or combining with standalone VMs.
