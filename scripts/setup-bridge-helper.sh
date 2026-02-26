#!/bin/bash
# Setup script for QEMU bridge helper
# This allows libvirt VMs to connect to Docker bridges

set -e

echo "=== Setting up QEMU bridge helper for libvirt ==="

# Create qemu config directory if it doesn't exist
sudo mkdir -p /etc/qemu

# Create bridge.conf to allow all bridges
echo "Configuring /etc/qemu/bridge.conf..."
echo "allow all" | sudo tee /etc/qemu/bridge.conf > /dev/null

# Set proper permissions
sudo chmod 0644 /etc/qemu/bridge.conf

# Find and set permissions on qemu-bridge-helper
BRIDGE_HELPER=$(find /usr -name qemu-bridge-helper 2>/dev/null | head -1)
if [ -n "$BRIDGE_HELPER" ]; then
    echo "Found bridge helper at: $BRIDGE_HELPER"
    sudo chmod u+s "$BRIDGE_HELPER"
    echo "✓ Set SUID bit on bridge helper"
else
    echo "⚠ Warning: Could not find qemu-bridge-helper"
    echo "  Install with: sudo apt install qemu-system-x86"
fi

# Add user to necessary groups
echo "Adding $USER to libvirt and kvm groups..."
sudo usermod -aG libvirt $USER
sudo usermod -aG kvm $USER

# Fix libvirt images directory permissions
echo "Fixing /var/lib/libvirt/images permissions..."
sudo chmod 755 /var/lib/libvirt/images

# Fix AppArmor for libvirt (if installed)
if command -v aa-complain &> /dev/null; then
    echo "Configuring AppArmor for libvirt..."
    echo '/var/lib/libvirt/images/** rwk,' | sudo tee -a /etc/apparmor.d/abstractions/libvirt-qemu > /dev/null
    sudo apparmor_parser -r /etc/apparmor.d/abstractions/libvirt-qemu 2>/dev/null || true
    sudo systemctl restart libvirtd
    echo "✓ AppArmor configured"
fi

echo ""
echo "=== Setup complete! ==="
echo ""
echo "IMPORTANT: You need to log out and log back in for group changes to take effect."
echo ""
echo "After logging back in, you can:"
echo "  1. Start the orchestrator: ./orchestrator -c config.yaml up"
echo "  2. VMs will connect to Docker bridge networks automatically"
echo ""
