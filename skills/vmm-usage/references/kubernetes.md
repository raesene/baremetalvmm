# Kubernetes Cluster Management with vmm

## Overview

`vmm cluster` creates Kubernetes clusters from Firecracker microVMs using kubeadm with Cilium (default) or Calico CNI. `--type openshift` builds a single-node MicroShift cluster instead. Each cluster gets a control plane VM and optional worker VMs.

## Cluster Defaults

| Parameter | Default | Flag |
|-----------|---------|------|
| Workers | 0 (control-plane only) | `--workers N` |
| CPUs per node | 2 | `--cpus N` |
| Memory per node | 4096 MB | `--memory MB` |
| Disk per node | 10240 MB | `--disk MB` |
| Kubernetes version | newest 1.37 patch | `--k8s-version VER` (exact, or MAJOR.MINOR for its newest patch; `--image k8s-X.Y.Z` implies X.Y.Z) |
| CNI | `cilium` | `--cni cilium\|calico` |
| Cluster type | `kubeadm` | `--type kubeadm\|openshift` (OpenShift = MicroShift, `--openshift-version`, default 4.20) |
| Admin workstation | off | `--admin-workstation` (extra VM with security tools and the cluster kubeconfig) |

## CNI Choice

| CNI | kube-proxy | Notes |
|-----|------------|-------|
| `cilium` (default) | **Not installed** — kubeadm runs with `--skip-phases=addon/kube-proxy`; Cilium does kube-proxy replacement | Needs `k8s-kernel` for BPF |
| `calico` | **Installed** (kubeadm default kube-proxy DaemonSet) | Tigera operator (Calico v3.29.3), VXLAN encapsulation, nftables dataplane, BGP disabled |

Use `--cni calico` when you need kube-proxy present (e.g. auditing kube-proxy configuration) or want Calico NetworkPolicy behaviour.

```bash
sudo vmm cluster create calico-test --cni calico --workers 1
```

## Kernel and Rootfs Requirements

Clusters need the `k8s-kernel` (Linux 6.6 LTS) for Cilium BPF support. If `k8s-kernel` is available locally, it is auto-selected automatically — you don't need to pass `--kernel k8s-kernel` explicitly. It is **not** pulled automatically: if it's missing the default 6.1 kernel is used and Cilium will fail, so run `sudo vmm kernel pull k8s-kernel` once before the first cluster (`sudo vmm kernel list` to check). OpenShift clusters prefer `security-kernel` (6.12) when present, then `k8s-kernel`.

Pre-built K8s rootfs images (`k8s-<version>.ext4`) include containerd and kubeadm pre-installed, cutting provisioning time significantly. If not available locally, vmm attempts to download from GitHub releases. If that fails, packages are installed at runtime over the network (~30-60s extra per node).

## Commands

```bash
# Create a single-node cluster (uses VMM-managed SSH key by default)
sudo vmm cluster create dev

# Create a 3-node cluster (1 control plane + 2 workers)
sudo vmm cluster create prod --workers 2 --cpus 4 --memory 8192

# Explicitly specify kernel, image, and K8s version
sudo vmm cluster create prod --kernel k8s-kernel --image k8s-1.37.1 --workers 2

# Add your own SSH key to the nodes
sudo vmm cluster create dev --ssh-key ~/.ssh/id_ed25519.pub

# Add an admin workstation VM (security tools + kubeconfig, its own SSH key to the nodes)
sudo vmm cluster create dev --workers 1 --admin-workstation

# List / inspect clusters (add --json for machine-readable output)
sudo vmm cluster list
sudo vmm cluster show mycluster

# Extract kubeconfig again (merges into the invoking user's ~/.kube/config)
sudo vmm cluster kubeconfig mycluster

# Stop / start a cluster (start waits for fresh node Ready status;
# a plain `kubectl wait` right after a restart can pass on stale status)
sudo vmm cluster stop mycluster
sudo vmm cluster start mycluster              # --timeout 10m, or --no-wait to skip the Ready wait

# Delete cluster and all its VMs
sudo vmm cluster delete mycluster -f
```

