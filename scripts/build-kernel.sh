#!/bin/bash
#
# build-kernel.sh - Build a Firecracker-compatible Linux kernel
#
# This script downloads, configures, and builds a Linux kernel suitable
# for use with Firecracker microVMs.
#
# Usage: build-kernel.sh --version <version> --name <name> [--output <dir>]
#
# Supported versions: 5.10, 6.1, 6.6
#

set -e

# Default values
OUTPUT_DIR="/var/lib/vmm/images/kernels"
VERSION=""
NAME=""
CONFIG_PROFILE="default"
BUILD_DIR=""
CLEANUP=true

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    cat <<EOF
Usage: $0 --version <version> --name <name> [--config-profile <profile>] [--output <dir>]

Build a Firecracker-compatible Linux kernel from source.

Options:
  --version VERSION          Kernel version to build (required)
                             Supported: 5.10, 6.1, 6.6, 6.12
  --name NAME                Name for the output kernel file (required)
  --config-profile PROFILE   Configuration profile (default: default)
                             Profiles: default, security
  --output DIR               Output directory (default: /var/lib/vmm/images/kernels)
  --no-cleanup               Keep build directory after completion
  --help                     Show this help message

Profiles:
  default    Minimal Firecracker config with Docker/K8s networking support
  security   Broad module coverage matching Ubuntu 24.04 for vulnerability
             research and security testing (IPsec, SCTP, io_uring, etc.)

Examples:
  $0 --version 6.1 --name kernel-6.1
  $0 --version 6.12 --name security-vmlinux.bin --config-profile security
  $0 --version 5.10 --name kernel-lts --output /custom/path
EOF
    exit 1
}

log_info() {
    echo -e "${GREEN}[INFO]${NC} $1" >&2
}

log_warn() {
    echo -e "${YELLOW}[WARN]${NC} $1" >&2
}

log_error() {
    echo -e "${RED}[ERROR]${NC} $1" >&2
}

check_dependencies() {
    log_info "Checking build dependencies..."

    local missing=()

    # Check for required packages
    local packages=(
        "build-essential"
        "flex"
        "bison"
        "bc"
        "libelf-dev"
        "libssl-dev"
        "wget"
    )

    for pkg in "${packages[@]}"; do
        if ! dpkg -l "$pkg" &>/dev/null; then
            missing+=("$pkg")
        fi
    done

    # Check for required commands
    local commands=("make" "gcc" "wget" "tar")
    for cmd in "${commands[@]}"; do
        if ! command -v "$cmd" &>/dev/null; then
            log_error "Required command not found: $cmd"
            exit 1
        fi
    done

    if [ ${#missing[@]} -ne 0 ]; then
        log_error "Missing required packages: ${missing[*]}"
        log_info "Install them with: sudo apt-get install ${missing[*]}"
        exit 1
    fi

    log_info "All dependencies satisfied"
}

get_kernel_url() {
    local series="$1"
    local major_version="${series%%.*}"

    # Validate series
    case "$series" in
        5.10|6.1|6.6|6.12) ;;
        *)
            log_error "Unsupported kernel series: $series"
            log_info "Supported series: 5.10, 6.1, 6.6, 6.12"
            exit 1
            ;;
    esac

    # Query kernel.org for the latest patch version in this series
    local latest=""
    if command -v jq &>/dev/null; then
        latest=$(wget -qO- https://www.kernel.org/releases.json 2>/dev/null | \
            jq -r --arg s "$series" \
            '[.releases[] | select(.version | startswith($s + ".")) | .version] | sort_by(split(".") | map(tonumber)) | last')
    fi

    if [ -z "$latest" ] || [ "$latest" = "null" ]; then
        log_warn "Could not query kernel.org for latest $series version, using fallback"
        # Fallback to known good versions
        case "$series" in
            5.10) latest="5.10.209" ;;
            6.1)  latest="6.1.119" ;;
            6.6)  latest="6.6.61" ;;
            6.12) latest="6.12.87" ;;
        esac
    fi

    log_info "Resolved kernel series $series to version $latest"
    echo "https://cdn.kernel.org/pub/linux/kernel/v${major_version}.x/linux-${latest}.tar.xz"
}

