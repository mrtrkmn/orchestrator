#!/bin/bash
# Download Debian cloud image which has better console access
cd /home/acrndev/Desktop/orchestrator/orchestrator/images

echo "Downloading Debian 12 cloud image (better for console/SSH)..."
wget -O debian-12.qcow2 \
  "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2"

echo "✓ Downloaded debian-12.qcow2"
echo ""
echo "To use it, update config/example.yaml:"
echo "  image: /home/acrndev/Desktop/orchestrator/orchestrator/images/debian-12.qcow2"
echo ""
echo "Debian cloud image:"
echo "  Username: debian"
echo "  Password: Can be set via console on first login"
echo ""
