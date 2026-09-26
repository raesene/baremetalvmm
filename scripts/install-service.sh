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

    # Generate a random password if none is set. Older versions wrote a fixed
    # placeholder that vmm-web accepts, so replace that too.
    if [ ! -s /etc/vmm-web/environment ] || grep -q "please-set-a-real-password" /etc/vmm-web/environment; then
        mkdir -p /etc/vmm-web
        WEB_PASSWORD=$(head -c 32 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24)
        ( umask 077; echo "VMM_WEB_PASSWORD=$WEB_PASSWORD" > /etc/vmm-web/environment )
        echo ""
        echo "Generated a vmm-web password (user: admin): $WEB_PASSWORD"
        echo "It is stored in /etc/vmm-web/environment; edit that file to change it."
    fi
    chmod 600 /etc/vmm-web/environment
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
    echo "  sudo systemctl enable --now vmm-web"
    echo "  sudo systemctl status vmm-web"
    echo "  (password: /etc/vmm-web/environment)"
fi
echo ""
