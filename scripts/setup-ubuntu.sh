#!/usr/bin/env bash
# =============================================================================
# Ubuntu Environment Setup Script for Orchestrator
# =============================================================================
# Installs and configures: Docker, libvirt/KVM/QEMU, WireGuard, Go,
# cloud-init tools, and all other dependencies needed by the orchestrator.
#
# Tested on: Ubuntu 22.04 / 24.04
# Usage:     chmod +x scripts/setup-ubuntu.sh && sudo ./scripts/setup-ubuntu.sh
# =============================================================================

set -euo pipefail

# ── Colors ───────────────────────────────────────────────────────────────────
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

info()  { echo -e "${CYAN}[INFO]${NC}  $*"; }
ok()    { echo -e "${GREEN}[OK]${NC}    $*"; }
warn()  { echo -e "${YELLOW}[WARN]${NC}  $*"; }
err()   { echo -e "${RED}[ERROR]${NC} $*"; }

# ── Pre-flight checks ───────────────────────────────────────────────────────
if [[ $EUID -ne 0 ]]; then
    err "This script must be run as root (use sudo)."
    exit 1
fi

REAL_USER="${SUDO_USER:-$USER}"
if [[ "$REAL_USER" == "root" ]]; then
    warn "Running as root without SUDO_USER set. Group membership changes will apply to root."
fi

source /etc/os-release 2>/dev/null || true
if [[ "${ID:-}" != "ubuntu" ]]; then
    warn "This script is designed for Ubuntu. Detected: ${ID:-unknown}. Proceeding anyway..."
fi

info "Setting up orchestrator environment for user: $REAL_USER"
echo ""

# ── 1. System update ────────────────────────────────────────────────────────
info "Updating package lists and upgrading existing packages..."
apt-get update -qq
apt-get upgrade -y -qq
ok "System updated."

# ── 2. Common utilities ─────────────────────────────────────────────────────
info "Installing common utilities..."
apt-get install -y -qq \
    apt-transport-https \
    ca-certificates \
    curl \
    wget \
    gnupg \
    lsb-release \
    software-properties-common \
    git \
    jq \
    make \
    net-tools \
    bridge-utils \
    iptables
ok "Common utilities installed."

# ── 3. Docker ────────────────────────────────────────────────────────────────
info "Installing Docker..."
if command -v docker &>/dev/null; then
    ok "Docker is already installed: $(docker --version)"
else
    # Add Docker official GPG key
    install -m 0755 -d /etc/apt/keyrings
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc

    # Add Docker repository
    echo \
        "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] \
        https://download.docker.com/linux/ubuntu \
        $(. /etc/os-release && echo "$VERSION_CODENAME") stable" | \
        tee /etc/apt/sources.list.d/docker.list > /dev/null

    apt-get update -qq
    apt-get install -y -qq \
        docker-ce \
        docker-ce-cli \
        containerd.io \
        docker-buildx-plugin \
        docker-compose-plugin

    ok "Docker installed: $(docker --version)"
fi

# Add user to docker group
usermod -aG docker "$REAL_USER"
ok "User '$REAL_USER' added to docker group."

# Enable and start Docker
systemctl enable --now docker
ok "Docker service enabled and running."

# ── 4. Libvirt / KVM / QEMU ─────────────────────────────────────────────────
info "Installing libvirt, KVM, and QEMU..."
apt-get install -y -qq \
    qemu-kvm \
    qemu-system-x86 \
    qemu-utils \
    libvirt-daemon-system \
    libvirt-clients \
    virtinst \
    virt-manager \
    ovmf
ok "Libvirt/KVM/QEMU installed."

# Enable and start libvirtd
systemctl enable --now libvirtd
ok "libvirtd service enabled and running."

# Add user to libvirt and kvm groups
usermod -aG libvirt "$REAL_USER"
usermod -aG kvm "$REAL_USER"
ok "User '$REAL_USER' added to libvirt and kvm groups."

