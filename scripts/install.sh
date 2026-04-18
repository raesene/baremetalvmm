#!/bin/bash
set -e

# VMM Installation Script
# This script installs the VMM binary and Firecracker

INSTALL_DIR="/usr/local/bin"
DATA_DIR="/var/lib/vmm"
GITHUB_REPO="raesene/baremetalvmm"

# Get the latest version from GitHub API, fallback to default
get_latest_version() {
    local version=""
    local api_url="https://api.github.com/repos/${GITHUB_REPO}/releases/latest"

    # Try to fetch the latest release version
    if command -v curl &> /dev/null; then
        version=$(curl -fsSL "$api_url" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"v?([^"]+)".*/\1/')
    elif command -v wget &> /dev/null; then
        version=$(wget -qO- "$api_url" 2>/dev/null | grep '"tag_name":' | sed -E 's/.*"v?([^"]+)".*/\1/')
    fi

    # Fallback to default version if API call fails
    if [ -z "$version" ]; then
        echo "0.1.0"
    else
        echo "$version"
    fi
}

# Allow override via environment variable, otherwise fetch from GitHub
VMM_VERSION="${VMM_VERSION:-$(get_latest_version)}"

echo "VMM Installer"
echo "============="

# Check for root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (sudo)"
    exit 1
fi

# Check for KVM
if [ ! -e /dev/kvm ]; then
    echo "Warning: /dev/kvm not found. KVM support is required."
    echo "Ensure your CPU supports virtualization and it's enabled in BIOS."
fi

# Download helper - tries curl first, falls back to wget
download_file() {
    local url="$1"
    local output="$2"

    if command -v curl &> /dev/null; then
        curl -fsSL -o "$output" "$url"
    elif command -v wget &> /dev/null; then
        wget -q -O "$output" "$url"
    else
        echo "Error: Neither curl nor wget is installed. Please install one of them."
        return 1
    fi
}

# Detect architecture
detect_arch() {
    local arch=$(uname -m)
    case "$arch" in
        x86_64)
            echo "amd64"
            ;;
        aarch64|arm64)
            echo "arm64"
            ;;
        *)
            echo ""
            ;;
    esac
}

# Download pre-built binary from GitHub releases
download_prebuilt() {
    local arch=$(detect_arch)
    if [ -z "$arch" ]; then
        echo "Unsupported architecture: $(uname -m)"
        return 1
    fi

    local url="https://github.com/${GITHUB_REPO}/releases/download/v${VMM_VERSION}/vmm_${VMM_VERSION}_linux_${arch}.tar.gz"
    echo "Downloading VMM v${VMM_VERSION} for linux/${arch}..."

    if download_file "$url" /tmp/vmm.tar.gz; then
        echo "Extracting..."
        tar -xzf /tmp/vmm.tar.gz -C /tmp vmm vmm-web 2>/dev/null || tar -xzf /tmp/vmm.tar.gz -C /tmp vmm
        cp /tmp/vmm "$INSTALL_DIR/vmm"
        chmod +x "$INSTALL_DIR/vmm"
        if [ -f /tmp/vmm-web ]; then
            cp /tmp/vmm-web "$INSTALL_DIR/vmm-web"
            chmod +x "$INSTALL_DIR/vmm-web"
            echo "VMM Web UI installed to $INSTALL_DIR/vmm-web"
        fi
        rm -f /tmp/vmm.tar.gz /tmp/vmm /tmp/vmm-web
        echo "VMM installed to $INSTALL_DIR/vmm"
        return 0
    else
        echo "Failed to download pre-built binary"
        rm -f /tmp/vmm.tar.gz
        return 1
    fi
}

# Build from source
build_from_source() {
    echo "Building VMM from source..."
    if command -v go &> /dev/null; then
        go build -o vmm ./cmd/vmm/
        cp vmm "$INSTALL_DIR/vmm"
        chmod +x "$INSTALL_DIR/vmm"
        echo "VMM installed to $INSTALL_DIR/vmm"

        echo "Building VMM Web UI from source..."
        go build -o vmm-web ./cmd/vmm-web/
        cp vmm-web "$INSTALL_DIR/vmm-web"
        chmod +x "$INSTALL_DIR/vmm-web"
        echo "VMM Web UI installed to $INSTALL_DIR/vmm-web"
        return 0
    else
        echo "Error: Go is not installed. Cannot build from source."
        return 1
    fi
}