get_firecracker_config_url() {
    local arch="$(uname -m)"
    # Firecracker provides recommended configs in their repo
    echo "https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-${arch}-6.1.config"
}

download_kernel() {
    local url="$1"
    local filename="$(basename "$url")"

    log_info "Downloading kernel source from $url"

    if [ -f "$BUILD_DIR/$filename" ]; then
        log_info "Source already downloaded"
    else
        wget -q --show-progress -O "$BUILD_DIR/$filename" "$url"
    fi

    log_info "Extracting kernel source..."
    tar -xf "$BUILD_DIR/$filename" -C "$BUILD_DIR"

    # Find the extracted directory
    local extracted_dir="$(ls -d "$BUILD_DIR"/linux-* | head -1)"
    echo "$extracted_dir"
}

create_kernel_config() {
    local kernel_dir="$1"
    local arch="$(uname -m)"

    log_info "Downloading Firecracker recommended kernel config..."

    cd "$kernel_dir"

    # Download Firecracker's recommended config
    local config_url="https://raw.githubusercontent.com/firecracker-microvm/firecracker/main/resources/guest_configs/microvm-kernel-ci-${arch}-6.1.config"

    if ! wget -q -O .config "$config_url"; then
        log_warn "Failed to download Firecracker config, using defconfig as base"
        make defconfig
    fi

    log_info "Customizing kernel configuration..."

    # Ensure key options are set correctly
    # These are essential for Firecracker operation
    ./scripts/config --enable CONFIG_VIRTIO
    ./scripts/config --enable CONFIG_VIRTIO_MMIO
    ./scripts/config --enable CONFIG_VIRTIO_BLK
    ./scripts/config --enable CONFIG_VIRTIO_NET
    ./scripts/config --enable CONFIG_SERIAL_8250
    ./scripts/config --enable CONFIG_SERIAL_8250_CONSOLE
    ./scripts/config --enable CONFIG_EXT4_FS
    ./scripts/config --enable CONFIG_NET
    ./scripts/config --enable CONFIG_INET

    # Overlay filesystem (required for Docker)
    ./scripts/config --enable CONFIG_OVERLAY_FS

    # Netfilter/iptables (required for Docker networking)
    ./scripts/config --enable CONFIG_NETFILTER
    ./scripts/config --enable CONFIG_NETFILTER_ADVANCED
    ./scripts/config --enable CONFIG_NETFILTER_XTABLES
    ./scripts/config --enable CONFIG_NETFILTER_NETLINK
    ./scripts/config --enable CONFIG_NETFILTER_NETLINK_QUEUE
    ./scripts/config --enable CONFIG_NETFILTER_NETLINK_LOG

    # Xtables match modules (used by iptables/Cilium)
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_COMMENT
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_MULTIPORT
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_MARK
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_STATISTIC
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_CONNTRACK

    # nf_tables (used by iptables-nft on modern systems)
    ./scripts/config --enable CONFIG_NF_TABLES
    ./scripts/config --enable CONFIG_NF_TABLES_INET
    ./scripts/config --enable CONFIG_NF_TABLES_NETDEV
    ./scripts/config --enable CONFIG_NFT_NUMGEN
    ./scripts/config --enable CONFIG_NFT_CT
    ./scripts/config --enable CONFIG_NFT_COUNTER
    ./scripts/config --enable CONFIG_NFT_CONNLIMIT
    ./scripts/config --enable CONFIG_NFT_LOG
    ./scripts/config --enable CONFIG_NFT_LIMIT
    ./scripts/config --enable CONFIG_NFT_MASQ
    ./scripts/config --enable CONFIG_NFT_REDIR
    ./scripts/config --enable CONFIG_NFT_NAT
    ./scripts/config --enable CONFIG_NFT_REJECT
    ./scripts/config --enable CONFIG_NFT_COMPAT
    ./scripts/config --enable CONFIG_NFT_HASH
    ./scripts/config --enable CONFIG_NFT_FIB
    ./scripts/config --enable CONFIG_NFT_FIB_INET

    # Connection tracking (required for NAT/masquerade)
    ./scripts/config --enable CONFIG_NF_CONNTRACK
    ./scripts/config --enable CONFIG_NF_NAT
    ./scripts/config --enable CONFIG_NF_NAT_MASQUERADE

    # IPv4 netfilter
    ./scripts/config --enable CONFIG_NF_TABLES_IPV4
    ./scripts/config --enable CONFIG_NFT_CHAIN_ROUTE_IPV4
    ./scripts/config --enable CONFIG_NFT_FIB_IPV4
    ./scripts/config --enable CONFIG_NF_REJECT_IPV4
    ./scripts/config --enable CONFIG_IP_NF_IPTABLES
    ./scripts/config --enable CONFIG_IP_NF_FILTER
    ./scripts/config --enable CONFIG_IP_NF_TARGET_REJECT
    ./scripts/config --enable CONFIG_IP_NF_NAT
    ./scripts/config --enable CONFIG_IP_NF_TARGET_MASQUERADE

    # IPv6 netfilter (Docker also uses IPv6)
    ./scripts/config --enable CONFIG_NF_TABLES_IPV6
    ./scripts/config --enable CONFIG_NFT_CHAIN_ROUTE_IPV6
    ./scripts/config --enable CONFIG_NFT_FIB_IPV6
    ./scripts/config --enable CONFIG_NF_REJECT_IPV6
    ./scripts/config --enable CONFIG_IP6_NF_IPTABLES
    ./scripts/config --enable CONFIG_IP6_NF_FILTER
    ./scripts/config --enable CONFIG_IP6_NF_TARGET_REJECT
    ./scripts/config --enable CONFIG_IP6_NF_NAT
    ./scripts/config --enable CONFIG_IP6_NF_TARGET_MASQUERADE

    # Network device drivers (required for Docker and Cilium)
    ./scripts/config --enable CONFIG_BRIDGE
    ./scripts/config --enable CONFIG_VETH
    ./scripts/config --enable CONFIG_VLAN_8021Q
    ./scripts/config --enable CONFIG_MACVLAN
    ./scripts/config --enable CONFIG_IPVLAN
    ./scripts/config --enable CONFIG_DUMMY
    ./scripts/config --enable CONFIG_VXLAN
    ./scripts/config --enable CONFIG_GENEVE
    ./scripts/config --enable CONFIG_TUN

    # Bridge netfilter (for Docker bridge networks)
    ./scripts/config --enable CONFIG_NF_TABLES_BRIDGE
    ./scripts/config --enable CONFIG_BRIDGE_NF_EBTABLES
    ./scripts/config --enable CONFIG_BRIDGE_EBT_BROUTE
    ./scripts/config --enable CONFIG_BRIDGE_EBT_T_FILTER
    ./scripts/config --enable CONFIG_BRIDGE_EBT_T_NAT

    # BPF support (required for Docker device cgroup)
    ./scripts/config --enable CONFIG_BPF
    ./scripts/config --enable CONFIG_BPF_SYSCALL
    ./scripts/config --enable CONFIG_BPF_JIT
    ./scripts/config --enable CONFIG_BPF_JIT_ALWAYS_ON

    # Cgroups (required for Docker container resource management)
    ./scripts/config --enable CONFIG_CGROUPS
    ./scripts/config --enable CONFIG_CGROUP_FREEZER
    ./scripts/config --enable CONFIG_CGROUP_PIDS
    ./scripts/config --enable CONFIG_CGROUP_DEVICE
    ./scripts/config --enable CONFIG_CPUSETS
    ./scripts/config --enable CONFIG_CGROUP_CPUACCT
    ./scripts/config --enable CONFIG_MEMCG
    ./scripts/config --enable CONFIG_CGROUP_SCHED
    ./scripts/config --enable CONFIG_CFS_BANDWIDTH
    ./scripts/config --enable CONFIG_CGROUP_BPF

    # Namespaces (required for Docker container isolation)
    ./scripts/config --enable CONFIG_NAMESPACES
    ./scripts/config --enable CONFIG_UTS_NS
    ./scripts/config --enable CONFIG_IPC_NS
    ./scripts/config --enable CONFIG_USER_NS
    ./scripts/config --enable CONFIG_PID_NS
    ./scripts/config --enable CONFIG_NET_NS

    # Crypto API user-space interface (AF_ALG sockets)
    ./scripts/config --enable CONFIG_CRYPTO
    ./scripts/config --enable CONFIG_CRYPTO_USER_API
    ./scripts/config --enable CONFIG_CRYPTO_USER_API_AEAD
    ./scripts/config --enable CONFIG_CRYPTO_USER_API_HASH
    ./scripts/config --enable CONFIG_CRYPTO_USER_API_SKCIPHER
    ./scripts/config --enable CONFIG_CRYPTO_AEAD
    ./scripts/config --enable CONFIG_CRYPTO_GCM
    ./scripts/config --enable CONFIG_CRYPTO_AES
    ./scripts/config --enable CONFIG_CRYPTO_CTR
    ./scripts/config --enable CONFIG_CRYPTO_GHASH
    ./scripts/config --enable CONFIG_CRYPTO_SEQIV
    ./scripts/config --enable CONFIG_CRYPTO_AUTHENC
    ./scripts/config --enable CONFIG_CRYPTO_CBC
    ./scripts/config --enable CONFIG_CRYPTO_HMAC
    ./scripts/config --enable CONFIG_CRYPTO_SHA256

    # Kernel config access from running kernel (for diagnostics)
    ./scripts/config --enable CONFIG_IKCONFIG
    ./scripts/config --enable CONFIG_IKCONFIG_PROC

    # Keep modules enabled (BPF_JIT depends on CONFIG_MODULES in 6.1)
    # All our required features are built-in via --enable above
    ./scripts/config --enable CONFIG_MODULES

    # Disable initramfs - we boot directly to rootfs
    ./scripts/config --disable CONFIG_BLK_DEV_INITRD

    # Update the config to resolve dependencies
    make olddefconfig

    # Verify critical options survived olddefconfig
    for opt in CONFIG_BPF_JIT CONFIG_BPF_SYSCALL CONFIG_CFS_BANDWIDTH CONFIG_CGROUPS; do
        if ! grep -q "^${opt}=y" .config; then
            log_error "CRITICAL: ${opt} not enabled after olddefconfig! Check dependencies."
            grep "${opt}" .config || echo "${opt} not found in .config"
        fi
    done
}

