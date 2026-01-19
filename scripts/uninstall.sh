#!/bin/bash
#
# VMM Uninstall Script
# Completely removes VMM and all associated resources from the system.
#
# Usage: sudo ./uninstall.sh [--yes]
#

set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

# Configuration
DATA_DIR="/var/lib/vmm"
SUBNET="172.16.0.0/16"
BRIDGE_NAME="vmm-br0"

# Parse arguments
SKIP_CONFIRM=false
for arg in "$@"; do
    case $arg in
        --yes|-y)
            SKIP_CONFIRM=true
            shift
            ;;
        --help|-h)
            echo "Usage: sudo $0 [--yes]"
            echo ""
            echo "Completely removes VMM and all associated resources."
            echo ""
            echo "Options:"
            echo "  --yes, -y    Skip confirmation prompt"
            echo "  --help, -h   Show this help message"
            exit 0
            ;;
        *)
            echo "Unknown option: $arg"
            echo "Use --help for usage information"
            exit 1
            ;;
    esac
done

# Check for root privileges
if [ "$EUID" -ne 0 ]; then
    echo -e "${RED}Error: This script must be run as root${NC}"
    echo "Usage: sudo $0 [--yes]"
    exit 1
fi

# Detect host interface from /proc/net/route (same method as vmm uses)
detect_host_interface() {
    local iface
    iface=$(awk '$2 == "00000000" {print $1; exit}' /proc/net/route 2>/dev/null)
    if [ -n "$iface" ]; then
        echo "$iface"
    else
        echo "eth0"
    fi
}

# Get the real user's home directory (handle sudo)
get_user_home() {
    if [ -n "$SUDO_USER" ]; then
        getent passwd "$SUDO_USER" | cut -d: -f6
    else
        echo "$HOME"
    fi
}

echo ""
echo -e "${GREEN}VMM Uninstaller${NC}"
echo "==============="
echo ""

# Confirmation prompt
if [ "$SKIP_CONFIRM" = false ]; then
    echo "This will completely remove VMM and all associated resources:"
    echo "  - Stop all running VMs"
    echo "  - Remove VMM and Firecracker binaries"
    echo "  - Remove all VM data (/var/lib/vmm)"
    echo "  - Remove user configuration (~/.config/vmm)"
    echo "  - Remove network bridge and TAP devices"
    echo "  - Remove iptables rules"
    echo "  - Remove systemd service"
    echo ""
    read -p "Are you sure you want to continue? [y/N] " -n 1 -r
    echo ""
    if [[ ! $REPLY =~ ^[Yy]$ ]]; then
        echo "Uninstall cancelled."
        exit 0
    fi
    echo ""
fi

HOST_IFACE=$(detect_host_interface)
USER_HOME=$(get_user_home)

# 1. Stop systemd service
echo "Stopping VMM service..."
if systemctl is-active --quiet vmm.service 2>/dev/null; then
    systemctl stop vmm.service
    echo -e "  ${GREEN}Service stopped${NC}"
fi
if systemctl is-enabled --quiet vmm.service 2>/dev/null; then
    systemctl disable vmm.service 2>/dev/null || true
    echo -e "  ${GREEN}Service disabled${NC}"
fi
if [ ! -f /etc/systemd/system/vmm.service ] && ! systemctl is-active --quiet vmm.service 2>/dev/null; then
    echo "  Service not installed"
fi

# 2. Stop all Firecracker processes
echo "Stopping Firecracker processes..."
FC_COUNT=$(pgrep -f firecracker 2>/dev/null | wc -l || echo "0")
if [ "$FC_COUNT" -gt 0 ]; then
    # Send SIGTERM first
    pkill -f firecracker 2>/dev/null || true
    sleep 3
    # Force kill any remaining
    pkill -9 -f firecracker 2>/dev/null || true
    echo -e "  ${GREEN}Stopped $FC_COUNT running VM(s)${NC}"
else
    echo "  No running VMs found"
fi

# 3. Remove TAP devices
echo "Removing network resources..."
TAP_DELETED=0
for tap in $(ip link show 2>/dev/null | grep -oP 'vmm-[a-z0-9]+(?=:)' || true); do
    ip link set "$tap" down 2>/dev/null || true
    ip link delete "$tap" 2>/dev/null || true
    echo -e "  ${GREEN}Deleted TAP device $tap${NC}"
    TAP_DELETED=$((TAP_DELETED + 1))
done
if [ "$TAP_DELETED" -eq 0 ]; then
    echo "  No TAP devices found"
fi

# 4. Remove bridge
if ip link show "$BRIDGE_NAME" &>/dev/null; then
    ip link set "$BRIDGE_NAME" down 2>/dev/null || true
    ip link delete "$BRIDGE_NAME" 2>/dev/null || true
    echo -e "  ${GREEN}Deleted bridge $BRIDGE_NAME${NC}"
