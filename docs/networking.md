# Networking and Mounts

## Network Architecture

VMs are connected via a bridge network with automatic IP configuration:

```
Host Network (eth0)
       |
       v
+--------------+
|   iptables   |  <- NAT/MASQUERADE
|   DNAT/SNAT  |  <- Port forwarding
+--------------+
       |
       v
+--------------+
|   vmm-br0    |  <- Bridge (172.16.0.1/16)
+--------------+
    |  |  |
    v  v  v
  tap0 tap1 tap2  <- One TAP per VM
    |  |  |
    v  v  v
  VM1 VM2 VM3     <- 172.16.0.2, 172.16.0.3, ...
```

IP addresses are allocated from 172.16.0.2 upward when a VM is started (not when created). Each start scans existing VMs and picks the next unused address, so multiple clusters and standalone VMs can coexist without IP collisions. The IP is configured via kernel command line parameters, so VMs get network connectivity immediately on boot.

## Port Forwarding

```bash
# Forward host port 8080 to VM port 80 (needs sudo for iptables)
sudo vmm port-forward add myvm 8080:80

# List port forwards
vmm port-forward list myvm

# Remove a port forward
sudo vmm port-forward remove myvm 8080:80
```

## SSH Key Injection

VMM automatically injects SSH public keys into VMs at startup. When you create a VM with the `--ssh-key` flag, the specified public key is stored in the VM configuration. When the VM starts, VMM:

1. Mounts the VM's rootfs image
2. Creates `/root/.ssh/` directory if needed
3. Writes the public key to `/root/.ssh/authorized_keys`
4. Sets correct permissions (700 for directory, 600 for file)
5. Unmounts and boots the VM

This allows passwordless SSH access as root using your existing SSH key pair. SSH key injection requires root privileges (for mounting the rootfs image).

## DNS Configuration

By default, VMs use public DNS servers (8.8.8.8, 8.8.4.4, 1.1.1.1). To use custom DNS servers:

```bash
# Use Quad9 and Cloudflare DNS
sudo vmm create myvm --dns 9.9.9.9 --dns 1.0.0.1

# Use corporate DNS
sudo vmm create myvm --dns 10.0.0.53 --dns 10.0.0.54
```

DNS configuration is written to `/etc/resolv.conf` in the VM's rootfs each time the VM starts.

## Host Directory Mounting

VMM can mount host directories inside VMs, making them accessible as block devices.

### How It Works

Since Firecracker doesn't support virtio-fs, VMM uses a block device approach:

1. At VM start, an ext4 image is created from each host directory
2. The image is attached as an additional block device (`/dev/vdb`, `/dev/vdc`, etc.)
3. Fstab entries are injected into the VM rootfs for auto-mounting
4. The VM boots with mounts available at `/mnt/<tag>`

### Creating a VM with Mounts

```bash
# Single mount (read-write by default)
sudo vmm create myvm --mount /home/user/code:code --ssh-key ~/.ssh/id_ed25519.pub

# Multiple mounts with different modes
sudo vmm create myvm \
  --mount /home/user/code:code:ro \
  --mount /home/user/output:output:rw \
  --ssh-key ~/.ssh/id_ed25519.pub

sudo vmm start myvm
```

The mount format is: `/host/path:tag[:ro|rw]`
- `/host/path` - Absolute path to the directory on the host
- `tag` - Name for the mount (alphanumeric, dashes, underscores only)
- `ro|rw` - Optional mode, defaults to `rw` (read-write)

### Accessing Mounts in the VM

```bash
vmm ssh myvm
# Inside the VM:
ls /mnt/code
```

### Syncing Mount Contents

If you make changes to the host directory while the VM is stopped, the changes will be included when you start the VM (the mount image is recreated from the host directory at each start).

To explicitly sync a mount image:

```bash
sudo vmm stop myvm
sudo vmm mount sync myvm code
sudo vmm start myvm
```

### Listing Mounts

```bash
vmm mount list myvm
# Output:
# Mounts for VM 'myvm':
#   code: /home/user/code -> /mnt/code (ro) [/dev/vdb]
#   output: /home/user/output -> /mnt/output (rw) [/dev/vdc]
```

### Limitations

- Mount images are snapshots - changes inside the VM are not reflected back to the host
- The VM must be stopped to sync mount contents from the host
- Mount tags must be unique within a VM
