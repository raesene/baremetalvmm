#!/bin/bash
set -e

# VMM Systemd Service Installation Script
# This script installs systemd services for VM auto-start and the web UI

SERVICE_DIR="/etc/systemd/system"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

echo "VMM Service Installer"
echo "====================="

# Check for root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root (sudo)"
    exit 1
fi

# Check if vmm is installed
if ! command -v vmm &> /dev/null; then
    echo "Error: vmm is not installed. Please run install.sh first."
    exit 1
fi

# Install vmm systemd service
echo "Installing vmm systemd service..."
cp "$SCRIPT_DIR/vmm.service" "$SERVICE_DIR/vmm.service"

# Install vmm-web systemd service if binary exists
if command -v vmm-web &> /dev/null; then
    echo "Installing vmm-web systemd service..."
    cp "$SCRIPT_DIR/vmm-web.service" "$SERVICE_DIR/vmm-web.service"

    # Create environment file directory and template if not present
    if [ ! -f /etc/vmm-web/environment ]; then
        mkdir -p /etc/vmm-web
        echo "VMM_WEB_PASSWORD=please-set-a-real-password" > /etc/vmm-web/environment
        chmod 600 /etc/vmm-web/environment
        echo ""
        echo "IMPORTANT: Set your vmm-web password in /etc/vmm-web/environment"
        echo "           Password must be at least 8 characters and not a common default."
    fi
fi

systemctl daemon-reload

echo ""
echo "Systemd services installed!"
echo ""
echo "VMM (auto-start/stop VMs on boot):"
echo "  sudo systemctl enable vmm"
echo "  sudo systemctl start vmm"
echo "  sudo systemctl status vmm"

if command -v vmm-web &> /dev/null; then
    echo ""
    echo "VMM Web UI:"
    echo "  1. Edit /etc/vmm-web/environment to set VMM_WEB_PASSWORD"
    echo "  2. sudo systemctl enable vmm-web"
    echo "  3. sudo systemctl start vmm-web"
    echo "  4. sudo systemctl status vmm-web"
fi
echo ""