# Install VMM binary
# Priority: 1. Pre-built binary from GitHub, 2. Build from source
install_vmm() {
    # Check if --build-from-source flag is provided
    if [ "$BUILD_FROM_SOURCE" = "1" ]; then
        echo "Building from source (--build-from-source specified)..."
        build_from_source
        return $?
    fi

    # Try to download pre-built binary first
    if download_prebuilt; then
        return 0
    fi

    # Fall back to building from source
    echo "Falling back to building from source..."
    build_from_source
}

# Parse command line arguments
BUILD_FROM_SOURCE=0
for arg in "$@"; do
    case "$arg" in
        --build-from-source)
            BUILD_FROM_SOURCE=1
            ;;
    esac
done

# Install VMM
install_vmm

# Create data directories
echo "Creating data directories..."
mkdir -p "$DATA_DIR"/{config,vms,images/kernels,images/rootfs,mounts,sockets,logs,state}

# Install build-kernel.sh script
SCRIPT_DIR="/usr/local/share/vmm"
mkdir -p "$SCRIPT_DIR"
if [ -f "scripts/build-kernel.sh" ]; then
    cp scripts/build-kernel.sh "$SCRIPT_DIR/build-kernel.sh"
    chmod +x "$SCRIPT_DIR/build-kernel.sh"
    echo "Installed build-kernel.sh to $SCRIPT_DIR"
elif [ -f "$(dirname "$0")/build-kernel.sh" ]; then
    cp "$(dirname "$0")/build-kernel.sh" "$SCRIPT_DIR/build-kernel.sh"
    chmod +x "$SCRIPT_DIR/build-kernel.sh"
    echo "Installed build-kernel.sh to $SCRIPT_DIR"
fi

if [ -f "scripts/build-rootfs.sh" ]; then
    cp scripts/build-rootfs.sh "$SCRIPT_DIR/build-rootfs.sh"
    chmod +x "$SCRIPT_DIR/build-rootfs.sh"
    echo "Installed build-rootfs.sh to $SCRIPT_DIR"
elif [ -f "$(dirname "$0")/build-rootfs.sh" ]; then
    cp "$(dirname "$0")/build-rootfs.sh" "$SCRIPT_DIR/build-rootfs.sh"
    chmod +x "$SCRIPT_DIR/build-rootfs.sh"
    echo "Installed build-rootfs.sh to $SCRIPT_DIR"
fi

# Download Firecracker if not present
FC_VERSION="v1.11.0"
FC_BIN="/usr/local/bin/firecracker"
if [ ! -f "$FC_BIN" ]; then
    echo "Downloading Firecracker $FC_VERSION..."
    ARCH=$(uname -m)
    FC_URL="https://github.com/firecracker-microvm/firecracker/releases/download/${FC_VERSION}/firecracker-${FC_VERSION}-${ARCH}.tgz"
    if ! download_file "$FC_URL" /tmp/firecracker.tgz; then
        echo "Error: Failed to download Firecracker"
        exit 1
    fi
    tar -xzf /tmp/firecracker.tgz -C /tmp
    cp "/tmp/release-${FC_VERSION}-${ARCH}/firecracker-${FC_VERSION}-${ARCH}" "$FC_BIN"
    chmod +x "$FC_BIN"
    rm -rf /tmp/firecracker.tgz "/tmp/release-${FC_VERSION}-${ARCH}"
    echo "Firecracker installed to $FC_BIN"
fi

