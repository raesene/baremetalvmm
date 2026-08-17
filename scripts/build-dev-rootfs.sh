#!/bin/bash
#
# build-dev-rootfs.sh - Build a Firecracker-compatible rootfs with development tools
#
# This script creates an ext4 rootfs image pre-loaded with development tools
# for use with Firecracker microVMs.
#
# Usage: build-dev-rootfs.sh --name <name> [--output <dir>] [--size <MB>] [--base-image <image>]
#
# Requires: Docker, root, mkfs.ext4, tar, mount, chroot, curl, git
#
# === Adding or updating tools ===
#
# APT packages:
#   Add to the apt-get install list in install_dev_tools().
#
# Binary tools (downloaded from GitHub/URLs):
#   1. Add a version variable in the "Tool versions" block below.
#   2. Add a download+install block in install_binary_tools().
#      Use: curl --retry 3 --retry-delay 5 -fSL <url> for downloads.
#      Place binaries at: $rootfs_dir/usr/local/bin/<name>
#
# Bumping versions:
#   Change the relevant _VERSION variable below, commit, and push.
#   The CI workflow triggers on changes to this file.
#

set -e

# ============================================================
# Tool versions — bump these to update pinned binary releases
# ============================================================
# TODO: Add tool versions as we decide what to include

# Default values
OUTPUT_DIR="/var/lib/vmm/images/rootfs"
NAME=""
SIZE_MB=4096
BASE_IMAGE="ubuntu:24.04"
CLEANUP=true
TMP_DIR=""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
    cat <<EOF
Usage: $0 --name <name> [--output <dir>] [--size <MB>] [--base-image <image>]

Build a Firecracker-compatible rootfs image with development tools.

Options:
  --name NAME           Name for the output rootfs file (required)
  --output DIR          Output directory (default: /var/lib/vmm/images/rootfs)
  --size MB             Image size in MB (default: 4096)
  --base-image IMAGE    Docker base image (default: ubuntu:24.04)
  --no-cleanup          Keep temporary directory after completion
  --help                Show this help message

