# CLI Command Reference

## VM Lifecycle

| Command | Description |
|---------|-------------|
| `vmm create <name>` | Create a new VM configuration (VM is not running yet) |
| `vmm start <name>` | Start a VM - assigns IP address, sets up networking, boots VM (requires root) |
| `vmm stop <name>` | Stop a running VM (requires root) |
| `vmm delete <name>` | Delete a VM and its resources |
| `vmm list` | List all VMs |

**Note**: VMs must be explicitly started after creation. IP addresses are assigned at start time, not at creation time.

## Create Options

```bash
vmm create <name> [flags]

Flags:
  --cpus int         Number of vCPUs (default 1)
  --memory int       Memory in MB (default 512)
  --disk int         Disk size in MB (default 1024)
  --ssh-key string   Path to SSH public key file for root access
  --dns string       Custom DNS servers (can be specified multiple times)
  --image string     Name of rootfs image to use (from 'vmm image import')
  --kernel string    Name of kernel to use (from 'vmm kernel import' or 'vmm kernel build')
  --mount string     Mount host directory in VM (format: /host/path:tag[:ro|rw], can be repeated)
```

Example with all options:
```bash
sudo vmm create myvm --cpus 2 --memory 2048 --disk 10000 \
  --ssh-key ~/.ssh/id_ed25519.pub \
  --dns 9.9.9.9 --dns 1.1.1.1 \
  --image ubuntu-base \
  --kernel my-kernel \
  --mount /home/user/code:code:ro
```

## Access

| Command | Description |
|---------|-------------|
| `vmm ssh <name>` | SSH into a VM as root |
| `vmm ssh <name> -u <user>` | SSH as specific user |

SSH access requires an SSH public key to be configured when creating the VM using the `--ssh-key` flag. You can use `sudo vmm ssh <name>` if you prefer consistency with other commands - VMM automatically detects the original user and uses their SSH keys.

## Networking

| Command | Description |
|---------|-------------|
| `vmm port-forward add <name> <host>:<guest>` | Forward port from host to VM |
| `vmm port-forward list <name>` | List port forwards for a VM |
| `vmm port-forward remove <name> <host>:<guest>` | Remove a port forward |

Example:
```bash
# Forward host port 8080 to VM port 80 (needs sudo for iptables)
sudo vmm port-forward add myvm 8080:80

# List port forwards
vmm port-forward list myvm

# Remove a port forward
sudo vmm port-forward remove myvm 8080:80
```

## Mounts

| Command | Description |
|---------|-------------|
| `vmm mount list <name>` | List mounts configured for a VM |
| `vmm mount sync <name> <tag>` | Sync mount image from host directory (VM must be stopped) |

## Images

| Command | Description |
|---------|-------------|
| `vmm image list` | List available images with descriptions |
| `vmm image pull` | Download default images |
| `vmm image import <docker-image> --name <name>` | Import a Docker image as rootfs |
| `vmm image snapshot <vm> --name <name>` | Snapshot a stopped VM's rootfs as a reusable base image |
| `vmm image delete <name>` | Delete an imported image |

## Kernels

| Command | Description |
|---------|-------------|
| `vmm kernel list` | List available kernels |
| `vmm kernel import <path> --name <name>` | Import a custom kernel binary |
| `vmm kernel build --version <ver> --name <name>` | Build a kernel from source |
| `vmm kernel delete <name>` | Delete a custom kernel |

## Clusters

| Command | Description |
|---------|-------------|
| `vmm cluster create <name>` | Create a Kubernetes cluster |
| `vmm cluster delete <name>` | Delete a cluster and all its VMs |
| `vmm cluster list` | List all clusters |
| `vmm cluster kubeconfig <name>` | Re-extract and merge kubeconfig |

See [Kubernetes Clusters](kubernetes.md) for full cluster create options.

## Configuration

| Command | Description |
|---------|-------------|
| `vmm config show` | Show current configuration |
| `vmm config init` | Initialize directories and config |