create_security_kernel_config() {
    local kernel_dir="$1"

    log_info "Applying security testing profile (broad Ubuntu 24.04-like module coverage)..."

    cd "$kernel_dir"

    # Start with the default Firecracker config
    create_kernel_config "$kernel_dir"

    # --- IPsec / xfrm (dirtyfrag exploit) ---
    ./scripts/config --enable CONFIG_XFRM
    ./scripts/config --enable CONFIG_XFRM_USER
    ./scripts/config --enable CONFIG_XFRM_ALGO
    ./scripts/config --enable CONFIG_XFRM_STATISTICS
    ./scripts/config --enable CONFIG_INET_ESP
    ./scripts/config --enable CONFIG_INET_ESP_OFFLOAD
    ./scripts/config --enable CONFIG_INET_AH
    ./scripts/config --enable CONFIG_INET6_ESP
    ./scripts/config --enable CONFIG_INET6_ESP_OFFLOAD
    ./scripts/config --enable CONFIG_INET6_AH
    ./scripts/config --enable CONFIG_NET_KEY

    # --- AF_RXRPC (dirtyfrag exploit) ---
    ./scripts/config --enable CONFIG_AF_RXRPC

    # --- io_uring (frequent CVE target) ---
    ./scripts/config --enable CONFIG_IO_URING

    # --- SCTP (network protocol, frequent CVE target) ---
    ./scripts/config --enable CONFIG_IP_SCTP

    # --- TIPC (cluster protocol, CVE target) ---
    ./scripts/config --enable CONFIG_TIPC

    # --- DCCP (datagram congestion control, CVE target) ---
    ./scripts/config --enable CONFIG_IP_DCCP

    # --- L2TP (tunneling, CVE target) ---
    ./scripts/config --enable CONFIG_L2TP
    ./scripts/config --enable CONFIG_L2TP_V3

    # --- MPLS (label switching) ---
    ./scripts/config --enable CONFIG_MPLS
    ./scripts/config --enable CONFIG_NET_MPLS_GSO
    ./scripts/config --enable CONFIG_MPLS_ROUTING
    ./scripts/config --enable CONFIG_MPLS_IPTUNNEL

    # --- MCTP (management component transport) ---
    ./scripts/config --enable CONFIG_MCTP

    # --- Additional crypto subsystems ---
    ./scripts/config --enable CONFIG_CRYPTO_USER
    ./scripts/config --enable CONFIG_CRYPTO_PCRYPT
    ./scripts/config --enable CONFIG_CRYPTO_CRYPTD
    ./scripts/config --enable CONFIG_CRYPTO_ECHAINIV
    ./scripts/config --enable CONFIG_CRYPTO_CMAC
    ./scripts/config --enable CONFIG_CRYPTO_XCBC
    ./scripts/config --enable CONFIG_CRYPTO_SHA512
    ./scripts/config --enable CONFIG_CRYPTO_SHA3
    ./scripts/config --enable CONFIG_CRYPTO_DES
    ./scripts/config --enable CONFIG_CRYPTO_CHACHA20POLY1305
    ./scripts/config --enable CONFIG_CRYPTO_NULL

    # --- Filesystems (exploit targets, useful for testing) ---
    ./scripts/config --enable CONFIG_FUSE_FS
    ./scripts/config --enable CONFIG_BTRFS_FS
    ./scripts/config --enable CONFIG_XFS_FS
    ./scripts/config --enable CONFIG_TMPFS_XATTR
    ./scripts/config --enable CONFIG_TMPFS_POSIX_ACL
    ./scripts/config --enable CONFIG_HUGETLBFS
    ./scripts/config --enable CONFIG_CONFIGFS_FS
    ./scripts/config --enable CONFIG_EFIVAR_FS

    # --- Network filesystems ---
    ./scripts/config --enable CONFIG_NFS_FS
    ./scripts/config --enable CONFIG_NFS_V4
    ./scripts/config --enable CONFIG_NFSD
    ./scripts/config --enable CONFIG_CIFS
    ./scripts/config --enable CONFIG_9P_FS
    ./scripts/config --enable CONFIG_NET_9P
    ./scripts/config --enable CONFIG_NET_9P_VIRTIO

    # --- kTLS (kernel TLS offload) ---
    ./scripts/config --enable CONFIG_TLS

    # --- Netlink and diagnostics ---
    ./scripts/config --enable CONFIG_INET_DIAG
    ./scripts/config --enable CONFIG_INET_TCP_DIAG
    ./scripts/config --enable CONFIG_INET_UDP_DIAG
    ./scripts/config --enable CONFIG_INET_MPTCP_DIAG
    ./scripts/config --enable CONFIG_SOCK_DIAG

    # --- MPTCP (multipath TCP) ---
    ./scripts/config --enable CONFIG_MPTCP

    # --- Additional netfilter / conntrack ---
    ./scripts/config --enable CONFIG_NF_CONNTRACK_FTP
    ./scripts/config --enable CONFIG_NF_CONNTRACK_TFTP
    ./scripts/config --enable CONFIG_NF_CONNTRACK_SIP
    ./scripts/config --enable CONFIG_NF_CT_NETLINK
    ./scripts/config --enable CONFIG_NETFILTER_XT_TARGET_NFLOG
    ./scripts/config --enable CONFIG_NETFILTER_XT_TARGET_TPROXY
    ./scripts/config --enable CONFIG_NETFILTER_XT_TARGET_CT
    ./scripts/config --enable CONFIG_NETFILTER_XT_MATCH_SOCKET

    # --- Traffic control / queueing (used in net exploits) ---
    ./scripts/config --enable CONFIG_NET_SCHED
    ./scripts/config --enable CONFIG_NET_CLS
    ./scripts/config --enable CONFIG_NET_CLS_U32
    ./scripts/config --enable CONFIG_NET_CLS_ROUTE4
    ./scripts/config --enable CONFIG_NET_CLS_FW
    ./scripts/config --enable CONFIG_NET_CLS_CGROUP
    ./scripts/config --enable CONFIG_NET_SCH_HTB
    ./scripts/config --enable CONFIG_NET_SCH_INGRESS
    ./scripts/config --enable CONFIG_NET_SCH_NETEM
    ./scripts/config --enable CONFIG_NET_SCH_FQ_CODEL

    # --- Multicast / IGMP ---
    ./scripts/config --enable CONFIG_IP_MULTICAST
    ./scripts/config --enable CONFIG_IP_MROUTE
    ./scripts/config --enable CONFIG_IPV6_MROUTE

    # --- Tunneling protocols ---
    ./scripts/config --enable CONFIG_NET_IPIP
    ./scripts/config --enable CONFIG_NET_IP_TUNNEL
    ./scripts/config --enable CONFIG_IPV6_SIT
    ./scripts/config --enable CONFIG_IPV6_TUNNEL
    ./scripts/config --enable CONFIG_IPV6_GRE
    ./scripts/config --enable CONFIG_GRE

    # --- Wireless regulatory (cfg80211 base, needed for some net testing) ---
    ./scripts/config --enable CONFIG_WIRELESS
    ./scripts/config --enable CONFIG_CFG80211

    # --- USB/IP (userspace USB, sometimes exploitable) ---
    ./scripts/config --enable CONFIG_USBIP_CORE
    ./scripts/config --enable CONFIG_USBIP_VHCI_HCD

    # --- Security subsystems (AppArmor, SELinux for testing profiles) ---
    ./scripts/config --enable CONFIG_SECURITY
    ./scripts/config --enable CONFIG_SECURITYFS
    ./scripts/config --enable CONFIG_SECURITY_APPARMOR
    ./scripts/config --enable CONFIG_SECURITY_SELINUX
    ./scripts/config --enable CONFIG_SECURITY_LANDLOCK
    ./scripts/config --enable CONFIG_LSM

    # --- Audit ---
    ./scripts/config --enable CONFIG_AUDIT
    ./scripts/config --enable CONFIG_AUDITSYSCALL

    # --- Tracing / debugging (useful for exploit development) ---
    ./scripts/config --enable CONFIG_FTRACE
    ./scripts/config --enable CONFIG_KPROBES
    ./scripts/config --enable CONFIG_KPROBE_EVENTS
    ./scripts/config --enable CONFIG_UPROBE_EVENTS
    ./scripts/config --enable CONFIG_TRACEPOINTS
    ./scripts/config --enable CONFIG_BPF_EVENTS

    # --- userfaultfd (used in many race condition exploits) ---
    ./scripts/config --enable CONFIG_USERFAULTFD

    # --- Misc subsystems commonly present on Ubuntu ---
    ./scripts/config --enable CONFIG_WATCH_QUEUE
    ./scripts/config --enable CONFIG_KEY_DH_OPERATIONS
    ./scripts/config --enable CONFIG_KEYS
    ./scripts/config --enable CONFIG_POSIX_MQUEUE
    ./scripts/config --enable CONFIG_INOTIFY_USER
    ./scripts/config --enable CONFIG_FANOTIFY
    ./scripts/config --enable CONFIG_EPOLL
    ./scripts/config --enable CONFIG_SIGNALFD
    ./scripts/config --enable CONFIG_TIMERFD
    ./scripts/config --enable CONFIG_EVENTFD
    ./scripts/config --enable CONFIG_SHMEM

    # --- Resolve all dependencies ---
    make olddefconfig

    log_info "Security profile applied — verifying key options..."

    local security_opts=(
        CONFIG_INET_ESP CONFIG_INET6_ESP CONFIG_AF_RXRPC
        CONFIG_IO_URING CONFIG_IP_SCTP CONFIG_XFRM
        CONFIG_FUSE_FS CONFIG_USERFAULTFD CONFIG_FTRACE
    )
    for opt in "${security_opts[@]}"; do
        if grep -q "^${opt}=y" .config || grep -q "^${opt}=m" .config; then
            log_info "  $opt: enabled"
        else
            log_warn "  $opt: NOT enabled (may have unmet dependencies)"
        fi
    done
}

