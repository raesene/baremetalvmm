# Troubleshooting

## Debugging

```bash
cat /var/lib/vmm/vms/<name>.json          # VM state
cat /var/lib/vmm/logs/<name>.log          # Firecracker logs
sudo vmm console <name> --full            # Serial console output (kernel boot, panics)
sudo vmm console <name>                   # Tail + follow console output live
ip link show vmm-br0                       # Bridge
sudo iptables -t nat -L -n -v             # NAT rules
ps aux | grep firecracker                  # Processes
vmm ssh myvm -- 'getent hosts google.com'  # DNS from VM
```

## KVM Not Available

```
Error: /dev/kvm not found
```

Ensure:
1. Your CPU supports virtualization (Intel VT-x or AMD-V)
2. Virtualization is enabled in BIOS
3. KVM modules are loaded: `sudo modprobe kvm_intel` or `sudo modprobe kvm_amd`

## Permission Denied on /dev/kvm

```bash
# Add your user to the kvm group
sudo usermod -aG kvm $USER
# Log out and back in
```

## Network Not Working in VM

Ensure IP forwarding is enabled:
```bash
sudo sysctl -w net.ipv4.ip_forward=1
```

Check iptables rules:
```bash
sudo iptables -t nat -L -n
```

Test connectivity from host:
```bash
ping 172.16.0.2
```

## VM Can't Reach the Internet

Verify the `host_interface` in your config matches your actual network interface:
```bash
# Find your network interface
ip route | grep default
# Example output: default via 192.168.1.1 dev wlp3s0

# Check your config
cat ~/.config/vmm/config.json

# Update host_interface if needed (e.g., change "eth0" to "wlp3s0")
```

After updating the config, restart your VM for the NAT rules to be recreated with the correct interface.

## VM Won't Start

Check the serial console log for kernel boot errors:
```bash
sudo vmm console <name> --full
```

Or check the Firecracker log:
```bash
cat /var/lib/vmm/logs/<vmname>.log
```

Check Firecracker socket:
```bash
ls -la /var/lib/vmm/sockets/
```

## VM Shows as Stopped When Running

Ensure you're checking with `vmm list` (no sudo required). The tool correctly detects running VMs even when run as non-root.
