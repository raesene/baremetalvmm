#!/bin/bash
#
# build-security-rootfs.sh - Build a Firecracker-compatible rootfs with security and container tools
#
# This script creates an ext4 rootfs image pre-loaded with security testing,
# container, and Kubernetes assessment tools for use with Firecracker microVMs.
#
# Usage: build-security-rootfs.sh --name <name> [--output <dir>] [--size <MB>] [--base-image <image>]
#
# Requires: Docker, root, mkfs.ext4, tar, mount, chroot, curl, git
#
# === Adding or updating tools ===
#
# APT packages:
#   Add to the apt-get install list in install_security_tools().
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
KUBECTL_VERSION="1.30.3"
AMICONTAINED_VERSION="0.4.9"
RBAC_TOOL_VERSION="1.19.0"
KDIGGER_VERSION="1.5.1"
NERDCTL_VERSION="1.7.6"
CRICTL_VERSION="1.30.1"
JWT_CLI_VERSION="6.2.0"
K9S_VERSION="0.32.7"
KUBELETCTL_VERSION="1.13"
KIND_VERSION="0.31.0"
OC_VERSION="4.20"

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

Build a Firecracker-compatible rootfs image with security and container tools.

Options:
  --name NAME           Name for the output rootfs file (required)
  --output DIR          Output directory (default: /var/lib/vmm/images/rootfs)
  --size MB             Image size in MB (default: 4096)
  --base-image IMAGE    Docker base image (default: ubuntu:24.04)
  --no-cleanup          Keep temporary directory after completion
  --help                Show this help message

Examples:
  $0 --name security-rootfs.ext4 --size 4096
  $0 --name security-rootfs.ext4 --base-image ubuntu:24.04 --output /tmp
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

    log_info "Installing APT packages (base system, security tools, Docker Engine)..."
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
            iputils-arping \
            dbus \
            wget \
            curl \
            git \
            mount \
            openssl \
            unzip \
            traceroute \
            dnsutils \
            tcpdump \
            libcap2-bin \
            ruby \
            whois \
            socat \
            apt-transport-https \
            ca-certificates \
            software-properties-common \
            python3 \
            python-is-python3 \
            vim \
            nano \
            net-tools \
            nmap \
            gpg \
            lsb-release

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

    # kubectl
    log_info "  Installing kubectl ${KUBECTL_VERSION}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://dl.k8s.io/v${KUBECTL_VERSION}/bin/linux/amd64/kubectl" \
        -o "$bin_dir/kubectl"
    chmod +x "$bin_dir/kubectl"

    # amicontained
    log_info "  Installing amicontained ${AMICONTAINED_VERSION}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/genuinetools/amicontained/releases/download/v${AMICONTAINED_VERSION}/amicontained-linux-amd64" \
        -o "$bin_dir/amicontained"
    chmod +x "$bin_dir/amicontained"

    # rbac-tool
    log_info "  Installing rbac-tool ${RBAC_TOOL_VERSION}..."
    local rbac_tmp
    rbac_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/alcideio/rbac-tool/releases/download/v${RBAC_TOOL_VERSION}/rbac-tool_v${RBAC_TOOL_VERSION}_linux_amd64.tar.gz" \
        | tar -xz -C "$rbac_tmp"
    mv "$rbac_tmp/rbac-tool" "$bin_dir/rbac-tool"
    chmod +x "$bin_dir/rbac-tool"
    rm -rf "$rbac_tmp"

    # kdigger
    log_info "  Installing kdigger ${KDIGGER_VERSION}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/quarkslab/kdigger/releases/download/v${KDIGGER_VERSION}/kdigger-linux-amd64" \
        -o "$bin_dir/kdigger"
    chmod +x "$bin_dir/kdigger"

    # nerdctl
    log_info "  Installing nerdctl ${NERDCTL_VERSION}..."
    local nerdctl_tmp
    nerdctl_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/containerd/nerdctl/releases/download/v${NERDCTL_VERSION}/nerdctl-${NERDCTL_VERSION}-linux-amd64.tar.gz" \
        | tar -xz -C "$nerdctl_tmp"
    mv "$nerdctl_tmp/nerdctl" "$bin_dir/nerdctl"
    chmod +x "$bin_dir/nerdctl"
    rm -rf "$nerdctl_tmp"

    # crictl
    log_info "  Installing crictl ${CRICTL_VERSION}..."
    local crictl_tmp
    crictl_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/kubernetes-sigs/cri-tools/releases/download/v${CRICTL_VERSION}/crictl-v${CRICTL_VERSION}-linux-amd64.tar.gz" \
        | tar -xz -C "$crictl_tmp"
    mv "$crictl_tmp/crictl" "$bin_dir/crictl"
    chmod +x "$bin_dir/crictl"
    rm -rf "$crictl_tmp"

    # jwt-cli
    log_info "  Installing jwt-cli ${JWT_CLI_VERSION}..."
    local jwt_tmp
    jwt_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/mike-engel/jwt-cli/releases/download/${JWT_CLI_VERSION}/jwt-linux.tar.gz" \
        | tar -xz -C "$jwt_tmp"
    mv "$jwt_tmp/jwt" "$bin_dir/jwt"
    chmod +x "$bin_dir/jwt"
    rm -rf "$jwt_tmp"

    # k9s
    log_info "  Installing k9s ${K9S_VERSION}..."
    local k9s_tmp
    k9s_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/derailed/k9s/releases/download/v${K9S_VERSION}/k9s_Linux_amd64.tar.gz" \
        | tar -xz -C "$k9s_tmp"
    mv "$k9s_tmp/k9s" "$bin_dir/k9s"
    chmod +x "$bin_dir/k9s"
    rm -rf "$k9s_tmp"

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

    # kubectx and kubens
    log_info "  Installing kubectx/kubens..."
    git clone --depth 1 https://github.com/ahmetb/kubectx "$rootfs_dir/opt/kubectx"
    ln -sf /opt/kubectx/kubectx "$bin_dir/kubectx"
    ln -sf /opt/kubectx/kubens "$bin_dir/kubens"

    # kubeletctl
    log_info "  Installing kubeletctl ${KUBELETCTL_VERSION}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://github.com/cyberark/kubeletctl/releases/download/v${KUBELETCTL_VERSION}/kubeletctl_linux_amd64" \
        -o "$bin_dir/kubeletctl"
    chmod +x "$bin_dir/kubeletctl"

    # kind
    log_info "  Installing kind ${KIND_VERSION}..."
    curl --retry 3 --retry-delay 5 -fSL \
        "https://kind.sigs.k8s.io/dl/v${KIND_VERSION}/kind-linux-amd64" \
        -o "$bin_dir/kind"
    chmod +x "$bin_dir/kind"

    # oc (OpenShift CLI) — downloaded from the Red Hat mirror; no subscription required
    log_info "  Installing oc (OpenShift CLI) ${OC_VERSION}..."
    local oc_tmp
    oc_tmp=$(mktemp -d)
    curl --retry 3 --retry-delay 5 -fSL \
        "https://mirror.openshift.com/pub/openshift-v4/clients/ocp/stable-${OC_VERSION}/openshift-client-linux.tar.gz" \
        | tar -xz -C "$oc_tmp"
    mv "$oc_tmp/oc" "$bin_dir/oc"
    chmod +x "$bin_dir/oc"
    rm -rf "$oc_tmp"

    log_info "Binary tools installed successfully"
}