build_kernel() {
    local kernel_dir="$1"
    local nproc="$(nproc)"

    log_info "Building kernel with $nproc parallel jobs..."
    log_info "This may take 10-30 minutes depending on your system."

    cd "$kernel_dir"
    make -j"$nproc" vmlinux

    if [ ! -f vmlinux ]; then
        log_error "Kernel build failed - vmlinux not found"
        exit 1
    fi

    log_info "Kernel build complete"
}

install_kernel() {
    local kernel_dir="$1"
    local dest="$OUTPUT_DIR/$NAME"

    log_info "Installing kernel to $dest"

    mkdir -p "$OUTPUT_DIR"
    cp "$kernel_dir/vmlinux" "$dest"

    local size=$(du -h "$dest" | cut -f1)
    log_info "Kernel installed: $dest ($size)"
}

cleanup() {
    if [ "$CLEANUP" = true ] && [ -n "$BUILD_DIR" ] && [ -d "$BUILD_DIR" ]; then
        log_info "Cleaning up build directory..."
        rm -rf "$BUILD_DIR"
    fi
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --version)
            VERSION="$2"
            shift 2
            ;;
        --name)
            NAME="$2"
            shift 2
            ;;
        --config-profile)
            CONFIG_PROFILE="$2"
            shift 2
            ;;
        --output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --no-cleanup)
            CLEANUP=false
            shift
            ;;
        --help|-h)
            usage
            ;;
        *)
            log_error "Unknown option: $1"
            usage
            ;;
    esac
