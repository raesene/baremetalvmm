# Security Testing with Vulnerable Kernels

VMM includes a **security testing kernel** designed for vulnerability research and PoC exploit testing. This kernel is built from the 6.12 LTS series with broad subsystem coverage, so most kernel exploits that work on Ubuntu will work in VMM VMs.

## The Security Kernel

The security kernel (`--config-profile security` in the build script) enables many kernel subsystems beyond what the default and k8s kernels provide:

| Subsystem | Config options | Used by |
|-----------|---------------|---------|
| IPsec/xfrm | `INET_ESP`, `INET6_ESP`, `XFRM` | dirtyfrag |
| AF_RXRPC | `AF_RXRPC` | dirtyfrag |
| AF_ALG crypto | `CRYPTO_USER_API_AEAD`, `CRYPTO_AUTHENC` | CVE-2026-31431 (copy-fail) |
| io_uring | `IO_URING` | Various io_uring CVEs |
| SCTP/DCCP/TIPC | `IP_SCTP`, `IP_DCCP`, `TIPC` | Network protocol CVEs |
| userfaultfd | `USERFAULTFD` | Race condition exploits |
| Tunneling | `GRE`, `IPIP`, `IPV6_SIT`, `L2TP` | Network stack exploits |
| FUSE/Btrfs/XFS | `FUSE_FS`, `BTRFS_FS`, `XFS_FS` | Filesystem CVEs |
| Traffic control | `NET_SCHED`, `NET_SCH_*`, `NET_CLS_*` | TC/qdisc exploits |
| LSMs | `SECURITY_APPARMOR`, `SECURITY_SELINUX` | Security testing |
| Tracing | `FTRACE`, `KPROBES`, `UPROBE_EVENTS` | Exploit development |

The security kernel is available as a pre-built download from GitHub releases (tagged `security-kernel-*`), or you can build it locally.

## Getting the Security Kernel

**Option 1: Download from GitHub releases**

```bash
wget https://github.com/raesene/baremetalvmm/releases/download/security-kernel-<version>/security-vmlinux.bin
sudo vmm kernel import security-vmlinux.bin --name security-kernel
```

**Option 2: Build locally**

```bash
sudo bash scripts/build-kernel.sh --version 6.12 --name security-kernel --config-profile security
```

## Using the Security Kernel

```bash
# Standalone VM
sudo vmm create vuln-test --cpus 2 --memory 2048 --kernel security-kernel --ssh-key ~/.ssh/id_ed25519.pub
sudo vmm start vuln-test

# Kubernetes cluster (for container escape PoCs)
sudo vmm cluster create vuln-cluster --workers 1 --cpus 2 --memory 4096 \
    --kernel security-kernel --ssh-key ~/.ssh/id_ed25519.pub
```

## Capturing Crash Output

All VMs automatically capture serial console output (kernel boot messages, panics, oops) to `/var/lib/vmm/logs/<name>-console.log`. This is essential for vulnerability research where kernel crashes need to be captured.

```bash
# Follow console output live while running an exploit
sudo vmm console vuln-test

# Review crash output after a kernel panic
sudo vmm console vuln-test --full

# Show last 100 lines of console output
sudo vmm console vuln-test -n 100 --follow=false
```

The console log captures everything from early kernel boot through to any panic or oops output, including stack traces and register dumps.

## Verifying Kernel Capabilities

Check that the required subsystems are available inside the VM:

```bash
# Check kernel version
sudo vmm ssh vuln-test -- "uname -r"

# Check specific config options
sudo vmm ssh vuln-test -- "zcat /proc/config.gz | grep -E 'INET_ESP|AF_RXRPC|IO_URING|CRYPTO_USER_API_AEAD'"

# Test AF_ALG (for copy-fail / CVE-2026-31431)
sudo vmm ssh vuln-test -- "python3 -c \"import socket; s = socket.socket(socket.AF_ALG, socket.SOCK_SEQPACKET, 0); s.bind(('aead', 'gcm(aes)')); print('AF_ALG AEAD: available'); s.close()\""
```

## Default vs Security Kernel

The **default kernel** (6.1 LTS) and **k8s kernel** (6.6 LTS) include only the subsystems needed for running VMs and Kubernetes. They have a smaller attack surface and are what you'd use for normal development work.

The **security kernel** (6.12 LTS) deliberately enables a broad set of subsystems to match what's available on a stock Ubuntu 24.04 installation, making it suitable for reproducing PoC exploits that target those subsystems.

## Cleanup

```bash
# Standalone
sudo vmm stop vuln-test && sudo vmm delete vuln-test

# Cluster
sudo vmm cluster delete vuln-cluster -f
```
