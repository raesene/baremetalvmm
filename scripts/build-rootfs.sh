#!/bin/bash
#
# build-rootfs.sh - Build a Firecracker-compatible Ubuntu rootfs image
#
# This script creates an ext4 rootfs image from a Docker base image,
# configured for Firecracker microVMs with systemd, SSH, and networking.
#
# Usage: build-rootfs.sh --name <name> [--output <dir>] [--size <MB>] [--base-image <image>]
#
# Requires: Docker, root, mkfs.ext4, tar, mount, chroot
#

set -e

# Default values
OUTPUT_DIR="/var/lib/vmm/images/rootfs"
NAME=""
SIZE_MB=512
BASE_IMAGE="ubuntu:24.04"
CLEANUP=true
TMP_DIR=""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

usage() {
    cat <<EOF
Usage: $0 --name <name> [--output <dir>] [--size <MB>] [--base-image <image>]

Build a Firecracker-compatible rootfs image from a Docker base image.

Options:
  --name NAME           Name for the output rootfs file (required)
  --output DIR          Output directory (default: /var/lib/vmm/images/rootfs)
  --size MB             Image size in MB (default: 512)
  --base-image IMAGE    Docker base image (default: ubuntu:24.04)
  --no-cleanup          Keep temporary directory after completion
  --help                Show this help message

Examples:
  $0 --name rootfs.ext4 --size 512
  $0 --name rootfs.ext4 --base-image ubuntu:24.04 --output /tmp
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

# Cleanup function for trap - unmounts bind mounts in reverse order
cleanup() {
    local exit_code=$?

    if [ -n "$TMP_DIR" ] && [ -d "$TMP_DIR" ]; then
        local rootfs_dir="$TMP_DIR/rootfs"
        local mount_point="$TMP_DIR/mnt"

        # Unmount ext4 image if mounted
        if mountpoint -q "$mount_point" 2>/dev/null; then
            umount "$mount_point" 2>/dev/null || umount -l "$mount_point" 2>/dev/null || true
        fi

        # Unmount chroot bind mounts in reverse order
        for mnt in "$rootfs_dir/sys" "$rootfs_dir/proc" "$rootfs_dir/dev/pts" "$rootfs_dir/dev"; do
            if mountpoint -q "$mnt" 2>/dev/null; then
                umount -l "$mnt" 2>/dev/null || true
            fi
        done

        # Remove container if it exists
        if [ -n "$CONTAINER_ID" ]; then
            docker rm "$CONTAINER_ID" 2>/dev/null || true
        fi

        if [ "$CLEANUP" = true ]; then
            log_info "Cleaning up temporary directory..."
            rm -rf "$TMP_DIR"
        else
            log_info "Temporary directory preserved at: $TMP_DIR"
        fi
    fi

    exit $exit_code
}

check_dependencies() {
    log_info "Checking dependencies..."

    local missing=()

    # Check for required commands
    local commands=("docker" "mkfs.ext4" "tar" "mount" "chroot")
    for cmd in "${commands[@]}"; do
        if ! command -v "$cmd" &>/dev/null; then
            missing+=("$cmd")
        fi
    done

    if [ ${#missing[@]} -ne 0 ]; then
        log_error "Missing required commands: ${missing[*]}"
        exit 1
    fi

    # Check Docker is running
    if ! docker info &>/dev/null; then
        log_error "Docker is not running or not accessible"
        exit 1
    fi

    log_info "All dependencies satisfied"
}

export_docker_image() {
    local image="$1"
    local export_dir="$2"

    log_info "Pulling Docker image '$image'..."
    docker pull "$image"

    log_info "Exporting Docker image..."
    CONTAINER_ID=$(docker create "$image")

    # Export and extract in one step
    docker export "$CONTAINER_ID" | tar -xf - -C "$export_dir"

    # docker export always adds /.dockerenv. Left in the image, it makes
    # systemd-detect-virt report "docker" inside the VM, so systemd skips
    # container-unsafe setup (securityfs is not mounted) and AppArmor refuses
    # to start.
    rm -f "$export_dir/.dockerenv"

    docker rm "$CONTAINER_ID" >/dev/null
    CONTAINER_ID=""

    log_info "Docker image exported successfully"
}

configure_rootfs() {
    local rootfs_dir="$1"

    log_info "Configuring rootfs for Firecracker..."

    # Create necessary directories
    for dir in dev dev/pts proc sys run tmp var/run var/log; do
        mkdir -p "$rootfs_dir/$dir"
    done

    # Mount required filesystems for chroot
    mount --bind /dev "$rootfs_dir/dev"
    mount --bind /dev/pts "$rootfs_dir/dev/pts"
    mount -t proc proc "$rootfs_dir/proc"
    mount -t sysfs sysfs "$rootfs_dir/sys"

    # Copy resolv.conf for DNS during package installation
    rm -f "$rootfs_dir/etc/resolv.conf"
    cp /etc/resolv.conf "$rootfs_dir/etc/resolv.conf" 2>/dev/null || \
        echo "nameserver 8.8.8.8" > "$rootfs_dir/etc/resolv.conf"

    # Install required packages
    log_info "Installing systemd, openssh-server, and networking tools..."
    log_info "This may take a few minutes..."

    chroot "$rootfs_dir" /bin/bash -c "
        export DEBIAN_FRONTEND=noninteractive
        export PATH=/usr/sbin:/usr/bin:/sbin:/bin

        apt-get update -qq
        apt-get install -qq -y --no-install-recommends \
            systemd \
            systemd-sysv \
            openssh-server \
            iproute2 \
            iputils-ping \
            dbus

        apt-get clean
        rm -rf /var/lib/apt/lists/*
    "

    # Unmount chroot filesystems
    umount "$rootfs_dir/sys"
    umount "$rootfs_dir/proc"
    umount "$rootfs_dir/dev/pts"
    umount "$rootfs_dir/dev"

    # Configure serial console on ttyS0
    log_info "Configuring serial console..."
    mkdir -p "$rootfs_dir/etc/systemd/system"
    cat > "$rootfs_dir/etc/systemd/system/serial-getty@ttyS0.service" <<'SERIAL_EOF'
[Unit]
Description=Serial Console on ttyS0
After=systemd-user-sessions.service

[Service]
ExecStart=/sbin/agetty -o '-p -- \\u' --keep-baud 115200,38400,9600 ttyS0 xterm-256color
Type=idle
Restart=always
RestartSec=0
UtmpIdentifier=ttyS0
TTYPath=/dev/ttyS0
TTYReset=yes
TTYVHangup=yes

[Install]
WantedBy=multi-user.target
SERIAL_EOF

    # Enable serial console service
    local wants_dir="$rootfs_dir/etc/systemd/system/multi-user.target.wants"
    mkdir -p "$wants_dir"
    ln -sf /etc/systemd/system/serial-getty@ttyS0.service \
        "$wants_dir/serial-getty@ttyS0.service"

    # Enable SSH service
    log_info "Configuring SSH..."
    if [ -f "$rootfs_dir/lib/systemd/system/ssh.service" ]; then
        ln -sf /lib/systemd/system/ssh.service "$wants_dir/ssh.service"
    else
        ln -sf /lib/systemd/system/sshd.service "$wants_dir/ssh.service"
    fi

    # Configure SSH to allow root login with keys
    local sshd_config="$rootfs_dir/etc/ssh/sshd_config"
    if [ -f "$sshd_config" ]; then
        # Ensure PermitRootLogin is set to prohibit-password
        if grep -q "PermitRootLogin" "$sshd_config"; then
            sed -i 's/.*PermitRootLogin.*/PermitRootLogin prohibit-password/' "$sshd_config"
        else
            echo "PermitRootLogin prohibit-password" >> "$sshd_config"
        fi
    fi

    # Create /etc/fstab
    cat > "$rootfs_dir/etc/fstab" <<'FSTAB_EOF'
# /etc/fstab - VMM generated
/dev/vda / ext4 defaults 0 1
FSTAB_EOF

    # Set hostname
    echo "vmm-guest" > "$rootfs_dir/etc/hostname"

    # Configure systemd-networkd
    log_info "Configuring networking..."
    mkdir -p "$rootfs_dir/etc/systemd/network"
    cat > "$rootfs_dir/etc/systemd/network/10-eth0.network" <<'NET_EOF'
[Match]
Name=eth0

[Network]
DHCP=no
NET_EOF

    # Enable systemd-networkd
    ln -sf /lib/systemd/system/systemd-networkd.service \
        "$wants_dir/systemd-networkd.service"

    # Lock root password (SSH key login only)
    if [ -f "$rootfs_dir/etc/shadow" ]; then
        sed -i 's|^root:[^:]*:|root:*:|' "$rootfs_dir/etc/shadow"
    fi

    log_info "Rootfs configuration complete"
}

create_ext4_image() {
    local image_path="$1"
    local source_dir="$2"
    local size_mb="$3"

    log_info "Creating ${size_mb}MB ext4 image..."

    # Create sparse file
    truncate -s "${size_mb}M" "$image_path"

    # Create ext4 filesystem
    mkfs.ext4 -F -L rootfs "$image_path" >/dev/null 2>&1

    # Newer mke2fs (Ubuntu 26.04, which ubuntu-latest runners use) enables
    # orphan_file. e2fsprogs before 1.47 can't resize such a filesystem and
    # kernels before 5.15 can't mount it read-write, so hosts older than the
    # build machine couldn't use the image.
    if dumpe2fs -h "$image_path" 2>/dev/null | grep -q '^Filesystem features:.*orphan_file'; then
        tune2fs -O ^orphan_file "$image_path" >/dev/null
    fi

    # Mount the image
    local mount_point="$TMP_DIR/mnt"
    mkdir -p "$mount_point"
    mount -o loop "$image_path" "$mount_point"

    # Copy files preserving permissions
    log_info "Copying files into image..."
    tar -cf - -C "$source_dir" . | tar -xf - -C "$mount_point"

    # Unmount
    umount "$mount_point"

    log_info "ext4 image created successfully"
}

# Parse arguments
while [[ $# -gt 0 ]]; do
    case "$1" in
        --name)
            NAME="$2"
            shift 2
            ;;
        --output)
            OUTPUT_DIR="$2"
            shift 2
            ;;
        --size)
            SIZE_MB="$2"
            shift 2
            ;;
        --base-image)
            BASE_IMAGE="$2"
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
if [ -z "$NAME" ]; then
    log_error "--name is required"
    usage
fi

# Check if running as root
if [ "$(id -u)" -ne 0 ]; then
    log_error "This script must be run as root"
    exit 1
fi

# Create temporary directory and set trap
TMP_DIR="$(mktemp -d -t vmm-rootfs-build-XXXXXX)"
CONTAINER_ID=""
trap cleanup EXIT

log_info "Build directory: $TMP_DIR"
log_info "Base image: $BASE_IMAGE"
log_info "Image size: ${SIZE_MB}MB"
log_info "Output: $OUTPUT_DIR/$NAME"

# Main build process
check_dependencies

EXPORT_DIR="$TMP_DIR/rootfs"
mkdir -p "$EXPORT_DIR"

export_docker_image "$BASE_IMAGE" "$EXPORT_DIR"
configure_rootfs "$EXPORT_DIR"

IMAGE_PATH="$TMP_DIR/$NAME"
create_ext4_image "$IMAGE_PATH" "$EXPORT_DIR" "$SIZE_MB"

# Compress the image
log_info "Compressing image with gzip..."
gzip -1 "$IMAGE_PATH"

# Copy to output directory
mkdir -p "$OUTPUT_DIR"
cp "$IMAGE_PATH.gz" "$OUTPUT_DIR/${NAME}.gz"

local_size=$(du -h "$OUTPUT_DIR/${NAME}.gz" | cut -f1)
log_info "Rootfs image built and installed successfully!"
log_info "  Output: $OUTPUT_DIR/${NAME}.gz ($local_size)"
echo ""
echo "To use as default rootfs, decompress to /var/lib/vmm/images/rootfs/:"
echo "  gunzip -c $OUTPUT_DIR/${NAME}.gz > /var/lib/vmm/images/rootfs/rootfs.ext4"