Examples:
  $0 --name dev-rootfs.ext4 --size 4096
  $0 --name dev-rootfs.ext4 --base-image ubuntu:24.04 --output /tmp
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
    local commands=("docker" "mkfs.ext4" "tar" "mount" "chroot" "curl" "git")
    for cmd in "${commands[@]}"; do
        if ! command -v "$cmd" &>/dev/null; then
            missing+=("$cmd")
        fi
    done

    if [ ${#missing[@]} -ne 0 ]; then
        log_error "Missing required commands: ${missing[*]}"
        exit 1
    fi

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

    docker export "$CONTAINER_ID" | tar -xf - -C "$export_dir"

    docker rm "$CONTAINER_ID" >/dev/null
    CONTAINER_ID=""

    log_info "Docker image exported successfully"
}

install_apt_packages() {
    local rootfs_dir="$1"

    log_info "Installing APT packages (base system, development tools)..."
    log_info "This may take several minutes..."

    # Mount required filesystems for chroot
    for dir in dev dev/pts proc sys run tmp var/run var/log; do
        mkdir -p "$rootfs_dir/$dir"
    done

    mount --bind /dev "$rootfs_dir/dev"
    mount --bind /dev/pts "$rootfs_dir/dev/pts"
    mount -t proc proc "$rootfs_dir/proc"
    mount -t sysfs sysfs "$rootfs_dir/sys"

    # Copy resolv.conf for DNS during package installation
    rm -f "$rootfs_dir/etc/resolv.conf"
    cp /etc/resolv.conf "$rootfs_dir/etc/resolv.conf" 2>/dev/null || \
        echo "nameserver 8.8.8.8" > "$rootfs_dir/etc/resolv.conf"

    chroot "$rootfs_dir" /bin/bash -c "
        export DEBIAN_FRONTEND=noninteractive
        export PATH=/usr/sbin:/usr/bin:/sbin:/bin

        apt-get update -qq
        apt-get install -qq -y --no-install-recommends \
            systemd \
            systemd-sysv \
            openssh-server \
            openssh-client \
            iproute2 \
            iputils-ping \
            dbus \
            wget \
            curl \
            git \
            mount \
            openssl \
            unzip \
            ca-certificates \
            software-properties-common \
            apt-transport-https \
            gpg \
            lsb-release \
            vim \
            nano \
            net-tools \
            dnsutils \
            python3 \
            python-is-python3 \
            build-essential \
            make \
            jq \
            tmux \
            htop \
            tree \
            strace \
            ltrace \
            file \
            less

        # Add NodeSource repository for Node.js 22
        curl -fsSL https://deb.nodesource.com/setup_22.x | bash -
        apt-get install -qq -y --no-install-recommends nodejs

        # Add Docker official repository
        install -m 0755 -d /etc/apt/keyrings
        curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
        chmod a+r /etc/apt/keyrings/docker.asc

        echo \"deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu \$(lsb_release -cs) stable\" > /etc/apt/sources.list.d/docker.list

        apt-get update -qq
        apt-get install -qq -y --no-install-recommends \
            docker-ce \
            docker-ce-cli \
            containerd.io

        # Enable Docker to start on boot
        systemctl enable docker 2>/dev/null || true
        systemctl enable containerd 2>/dev/null || true

        apt-get clean
        rm -rf /var/lib/apt/lists/*
    "

    # Unmount chroot filesystems
    umount "$rootfs_dir/sys"
    umount "$rootfs_dir/proc"
    umount "$rootfs_dir/dev/pts"
    umount "$rootfs_dir/dev"

    log_info "APT packages installed successfully"
}

install_binary_tools() {
    local rootfs_dir="$1"
    local bin_dir="$rootfs_dir/usr/local/bin"

    mkdir -p "$bin_dir"

    log_info "Installing binary tools..."

    # Go (latest stable from go.dev)
    log_info "  Fetching latest Go version..."
    local go_version
    go_version=$(curl --retry 3 --retry-delay 5 -fsSL "https://go.dev/VERSION?m=text" | head -1)
    log_info "  Installing ${go_version}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://go.dev/dl/${go_version}.linux-amd64.tar.gz" \
        | tar -xz -C "$rootfs_dir/usr/local"
    # Add Go to PATH for all users
    cat > "$rootfs_dir/etc/profile.d/golang.sh" <<'GOEOF'
export PATH=$PATH:/usr/local/go/bin:$HOME/go/bin
export GOPATH=$HOME/go
GOEOF
    chmod +x "$rootfs_dir/etc/profile.d/golang.sh"

    # pi coding agent (installed via npm inside chroot, needs Node.js)
    log_info "  Installing pi coding agent..."
    mount --bind /dev "$rootfs_dir/dev"
    mount --bind /dev/pts "$rootfs_dir/dev/pts"
    mount -t proc proc "$rootfs_dir/proc"
    mount -t sysfs sysfs "$rootfs_dir/sys"
    rm -f "$rootfs_dir/etc/resolv.conf"
    cp /etc/resolv.conf "$rootfs_dir/etc/resolv.conf" 2>/dev/null || \
        echo "nameserver 8.8.8.8" > "$rootfs_dir/etc/resolv.conf"
    chroot "$rootfs_dir" /bin/bash -c "
        export PATH=/usr/sbin:/usr/bin:/sbin:/bin:/usr/local/bin
        npm install -g --ignore-scripts @earendil-works/pi-coding-agent
    "
    umount "$rootfs_dir/sys"
    umount "$rootfs_dir/proc"
    umount "$rootfs_dir/dev/pts"
    umount "$rootfs_dir/dev"

    # starship
    log_info "  Installing starship (latest)..."
    local starship_tmp
    starship_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/starship/starship/releases/latest/download/starship-x86_64-unknown-linux-musl.tar.gz" \
        | tar -xz -C "$starship_tmp"
    mv "$starship_tmp/starship" "$bin_dir/starship"
    chmod +x "$bin_dir/starship"
    rm -rf "$starship_tmp"

    log_info "Binary tools installed successfully"
}

install_bundled_files() {
    local rootfs_dir="$1"
    local files_dir="$SCRIPT_DIR/dev-rootfs-files"

    log_info "Installing bundled config files..."

    # Starship prompt config
    mkdir -p "$rootfs_dir/root/.config"
    if [ -f "$files_dir/config/starship.toml" ]; then
        cp "$files_dir/config/starship.toml" "$rootfs_dir/root/.config/starship.toml"
    else
        # Use the security rootfs starship config as fallback
        cp "$SCRIPT_DIR/security-rootfs-files/config/starship.toml" \
            "$rootfs_dir/root/.config/starship.toml"
    fi

    # Add starship init to bashrc
    echo 'eval "$(starship init bash)"' >> "$rootfs_dir/root/.bashrc"

    # Auto-attach to tmux session on login
    cat >> "$rootfs_dir/root/.bashrc" <<'TMUXEOF'

# Automatically attach to 'main' tmux session, or create it if it doesn't exist
if [ -z "$TMUX" ] && [ -n "$PS1" ]; then
    tmux attach-session -t main || tmux new-session -s main
fi
TMUXEOF

    # SSH authorized key for remote access
    mkdir -p "$rootfs_dir/root/.ssh"
    chmod 700 "$rootfs_dir/root/.ssh"
    cat >> "$rootfs_dir/root/.ssh/authorized_keys" <<'SSHEOF'
ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAACAQDQS08ZhjPPZTMtXQEftREeXD4D9Jf5FoGRKj29BerWn2isVLWMPnRNKuBt7uM/cPIjM32iFfCW1ykZdLmk7YcKXX0iDiS5i7EV7aQ5T0o0XRNnK3+p7QQTXeoKonbx3rkvvVaR/0/9DLHzq4mmN5sECMAff7/N6+z+rsxoJ/VgOV09HOVWnDoXDePtx3bgGrq02g2AEbkw4gCVI/auJ5ZND12mQTR5KcYDlyZW1r4GRveJRoReHr2pek6IVrY+DxHJosntECw+UhmWMelz4sMSwdY56Tx+jVZDfFwW1u3tw3dEU5B+qvp7PB1E+4A5NGykvda46NUaNfBjBUD0DVhQs/BUhBUHUjvPWbL//hPx0BkovpC6Q2AKVorfdtDetzID3EuTrYD8SHX8wlGxcVDANH8CDUI6hlUIoSSH1Gk4tqlLwM3YQvao/wTzzmc500juNWBYAP3RhsA/0p6NhDUpUqAyocIGWtO+exTy+8sQN2iyO0OpWpaGkzDQP4O46F1P+BlSRrzb3DEi9JFWj0hip5OTGPDnVZjDHy66fRdJZj4nWz78MBvr8Y8e+SinaFmpIPt/yI+L5QVfM7TdhH8rvi5+wssAdnBvDbek00oCG9wUW+pDFJ1I1nx6WEcCCOtyDnj2cTaGDYkTGeGQfjpaRZMgeeR7XiZPLP191VhXPw== rorym@mccune.org.uk
SSHEOF
    chmod 600 "$rootfs_dir/root/.ssh/authorized_keys"

    # Marker file
    echo "dev" > "$rootfs_dir/etc/vmm-dev-rootfs"

    log_info "Bundled files installed successfully"
}

configure_base_rootfs() {
    local rootfs_dir="$1"

    log_info "Configuring base rootfs for Firecracker..."

    # Configure serial console on ttyS0
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

    log_info "Base rootfs configuration complete"
}

create_ext4_image() {
    local image_path="$1"
    local source_dir="$2"
    local size_mb="$3"

    log_info "Creating ${size_mb}MB ext4 image..."

    truncate -s "${size_mb}M" "$image_path"
    mkfs.ext4 -F -L rootfs "$image_path" >/dev/null 2>&1

    local mount_point="$TMP_DIR/mnt"
    mkdir -p "$mount_point"
    mount -o loop "$image_path" "$mount_point"

    log_info "Copying files into image..."
    tar -cf - -C "$source_dir" . | tar -xf - -C "$mount_point"

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

if [ "$(id -u)" -ne 0 ]; then
    log_error "This script must be run as root"
    exit 1
fi

# Create temporary directory and set trap
TMP_DIR="$(mktemp -d -t vmm-dev-rootfs-build-XXXXXX)"
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
install_apt_packages "$EXPORT_DIR"
install_binary_tools "$EXPORT_DIR"
install_bundled_files "$EXPORT_DIR"
configure_base_rootfs "$EXPORT_DIR"

IMAGE_PATH="$TMP_DIR/$NAME"
create_ext4_image "$IMAGE_PATH" "$EXPORT_DIR" "$SIZE_MB"

# Compress the image
log_info "Compressing image with gzip..."
gzip -1 "$IMAGE_PATH"

# Copy to output directory
mkdir -p "$OUTPUT_DIR"
cp "$IMAGE_PATH.gz" "$OUTPUT_DIR/${NAME}.gz"

local_size=$(du -h "$OUTPUT_DIR/${NAME}.gz" | cut -f1)
log_info "Dev rootfs image built successfully!"
log_info "  Output: $OUTPUT_DIR/${NAME}.gz ($local_size)"
log_info "  Pre-installed tools:"
log_info "    Go (latest), Node.js 22, Docker Engine, build-essential, make, git"
log_info "    pi coding agent, python3, jq, tmux, htop, vim, nano, curl, wget"
log_info "    strace, ltrace, tree, file, less"
log_info "    starship prompt"
echo ""
echo "To use this rootfs with vmm:"
echo "  gunzip -c $OUTPUT_DIR/${NAME}.gz > /var/lib/vmm/images/rootfs/dev-rootfs.ext4"
echo "  vmm create devvm --image dev-rootfs --ssh-key ~/.ssh/id_ed25519.pub"