# ── 5. QEMU bridge helper setup ─────────────────────────────────────────────
info "Configuring QEMU bridge helper..."

# Create /etc/qemu/bridge.conf
mkdir -p /etc/qemu
echo "allow all" > /etc/qemu/bridge.conf
chmod 0644 /etc/qemu/bridge.conf

# Set SUID bit on qemu-bridge-helper
BRIDGE_HELPER=$(find /usr -name qemu-bridge-helper 2>/dev/null | head -1)
if [[ -n "$BRIDGE_HELPER" ]]; then
    chmod u+s "$BRIDGE_HELPER"
    ok "SUID bit set on bridge helper: $BRIDGE_HELPER"
else
    warn "qemu-bridge-helper not found. VMs may not be able to attach to Docker bridges."
fi

# Fix libvirt images directory permissions
mkdir -p /var/lib/libvirt/images
chmod 755 /var/lib/libvirt/images
ok "QEMU bridge helper configured."

# ── 6. AppArmor configuration for libvirt ────────────────────────────────────
info "Configuring AppArmor for libvirt..."
if [[ -f /etc/apparmor.d/abstractions/libvirt-qemu ]]; then
    # Add permissive rules for VM image paths (idempotent)
    if ! grep -q '/var/lib/libvirt/images/\*\* rwk,' /etc/apparmor.d/abstractions/libvirt-qemu 2>/dev/null; then
        echo '/var/lib/libvirt/images/** rwk,' >> /etc/apparmor.d/abstractions/libvirt-qemu
    fi
    apparmor_parser -r /etc/apparmor.d/abstractions/libvirt-qemu 2>/dev/null || true
    systemctl restart libvirtd
    ok "AppArmor configured for libvirt."
else
    warn "AppArmor libvirt profile not found. Skipping."
fi

# ── 7. Cloud-init tools ─────────────────────────────────────────────────────
info "Installing cloud-init tools (cloud-image-utils, genisoimage)..."
apt-get install -y -qq \
    cloud-image-utils \
    genisoimage
ok "Cloud-init tools installed."

# ── 8. WireGuard ─────────────────────────────────────────────────────────────
info "Installing WireGuard..."
apt-get install -y -qq \
    wireguard \
    wireguard-tools
ok "WireGuard installed."

# ── 9. Go ────────────────────────────────────────────────────────────────────
GO_VERSION="1.24.0"
info "Installing Go ${GO_VERSION}..."
if command -v go &>/dev/null; then
    INSTALLED_GO=$(go version | awk '{print $3}' | sed 's/go//')
    ok "Go is already installed: v${INSTALLED_GO}"
else
    ARCH=$(dpkg --print-architecture)
    GO_TARBALL="go${GO_VERSION}.linux-${ARCH}.tar.gz"
    wget -q "https://go.dev/dl/${GO_TARBALL}" -O "/tmp/${GO_TARBALL}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${GO_TARBALL}"
    rm -f "/tmp/${GO_TARBALL}"

    # Set up Go path for the real user
    PROFILE_FILE="/home/${REAL_USER}/.profile"
    if [[ "$REAL_USER" == "root" ]]; then
        PROFILE_FILE="/root/.profile"
    fi
    if ! grep -q '/usr/local/go/bin' "$PROFILE_FILE" 2>/dev/null; then
        {
            echo ''
            echo '# Go'
            echo 'export PATH=$PATH:/usr/local/go/bin'
            echo 'export PATH=$PATH:$(go env GOPATH)/bin'
        } >> "$PROFILE_FILE"
    fi

    export PATH=$PATH:/usr/local/go/bin
    ok "Go ${GO_VERSION} installed."
fi

# ── 10. KVM check ───────────────────────────────────────────────────────────
info "Verifying KVM support..."
if [[ -e /dev/kvm ]]; then
    ok "KVM is available (/dev/kvm exists)."
    # Ensure kvm device is accessible by kvm group
    chown root:kvm /dev/kvm
    chmod 660 /dev/kvm