`--ssh-key` is optional. When omitted, the VMM-managed Ed25519 key (`/var/lib/vmm/ssh/vmm_ed25519`) is used for cluster provisioning (kubeadm over SSH). If you provide a public key, it is added to the nodes alongside the managed key, and its private half is tried first for provisioning when it sits next to it, falling back to the managed key (e.g. when the private key has a passphrase and there's no ssh-agent).

## VM Naming Convention

Cluster VMs follow a fixed naming pattern:

- Control plane: `{cluster-name}-control-plane`
- Workers: `{cluster-name}-worker-1`, `{cluster-name}-worker-2`, ...
- Admin workstation (if requested): `{cluster-name}-admin` — `sudo vmm ssh {cluster-name}-admin`, then `kubectl` and `ssh <node-ip>` work from inside it

These VMs appear in `sudo vmm list` alongside standalone VMs.

## Kubeconfig

After creation, the kubeconfig is automatically merged into `~/.kube/config` as context `vmm-{cluster-name}`.

```bash
kubectl --context vmm-mycluster get nodes
kubectl --context vmm-mycluster get pods -A
```

## JSON Output

`cluster list --json` (array), `cluster show <name> --json` and `cluster create --json` all print the same stable shape, meant for scripts and test harnesses:

```json
{"name": "dev", "state": "running", "type": "kubeadm", "cni": "cilium", "k8s_version": "1.37.1",
 "nested": false, "control_plane_ip": "172.16.0.5", "context": "vmm-dev",
 "nodes": [{"name": "dev-control-plane", "role": "control-plane", "ip": "172.16.0.5", "state": "running"},
           {"name": "dev-worker-1", "role": "worker", "ip": "172.16.0.6", "state": "running"}],
 "created_at": "..."}
```

`admin_workstation` (`{name, ip, state}`) appears when there is one; OpenShift clusters have `openshift_version` instead of `k8s_version`. `cluster create --json` writes progress to stderr, so stdout holds only the JSON once the cluster is ready.

## Provisioning Sequence

1. Create VMs (control plane + workers)
2. Start all VMs, wait for SSH connectivity
3. Install containerd on all nodes (skipped if pre-installed rootfs)
4. Install kubeadm/kubelet/kubectl (skipped if pre-installed rootfs)
5. `kubeadm init` on control plane (skips kube-proxy for Cilium only)
6. Install the CNI: Cilium with kube-proxy replacement (default), or Calico via the Tigera operator (`--cni calico`)
7. Join workers via `kubeadm join`
8. Wait for all nodes to reach Ready state (up to 5 minutes)
9. Extract and merge kubeconfig

## Troubleshooting

- **Cluster creation fails at SSH**: VMs may not have booted yet. Check `sudo vmm list` to verify VMs are running, then check `/var/lib/vmm/logs/<vm-name>.log`.
- **Cilium not starting**: Ensure `k8s-kernel` (6.6 LTS) is being used. The default 6.1 kernel lacks required BPF features.
- **Node not joining**: SSH into the worker VM and check `journalctl -u kubelet` for errors.
- **Cluster stuck in creating state**: If provisioning was interrupted, delete and recreate. Cluster state is in `/var/lib/vmm/clusters/<name>.json`.

## Resource Planning

Each cluster node uses the configured CPU/memory/disk. A 3-node cluster with defaults consumes:
- 6 vCPUs (2 per node)
- 12 GB RAM (4 GB per node)
- 30 GB disk (10 GB per node)

Plan host resources accordingly when creating multiple clusters or combining with standalone VMs.

## Nested Clusters (Kata Containers)

`--nested` runs the nodes under Cloud Hypervisor with `/dev/kvm`, so pods can run in VMs with Kata Containers (or VMs with KubeVirt). kubeadm clusters only.

Prerequisites (same as nested VMs in the main skill, plus a KVM-capable k8s kernel): nested KVM on the host, `cloud-hypervisor` installed, and a current `k8s-kernel` (`sudo vmm kernel pull k8s-kernel --force` if the local one predates KVM support — provisioning fails with a "has no /dev/kvm" message otherwise, before Kubernetes is installed).

```bash
sudo vmm cluster create lab --nested --workers 1 --cpus 4 --memory 8192 --disk 20480

# Install Kata (needs helm) and run a pod in a Kata VM
helm --kube-context vmm-lab install kata-deploy \
  oci://ghcr.io/kata-containers/kata-deploy-charts/kata-deploy --version 4.2.0 -n kube-system --wait
kubectl --context vmm-lab get runtimeclasses     # kata-clh-runtime-rs, kata-qemu-runtime-rs, ...
```

Pods with `runtimeClassName: kata-clh-runtime-rs` (or `kata-qemu-runtime-rs`) run in their own VM; `uname -r` inside shows the Kata guest kernel, not the node's. Give nodes 4 vCPUs / 8 GB or more. With workers, the control plane stays tainted and Kata pods run on workers; single-node clusters have the taint removed. Kata pods are three virtualization levels deep, so expect them to be slower than ordinary pods.

