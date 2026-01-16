# Bare Metal Container VMM

## Project Goal

The goal of this project is to create a program that will allow for multiple lightweight VMs to be started on a single bare metal host.

The VMs should be as lightweight as possible and don't need extensive hardware emulation.

It should be possible to network the VMs and expose them to the wider network via port-forwarding or similar. The VMs will run an SSH daemon so that users can connect to them.

There should be a control panel which allows the administrator to start and stop the VMs.

The host system will run a standard Linux distribution with Ubuntu 24.04 being the reference.

## Implementation Status

**Status: Core functionality complete and tested**

### VMM Choice: Firecracker

After evaluating the options, **Firecracker** was chosen as the VMM engine because:
- Designed specifically for lightweight, fast-booting microVMs
- Sub-second boot times
- Minimal memory overhead (<5MB per VM)
- Well-maintained Go SDK (firecracker-go-sdk)
- Production-proven (used by AWS Lambda and Fargate)

### What's Implemented

| Feature | Status | Notes |
|---------|--------|-------|
| VM Creation | Done | `vmm create <name>` with CPU/memory/disk options |
| VM Start | Done | Full Firecracker integration with networking |
| VM Stop | Done | Graceful shutdown with TAP cleanup |
| VM Delete | Done | With `--force` option for running VMs |
| VM Listing | Done | Works as root and non-root users |
| Bridge Networking | Done | vmm-br0 with NAT/MASQUERADE |
| IP Allocation | Done | Sequential from 172.16.0.2 via kernel args |
| Port Forwarding | Done | iptables DNAT rules |
| Image Management | Done | Downloads Firecracker quickstart images |
| systemd Integration | Done | Auto-start/stop on boot |
| SSH Access | Done | SSH key injection via `--ssh-key` flag |

### What's Not Yet Implemented

- **Cloud-init support** - For more flexible VM initialization
- **Custom rootfs images** - Currently uses Firecracker quickstart image
- **Jailer integration** - For production security hardening
- **VM snapshots** - Save/restore VM state
- **Web UI** - Browser-based management interface
- **Resource quotas** - CPU/memory/disk enforcement

## VMM Options Explored

### Firecracker (Selected)
https://github.com/firecracker-microvm/firecracker

Lightweight VMM designed for serverless workloads. Chosen for this project.

### Cloud Hypervisor
https://github.com/cloud-hypervisor/cloud-hypervisor

More feature-rich but heavier. Better suited for traditional VM workloads.

### Similar Systems

Other software that provided inspiration:

- https://github.com/liquidmetal-dev/flintlock - Firecracker orchestration

## Testing

Tested on Ubuntu 24.04 with:
- KVM support enabled
- Go 1.22+
- Firecracker v1.11.0

All core commands verified working:
```bash
vmm config init    # Initialize configuration
vmm image pull     # Download kernel and rootfs
vmm create test1   # Create VM
vmm start test1    # Start VM (requires root)
vmm list           # List VMs (works as non-root)
vmm stop test1     # Stop VM
vmm delete test1   # Delete VM
```

Network connectivity verified - VMs are pingable from host at 172.16.0.x addresses.