# Download kernel from GitHub releases if not present
KERNEL_PATH="$DATA_DIR/images/kernels/vmlinux.bin"
if [ ! -f "$KERNEL_PATH" ]; then
    echo "Downloading pre-built kernel from GitHub releases..."
    KERNEL_URL=""

    # Query GitHub API for latest kernel-* release
    API_URL="https://api.github.com/repos/${GITHUB_REPO}/releases"
    RELEASES_JSON=""
    if command -v curl &> /dev/null; then
        RELEASES_JSON=$(curl -fsSL "$API_URL" 2>/dev/null)
    elif command -v wget &> /dev/null; then
        RELEASES_JSON=$(wget -qO- "$API_URL" 2>/dev/null)
    fi

    if [ -n "$RELEASES_JSON" ]; then
        # Find the latest release with a kernel-* tag and extract the vmlinux.bin asset URL
        if command -v jq &> /dev/null; then
            KERNEL_URL=$(echo "$RELEASES_JSON" | jq -r '
                [.[] | select(.tag_name | startswith("kernel-"))] |
                first |
                .assets[] | select(.name == "vmlinux.bin") |
                .browser_download_url' 2>/dev/null)
        else
            # Fallback: parse JSON with grep/sed (works without jq)
            KERNEL_URL=$(echo "$RELEASES_JSON" | \
                grep -A 50 '"tag_name": "kernel-' | \
                grep '"browser_download_url".*vmlinux.bin' | \
                head -1 | \
                sed -E 's/.*"browser_download_url": "([^"]+)".*/\1/')
        fi
    fi

    if [ -n "$KERNEL_URL" ] && [ "$KERNEL_URL" != "null" ]; then
        if download_file "$KERNEL_URL" "$KERNEL_PATH"; then
            echo "Kernel downloaded to $KERNEL_PATH"
        else
            echo "Warning: Failed to download kernel. Run 'sudo vmm image pull' later to download it."
        fi
    else
        echo "Warning: Could not find kernel release. Run 'sudo vmm image pull' later to download it."
    fi
else
    echo "Kernel already exists at $KERNEL_PATH"
fi

# Download Kubernetes-compatible kernel from GitHub releases if not present
K8S_KERNEL_PATH="$DATA_DIR/images/kernels/k8s-kernel"
if [ ! -f "$K8S_KERNEL_PATH" ]; then
    echo "Downloading pre-built Kubernetes kernel from GitHub releases..."
    K8S_KERNEL_URL=""

    # Reuse RELEASES_JSON from above if available, otherwise fetch it
    if [ -z "$RELEASES_JSON" ]; then
        API_URL="https://api.github.com/repos/${GITHUB_REPO}/releases"
        if command -v curl &> /dev/null; then
            RELEASES_JSON=$(curl -fsSL "$API_URL" 2>/dev/null)
        elif command -v wget &> /dev/null; then
            RELEASES_JSON=$(wget -qO- "$API_URL" 2>/dev/null)
        fi
    fi

    if [ -n "$RELEASES_JSON" ]; then
        if command -v jq &> /dev/null; then
            K8S_KERNEL_URL=$(echo "$RELEASES_JSON" | jq -r '
                [.[] | select(.tag_name | startswith("k8s-kernel-"))] |
                first |
                .assets[] | select(.name == "k8s-vmlinux.bin") |
                .browser_download_url' 2>/dev/null)
        else
            K8S_KERNEL_URL=$(echo "$RELEASES_JSON" | \
                grep -A 50 '"tag_name": "k8s-kernel-' | \
                grep '"browser_download_url".*k8s-vmlinux.bin' | \
                head -1 | \
                sed -E 's/.*"browser_download_url": "([^"]+)".*/\1/')
        fi
    fi

    if [ -n "$K8S_KERNEL_URL" ] && [ "$K8S_KERNEL_URL" != "null" ]; then
        if download_file "$K8S_KERNEL_URL" "$K8S_KERNEL_PATH"; then
            echo "Kubernetes kernel downloaded to $K8S_KERNEL_PATH"
        else
            echo "Warning: Failed to download Kubernetes kernel. Build one with: sudo vmm kernel build --version 6.6 --name k8s-kernel"
        fi
    else
        echo "No Kubernetes kernel release found. Build one with: sudo vmm kernel build --version 6.6 --name k8s-kernel"
    fi
else
    echo "Kubernetes kernel already exists at $K8S_KERNEL_PATH"
fi

# Download rootfs from GitHub releases if not present
ROOTFS_PATH="$DATA_DIR/images/rootfs/rootfs.ext4"
if [ ! -f "$ROOTFS_PATH" ]; then
    echo "Downloading pre-built rootfs from GitHub releases..."
    ROOTFS_URL=""

    # Reuse RELEASES_JSON from the kernel section if available, otherwise fetch it
    if [ -z "$RELEASES_JSON" ]; then
        API_URL="https://api.github.com/repos/${GITHUB_REPO}/releases"
        if command -v curl &> /dev/null; then
            RELEASES_JSON=$(curl -fsSL "$API_URL" 2>/dev/null)
        elif command -v wget &> /dev/null; then
            RELEASES_JSON=$(wget -qO- "$API_URL" 2>/dev/null)
        fi
    fi

    if [ -n "$RELEASES_JSON" ]; then
        # Find the latest release with a rootfs-* tag and extract the rootfs.ext4.gz asset URL
        if command -v jq &> /dev/null; then
            ROOTFS_URL=$(echo "$RELEASES_JSON" | jq -r '
                [.[] | select(.tag_name | startswith("rootfs-"))] |
                first |
                .assets[] | select(.name == "rootfs.ext4.gz") |
                .browser_download_url' 2>/dev/null)
        else
            # Fallback: parse JSON with grep/sed (works without jq)
            ROOTFS_URL=$(echo "$RELEASES_JSON" | \
                grep -A 50 '"tag_name": "rootfs-' | \
                grep '"browser_download_url".*rootfs.ext4.gz' | \
                head -1 | \
                sed -E 's/.*"browser_download_url": "([^"]+)".*/\1/')
        fi
    fi

    if [ -n "$ROOTFS_URL" ] && [ "$ROOTFS_URL" != "null" ]; then
        echo "  Found rootfs in GitHub releases, downloading..."
        if download_file "$ROOTFS_URL" "$ROOTFS_PATH.gz"; then
            echo "  Decompressing rootfs..."
            gunzip -f "$ROOTFS_PATH.gz"
            echo "Rootfs downloaded to $ROOTFS_PATH"
        else
            rm -f "$ROOTFS_PATH.gz"
            echo "  GitHub download failed, trying fallback URL..."
            FALLBACK_URL="https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/rootfs/bionic.rootfs.ext4"
            if download_file "$FALLBACK_URL" "$ROOTFS_PATH"; then
                echo "Rootfs downloaded to $ROOTFS_PATH (fallback)"
            else
                echo "Warning: Failed to download rootfs. Run 'sudo vmm image pull' later to download it."
            fi
        fi
    else
        echo "  No rootfs release found, trying fallback URL..."
        FALLBACK_URL="https://s3.amazonaws.com/spec.ccfc.min/img/quickstart_guide/x86_64/rootfs/bionic.rootfs.ext4"
        if download_file "$FALLBACK_URL" "$ROOTFS_PATH"; then
            echo "Rootfs downloaded to $ROOTFS_PATH (fallback)"
        else
            echo "Warning: Failed to download rootfs. Run 'sudo vmm image pull' later to download it."
        fi
    fi
else
    echo "Rootfs already exists at $ROOTFS_PATH"
fi

echo ""
echo "Installation complete!"
echo ""
echo "Next steps:"
echo "  1. Initialize VMM:     vmm config init"
echo "  2. Pull images:        sudo vmm image pull"
echo "  3. Create a VM:        sudo vmm create myvm --ssh-key ~/.ssh/id_ed25519.pub"
echo "  4. Start the VM:       sudo vmm start myvm"
echo "  5. SSH into the VM:    vmm ssh myvm"
echo ""
echo "For Kubernetes clusters:"
echo "  sudo vmm cluster create mycluster --ssh-key ~/.ssh/id_ed25519.pub --kernel k8s-kernel"
echo ""
if [ -f "$INSTALL_DIR/vmm-web" ]; then
echo "Web UI (optional):"
echo "  VMM_WEB_PASSWORD=<password> sudo -E vmm-web --listen 0.0.0.0:8080"
echo ""
fi
echo "Optional - To enable auto-start on boot, run:"
echo "  sudo ./scripts/install-service.sh"
echo ""