else
    echo "  Bridge $BRIDGE_NAME not found"
fi

# 5. Remove iptables rules
echo "Removing iptables rules..."
RULES_REMOVED=0

# Remove NAT MASQUERADE rule
if iptables -t nat -C POSTROUTING -s "$SUBNET" -o "$HOST_IFACE" -j MASQUERADE 2>/dev/null; then
    iptables -t nat -D POSTROUTING -s "$SUBNET" -o "$HOST_IFACE" -j MASQUERADE 2>/dev/null || true
    echo -e "  ${GREEN}Removed NAT MASQUERADE rule${NC}"
    RULES_REMOVED=$((RULES_REMOVED + 1))
fi

# Remove FORWARD rules
if iptables -C FORWARD -i "$BRIDGE_NAME" -o "$HOST_IFACE" -j ACCEPT 2>/dev/null; then
    iptables -D FORWARD -i "$BRIDGE_NAME" -o "$HOST_IFACE" -j ACCEPT 2>/dev/null || true
    echo -e "  ${GREEN}Removed FORWARD rule (bridge to host)${NC}"
    RULES_REMOVED=$((RULES_REMOVED + 1))
fi

if iptables -C FORWARD -i "$HOST_IFACE" -o "$BRIDGE_NAME" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null; then
    iptables -D FORWARD -i "$HOST_IFACE" -o "$BRIDGE_NAME" -m conntrack --ctstate RELATED,ESTABLISHED -j ACCEPT 2>/dev/null || true
    echo -e "  ${GREEN}Removed FORWARD rule (host to bridge)${NC}"
    RULES_REMOVED=$((RULES_REMOVED + 1))
fi

# Remove any DNAT port forwarding rules for the subnet
while iptables -t nat -L PREROUTING -n --line-numbers 2>/dev/null | grep -q "172.16.0."; do
    LINE=$(iptables -t nat -L PREROUTING -n --line-numbers 2>/dev/null | grep "172.16.0." | head -1 | awk '{print $1}')
    if [ -n "$LINE" ]; then
        iptables -t nat -D PREROUTING "$LINE" 2>/dev/null || break
        echo -e "  ${GREEN}Removed DNAT port forwarding rule${NC}"
        RULES_REMOVED=$((RULES_REMOVED + 1))
    else
        break
    fi
done

if [ "$RULES_REMOVED" -eq 0 ]; then
    echo "  No iptables rules found"
fi

# 6. Remove data directory
echo "Removing data directories..."
if [ -d "$DATA_DIR" ]; then
    rm -rf "$DATA_DIR"
    echo -e "  ${GREEN}Removed $DATA_DIR${NC}"
else
    echo "  $DATA_DIR not found"
fi

# 7. Remove user configuration
echo "Removing user configuration..."
USER_CONFIG_DIR="$USER_HOME/.config/vmm"
if [ -d "$USER_CONFIG_DIR" ]; then
    rm -rf "$USER_CONFIG_DIR"
    echo -e "  ${GREEN}Removed $USER_CONFIG_DIR${NC}"
else
    echo "  $USER_CONFIG_DIR not found"
fi

# 8. Remove systemd service file
echo "Removing systemd service..."
if [ -f /etc/systemd/system/vmm.service ]; then
    rm -f /etc/systemd/system/vmm.service
    systemctl daemon-reload
    echo -e "  ${GREEN}Removed /etc/systemd/system/vmm.service${NC}"
else
    echo "  Service file not found"
fi

# 9. Remove binaries
echo "Removing binaries..."
if [ -f /usr/local/bin/vmm ]; then
    rm -f /usr/local/bin/vmm
    echo -e "  ${GREEN}Removed /usr/local/bin/vmm${NC}"
else
    echo "  /usr/local/bin/vmm not found"
fi

if [ -f /usr/local/bin/firecracker ]; then
    rm -f /usr/local/bin/firecracker
    echo -e "  ${GREEN}Removed /usr/local/bin/firecracker${NC}"
else
    echo "  /usr/local/bin/firecracker not found"
fi

if [ -f /usr/local/share/vmm/build-kernel.sh ]; then
    rm -f /usr/local/share/vmm/build-kernel.sh
    rmdir /usr/local/share/vmm 2>/dev/null || true
    echo -e "  ${GREEN}Removed /usr/local/share/vmm/build-kernel.sh${NC}"
else
    echo "  /usr/local/share/vmm/build-kernel.sh not found"
fi

echo ""
echo -e "${GREEN}VMM uninstalled successfully!${NC}"
echo ""