else
    warn "/dev/kvm not found. Hardware virtualization may not be enabled in BIOS/UEFI."
    warn "VMs will run without KVM acceleration (much slower)."
fi

if lsmod | grep -q kvm; then
    ok "KVM kernel modules loaded."
else
    warn "KVM kernel modules not loaded. Attempting to load..."
    modprobe kvm 2>/dev/null || true
    modprobe kvm_intel 2>/dev/null || modprobe kvm_amd 2>/dev/null || true
fi

# ── 11. Download a default VM image ─────────────────────────────────────────
info "Downloading default Debian 12 cloud image..."
DEBIAN_IMG="/var/lib/libvirt/images/debian-12.qcow2"
if [[ -f "$DEBIAN_IMG" ]]; then
    ok "Debian 12 image already exists at ${DEBIAN_IMG}"
else
    wget -q --show-progress \
        "https://cloud.debian.org/images/cloud/bookworm/latest/debian-12-generic-amd64.qcow2" \
        -O "$DEBIAN_IMG"
    chown libvirt-qemu:kvm "$DEBIAN_IMG"
    chmod 644 "$DEBIAN_IMG"
    ok "Debian 12 cloud image downloaded to ${DEBIAN_IMG}"
fi

# ── 12. Build cloud-init ISO ────────────────────────────────────────────────
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CLOUD_INIT_DIR="${SCRIPT_DIR}/../cloud-init"
if [[ -d "$CLOUD_INIT_DIR" ]]; then
    info "Building cloud-init ISO..."
    pushd "$CLOUD_INIT_DIR" > /dev/null
    if [[ -f user-data && -f meta-data ]]; then
        if command -v cloud-localds &>/dev/null; then
            cloud-localds cloud-init.iso user-data meta-data
        elif command -v genisoimage &>/dev/null; then
            genisoimage -output cloud-init.iso -volid cidata -joliet -rock user-data meta-data
        fi
        ok "cloud-init.iso built in ${CLOUD_INIT_DIR}"
    else
        warn "cloud-init user-data/meta-data not found. Skipping ISO build."
    fi
    popd > /dev/null
fi

# ── 13. Cleanup ──────────────────────────────────────────────────────────────
info "Cleaning up apt cache..."
apt-get autoremove -y -qq
apt-get clean -qq
ok "Cleanup complete."

# ── Summary ──────────────────────────────────────────────────────────────────
echo ""
echo "============================================================================="
echo -e "${GREEN}  Environment setup complete!${NC}"
echo "============================================================================="
echo ""
echo "  Installed components:"
echo "    - Docker          $(docker --version 2>/dev/null || echo 'check manually')"
echo "    - Libvirt         $(virsh --version 2>/dev/null || echo 'check manually')"
echo "    - QEMU            $(qemu-system-x86_64 --version 2>/dev/null | head -1 || echo 'check manually')"
echo "    - WireGuard       $(wg --version 2>/dev/null || echo 'check manually')"
echo "    - Go              $(go version 2>/dev/null || echo 'check manually')"
echo "    - cloud-localds   $(command -v cloud-localds &>/dev/null && echo 'available' || echo 'not found')"
echo "    - genisoimage     $(command -v genisoimage &>/dev/null && echo 'available' || echo 'not found')"
echo ""
echo "  VM image: ${DEBIAN_IMG}"
echo ""
echo -e "  ${YELLOW}IMPORTANT: You must log out and log back in${NC} for group changes to take effect."
echo "  (Groups added: docker, libvirt, kvm)"
echo ""
echo "  Quick start:"
echo "    cd $(dirname "$SCRIPT_DIR")"
echo "    go build -o orchestrator ."
echo "    ./orchestrator -c config/example.yaml up"
echo ""
echo "============================================================================="