install_bundled_files() {
    local rootfs_dir="$1"
    local files_dir="$SCRIPT_DIR/security-rootfs-files"

    log_info "Installing bundled config files, manifests, and scripts..."

    # Starship prompt config
    mkdir -p "$rootfs_dir/root/.config"
    cp "$files_dir/config/starship.toml" "$rootfs_dir/root/.config/starship.toml"

    # Add starship init to bashrc
    echo 'eval "$(starship init bash)"' >> "$rootfs_dir/root/.bashrc"

    # K8s security manifests
    mkdir -p "$rootfs_dir/manifests"
    cp "$files_dir/manifests/"*.yml "$rootfs_dir/manifests/"

    # Security scripts
    mkdir -p "$rootfs_dir/scripts"
    cp "$files_dir/scripts/"* "$rootfs_dir/scripts/"
    chmod +x "$rootfs_dir/scripts/"*

    # SetUID bash for security training
    cp "$rootfs_dir/bin/bash" "$rootfs_dir/bin/setuidbash"
    chmod 4755 "$rootfs_dir/bin/setuidbash"

    # Marker file
    echo "security" > "$rootfs_dir/etc/vmm-security-rootfs"

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
TMP_DIR="$(mktemp -d -t vmm-security-rootfs-build-XXXXXX)"
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
log_info "Security rootfs image built successfully!"
log_info "  Output: $OUTPUT_DIR/${NAME}.gz ($local_size)"
log_info "  Pre-installed tools:"
log_info "    kubectl ${KUBECTL_VERSION}, Docker Engine (apt), nerdctl ${NERDCTL_VERSION}, crictl ${CRICTL_VERSION}"
log_info "    nmap, tcpdump, socat, dnsutils, traceroute, whois"
log_info "    amicontained ${AMICONTAINED_VERSION}, kdigger ${KDIGGER_VERSION}, kubeletctl ${KUBELETCTL_VERSION}"
log_info "    rbac-tool ${RBAC_TOOL_VERSION}, k9s ${K9S_VERSION}, jwt-cli ${JWT_CLI_VERSION}"
log_info "    kind ${KIND_VERSION}, starship prompt, kubectx/kubens, oc ${OC_VERSION}"
echo ""
echo "To use this rootfs with vmm:"
echo "  gunzip -c $OUTPUT_DIR/${NAME}.gz > /var/lib/vmm/images/rootfs/security-rootfs.ext4"
echo "  vmm create secvm --image security-rootfs --ssh-key ~/.ssh/id_ed25519.pub"