done

# Validate arguments
if [ -z "$VERSION" ]; then
    log_error "--version is required"
    usage
fi

if [ -z "$NAME" ]; then
    log_error "--name is required"
    usage
fi

case "$CONFIG_PROFILE" in
    default|security) ;;
    *)
        log_error "Unknown config profile: $CONFIG_PROFILE"
        log_info "Supported profiles: default, security"
        exit 1
        ;;
esac

# Check if running as root (needed for some operations)
if [ "$(id -u)" -ne 0 ]; then
    log_warn "Running as non-root user. You may need root for final installation."
fi

# Create build directory
BUILD_DIR="$(mktemp -d -t vmm-kernel-build-XXXXXX)"
trap cleanup EXIT

log_info "Build directory: $BUILD_DIR"
log_info "Target kernel version: $VERSION"
log_info "Output name: $NAME"
log_info "Config profile: $CONFIG_PROFILE"

# Main build process
check_dependencies

KERNEL_URL="$(get_kernel_url "$VERSION")"
KERNEL_DIR="$(download_kernel "$KERNEL_URL")"

case "$CONFIG_PROFILE" in
    security)
        create_security_kernel_config "$KERNEL_DIR"
        ;;
    *)
        create_kernel_config "$KERNEL_DIR"
        ;;
esac
build_kernel "$KERNEL_DIR"
install_kernel "$KERNEL_DIR"

log_info "Kernel '$NAME' built and installed successfully!"
echo ""
echo "Use it with: vmm create myvm --kernel $NAME"
