---
name: vmm-usage
description: >
  Create, manage, and interact with Firecracker microVMs and Kubernetes clusters using the vmm CLI tool.
  Use when an agent needs to: spin up a Linux VM for testing or development, create a Kubernetes cluster
  from microVMs, snapshot a VM's rootfs for reuse, take and restore memory snapshots of a running VM,
  manage VM lifecycle (create/start/stop/delete), SSH into or copy files to/from a running VM,
  run untrusted code in an internet-only VM, work with custom kernels, or create a nested VM that can run its own VMs
  (Cloud Hypervisor backend, --nested). Triggers on tasks involving virtual machines, test environments,
  isolated Linux environments, Kubernetes cluster creation, nested virtualization, /dev/kvm inside a VM,
  KubeVirt/Kata/hypervisor testing, or VM-based infrastructure.
  Assumes vmm is already installed and configured on the host.
---

# vmm — Bare Metal MicroVM Manager

vmm manages Firecracker microVMs on Linux. VMs that need to run VMs of their own (nested virtualization) run under Cloud Hypervisor instead; see [Nested VMs](#nested-vms-vms-inside-vms). Run every vmm command with `sudo`, including read-only ones like `list`: the data directory (`/var/lib/vmm`) is root-only, so without sudo they fail with "permission denied".

## Quick Reference

```
sudo vmm create <name> [--cpus N] [--memory MB] [--disk MB] [--ssh-key PATH] [--kernel NAME] [--image NAME] [--dns SERVER] [--mount PATH:TAG[:ro|rw]] [--no-autostart] [--restrict-local-networks] [--nested] [--backend firecracker|cloud-hypervisor]
sudo vmm start <name>
sudo vmm stop <name>
sudo vmm delete <name> [-f]
sudo vmm list [--json]
sudo vmm ssh <name> [-u user] [-- <command>]
sudo vmm cp [-r] [-u user] <src> <dest>          # one side is <vm>:<path>
sudo vmm console <name> [--full] [-n LINES]
sudo vmm set <name> autostart|restrict-local-networks on|off
sudo vmm snapshot create|list|show|restore|delete ...
sudo vmm port-forward add|remove|list|apply ...
sudo vmm mount list|sync ...
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

SSH connects as root using the VMM-managed key. Use `-u <user>` for a different user. If you provided a custom `--ssh-key` at create time, the managed key is still used first. Host key checking is off (IPs get reused when VMs are recreated), so there are no known_hosts prompts.

### Copy files to and from a VM

```bash
sudo vmm cp ./app.tar.gz myvm:/tmp/
sudo vmm cp myvm:/var/log/syslog ./syslog
sudo vmm cp -r ./src myvm:/root/src
```

Exactly one side must be `<vm>:<path>`; a relative guest path is relative to the user's home. The VM must be running.

### Stop and delete

```bash
sudo vmm stop myvm
sudo vmm delete myvm
# Deleting a running VM is refused; -f stops it first:
sudo vmm delete myvm -f
```

Stop shuts the guest down cleanly (it waits up to 30s before falling back to a signal), so expect it to take a few seconds.

### List VMs

```bash
sudo vmm list          # all VMs, including stopped ones (-a is on by default)
sudo vmm list --json   # machine-readable: array of VM records ([] when none)
```

### Autostart

VMs start with the host by default (via `vmm.service`). Use `--no-autostart` on create, or `sudo vmm set <name> autostart off` later, for VMs that shouldn't.

## Networking

VMs get IPs from 172.16.0.0/16, allocated sequentially from 172.16.0.2. The host can reach VMs directly by IP. VMs have outbound internet via NAT.

To find a VM's IP, prefer the JSON output: `sudo vmm list --json | jq -r '.[] | select(.name=="myvm") | .ip_address'` (other useful fields: `state`, `backend`, `nested`, `port_forwards`). The table's columns are `NAME ID STATE CPUs MEMORY IP ADDRESS BACKEND`, and MEMORY prints as two words (`512 MB`), so with awk the IP is field 7: `sudo vmm list | awk '$1=="myvm" {print $7}'`.

Other networking features:
- **Port forwarding** (live, works on running VMs, survives restarts): `sudo vmm port-forward add <name> <host-port>:<guest-port>[/udp]`, `remove` with the same spec, `list [name]`
- **Custom DNS**: `--dns <server>` flag on create (repeatable)
- **Internet-only VM for untrusted code**: `--restrict-local-networks` on create (or `sudo vmm set <name> restrict-local-networks on|off`, live). The VM can't open connections to the host, private/LAN ranges or other VMs; `vmm ssh`/`cp` and port forwards still work. Its DNS servers must be public. Needs `nft` on the host (create warns if it's missing; start fails closed). Not for clusters

## Host Directory Mounts

```bash
sudo vmm create myvm --mount /home/me/code:code:ro
sudo vmm start myvm          # appears in the guest at /mnt/code
```

A mount is a **copy**: the host directory is packed into a fresh ext4 image each time the VM starts, not shared live. Host changes made while the VM runs aren't seen until the next start, and guest writes never reach the host and are lost on restart, even with `rw`. Use `vmm cp` to get results back out. `sudo vmm mount sync myvm code` rebuilds an image while the VM is stopped; `sudo vmm mount list myvm` shows a VM's mounts.

## Memory Snapshots (Firecracker VMs)

Save a running VM's full state (memory, devices, disks) and roll it back later — useful for resetting an environment between test runs.

```bash
sudo vmm snapshot create myvm clean            # VM keeps running; --stop to stop it afterwards
sudo vmm snapshot list [myvm]
sudo vmm snapshot restore myvm clean -f        # -f stops the VM first if running
sudo vmm snapshot restore myvm clean --no-start   # roll back disks only, leave it stopped
sudo vmm snapshot delete myvm clean
```

Restore is in place only (same VM, same IP/MAC); a snapshot can't be used to clone a new VM — use `vmm image snapshot` (below) for that. Not supported for Cloud Hypervisor (nested) VMs.

## Images and Kernels

### Pull default images

```bash
sudo vmm image pull    # downloads default kernel + rootfs
```

This downloads `vmlinux.bin` (Linux 6.1 LTS kernel) and `rootfs.ext4` (Ubuntu 24.04) from GitHub releases.

### List available images and kernels

```bash
sudo vmm image list            # local; also notes when a newer release of a local image exists
sudo vmm kernel list
sudo vmm image list --remote   # what's available to pull from GitHub releases
sudo vmm kernel list --remote
```

Pull a non-default artifact by its local name: `sudo vmm kernel pull security-kernel`, `sudo vmm image pull security-rootfs`. Add `--force` to replace an existing copy with the newest release.

### Kernel variants

| Name | Series | Use case |
|------|--------|----------|
| `vmlinux.bin` (default) | 6.1 LTS | General-purpose VMs |
| `k8s-kernel` | 6.6 LTS | Kubernetes clusters (Cilium/BPF, AppArmor, KVM for nested nodes) |
| `security-kernel` | 6.12 LTS | Security testing (broad module coverage) |
| `security-kernel-<series>` | 5.10, 5.15, 6.1, 6.6, 6.18 | Same, other LTS series (e.g. `security-kernel-5.15`) |
| `kasan-kernel` / `kasan-kernel-<series>` | 6.12 / other LTS | KASAN memory sanitizer (detects use-after-free, OOB, etc.) |
| `nested-kernel` | 6.1 LTS | Nested VMs: default kernel + KVM, auto-selected by `--nested` |

Use `--kernel <name>` on create to select a non-default kernel.

### Rootfs variants

| Name | Description |
|------|-------------|
| `rootfs` (default) | Ubuntu 24.04, general-purpose |
| `k8s-<version>` (e.g. `k8s-1.37.1`) | Ubuntu 24.04 with kubeadm/containerd pre-installed (clusters pull these automatically) |
| `security-rootfs` | Ubuntu 26.04 with container/K8s security tools, ripgrep, fd and the pi coding agent |
| `dev-rootfs` | Ubuntu 26.04 development image (Go, Node.js, Docker, tmux, pi agent) |

Use `--image <name>` on create to select a non-default rootfs (the name as shown by `vmm image list`, without `.ext4`). `--disk` only grows a rootfs: an image bigger than `--disk` keeps its own size, so pass a `--disk` well above the image size if you need free space in it.

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

The snapshot shrinks the rootfs to minimum size automatically, so pass `--disk` when creating VMs from it if they need free space.

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
- To run vmm *inside* a nested VM, set a different `subnet`/`gateway` (e.g. `172.17.0.0/16` / `172.17.0.1`) in the inner `/etc/vmm/config.json` first (`sudo vmm config init` creates it) — the default 172.16.0.0/16 clashes with the nested VM's own network.
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
sudo vmm cluster create mycluster --workers 2 --cpus 4 --memory 8192 --k8s-version 1.36   # newest 1.36 patch; or exact, e.g. 1.36.5 (default: newest 1.37 patch)

# Calico CNI instead of Cilium (keeps kube-proxy; Cilium replaces it)
sudo vmm cluster create mycluster --cni calico

# Single-node OpenShift (MicroShift) instead of kubeadm
sudo vmm cluster create myocp --type openshift

# Use the cluster (context merged into the invoking user's ~/.kube/config)
kubectl --context vmm-mycluster get nodes

# Status, machine-readable (stable shape: name, state, type, cni, k8s_version,
# control_plane_ip, context, nodes[{name, role: control-plane|worker, ip, state}])
sudo vmm cluster list --json
sudo vmm cluster show mycluster --json
sudo vmm cluster create ci --workers 1 --json > ci.json   # progress goes to stderr

# Stop and start (workers stop first; start waits for every node to report Ready)
sudo vmm cluster stop mycluster
sudo vmm cluster start mycluster

# Delete
sudo vmm cluster delete mycluster -f
```

`--ssh-key` is optional for clusters too — the VMM-managed key is used for cluster provisioning (kubeadm over SSH) by default. A public key passed with `--ssh-key` is added to the nodes; its private half is tried first for provisioning if it sits next to the `.pub` file, with the managed key as fallback (so a passphrase-protected key is fine). If `k8s-kernel` is available locally, it is auto-selected for clusters; it isn't pulled automatically, and Cilium fails on the default kernel, so run `sudo vmm kernel pull k8s-kernel` before the first cluster.

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
# Ping answers before sshd is up; wait for SSH itself when you need to run commands
until sudo vmm ssh myvm -- true &>/dev/null; do sleep 1; done
# or just the network:
VM_IP=$(sudo vmm list --json | jq -r '.[] | select(.name=="myvm") | .ip_address')
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
sudo vmm kernel pull security-kernel     # once, if not already local
sudo vmm image pull security-rootfs
sudo vmm create sectest --cpus 2 --memory 2048 --disk 8192 --kernel security-kernel --image security-rootfs
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

The KASAN kernel has the same module coverage as the security kernel, plus Kernel Address Sanitizer instrumentation. It detects use-after-free, out-of-bounds access, double-free, and other memory corruption bugs, reporting them to the serial console. Available across all LTS series (5.10–6.18) as `kasan-kernel-<series>`. Pull with `sudo vmm kernel pull kasan-kernel` (or `kasan-kernel-<series>`), or build locally with `sudo vmm kernel build --profile security-kasan`.

### Use a custom SSH key alongside the managed key

```bash
sudo vmm create myvm --cpus 2 --memory 1024 --ssh-key ~/.ssh/id_ed25519.pub
```

Your key is added alongside the VMM-managed key. Both can be used to access the VM.

## Important Notes

- All create/start/stop/delete operations require `sudo`.
- VMs restart with the host (autostart is on by default; `--no-autostart` or `vmm set <name> autostart off` to opt out) when `vmm.service` is installed. Don't run `systemctl stop vmm` — it stops every VM.
- VM configs are stored in `/var/lib/vmm/vms/<name>.json`.
- Hypervisor logs (Firecracker or Cloud Hypervisor) are in `/var/lib/vmm/logs/<name>.log` — check these if a VM fails to start.
- `sudo vmm console <name>` follows the log until interrupted; use `sudo vmm console <name> --full` in scripts or it will hang.
- VM names must be unique. Attempting to create a duplicate name will fail.
- Disk size is set at creation time and cannot be changed later.
- VMM auto-manages an Ed25519 SSH key pair — `--ssh-key` is optional and adds your key alongside the managed one.
- The `--ssh-key` flag takes a path to a **public** key file (e.g., `~/.ssh/id_ed25519.pub`).
- Create flag defaults can be set in `/etc/vmm/config.json` under `vm_defaults` (`cpus`, `memory_mb`, `disk_size_mb`, `image`, `kernel`, `ssh_key_path`, `dns_servers`) (resolution: CLI flag > config default > hardcoded fallback).
