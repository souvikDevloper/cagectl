#!/usr/bin/env bash
# setup-rootfs.sh — Downloads and extracts Alpine Linux minimal rootfs.
#
# Alpine Linux is perfect for container base images because:
#   - Tiny (~3MB compressed, ~8MB extracted)
#   - Uses musl libc and busybox — minimal attack surface
#   - Has a package manager (apk) for installing additional software
#   - Same base image Docker's "alpine" image uses
#
# Usage:
#   ./scripts/setup-rootfs.sh [target-directory]
#
# This creates a directory with a complete Linux root filesystem that
# cagectl can use as the lower (read-only) layer of its OverlayFS setup.

set -euo pipefail

# Configuration
ALPINE_VERSION="3.19"
ALPINE_RELEASE="3.19.1"
ARCH="x86_64"
ALPINE_MIRROR="https://dl-cdn.alpinelinux.org/alpine"

# Target directory (default: ./rootfs)
ROOTFS_DIR="${1:-./rootfs}"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

info() { echo -e "${GREEN}[INFO]${NC} $*"; }
warn() { echo -e "${YELLOW}[WARN]${NC} $*"; }
error() { echo -e "${RED}[ERROR]${NC} $*" >&2; exit 1; }

# Check for required tools
for tool in curl tar; do
    if ! command -v "$tool" &> /dev/null; then
        error "$tool is required but not installed."
    fi
done

# Check if running as root (needed to preserve ownership in rootfs)
if [ "$(id -u)" -ne 0 ]; then
    warn "Not running as root. File ownership in rootfs may not be preserved."
    warn "For best results, run: sudo $0 $ROOTFS_DIR"
fi

# Download URL
TARBALL="alpine-minirootfs-${ALPINE_RELEASE}-${ARCH}.tar.gz"
URL="${ALPINE_MIRROR}/v${ALPINE_VERSION}/releases/${ARCH}/${TARBALL}"

info "Alpine Linux rootfs setup"
info "  Version: ${ALPINE_RELEASE}"
info "  Arch:    ${ARCH}"
info "  Target:  ${ROOTFS_DIR}"
echo ""

# Create target directory
if [ -d "$ROOTFS_DIR" ]; then
    warn "Directory ${ROOTFS_DIR} already exists."
    read -rp "Remove and re-download? [y/N] " answer
    if [[ "$answer" =~ ^[Yy]$ ]]; then
        rm -rf "$ROOTFS_DIR"
    else
        info "Using existing rootfs at ${ROOTFS_DIR}"
        exit 0
    fi
fi

mkdir -p "$ROOTFS_DIR"

# Download the rootfs tarball
info "Downloading ${TARBALL}..."
TMPFILE=$(mktemp)
trap 'rm -f "$TMPFILE"' EXIT

if ! curl -fSL --progress-bar -o "$TMPFILE" "$URL"; then
    error "Failed to download rootfs from ${URL}"
fi

info "Download complete ($(du -h "$TMPFILE" | cut -f1))"

# Extract the rootfs
info "Extracting rootfs to ${ROOTFS_DIR}..."
tar xzf "$TMPFILE" -C "$ROOTFS_DIR"

# Set up basic configuration inside the rootfs
info "Configuring rootfs..."

# Set up DNS resolution
cat > "${ROOTFS_DIR}/etc/resolv.conf" << 'EOF'
nameserver 8.8.8.8
nameserver 8.8.4.4
nameserver 1.1.1.1
EOF

# Set up Alpine package repositories
cat > "${ROOTFS_DIR}/etc/apk/repositories" << EOF
${ALPINE_MIRROR}/v${ALPINE_VERSION}/main
${ALPINE_MIRROR}/v${ALPINE_VERSION}/community
EOF

# Create a minimal /etc/hosts
cat > "${ROOTFS_DIR}/etc/hosts" << 'EOF'
127.0.0.1   localhost
::1         localhost
EOF

# Ensure required directories exist
for dir in proc sys dev dev/pts tmp run var/tmp; do
    mkdir -p "${ROOTFS_DIR}/${dir}"
done

# Set proper permissions on /tmp
chmod 1777 "${ROOTFS_DIR}/tmp"
chmod 1777 "${ROOTFS_DIR}/var/tmp"

# Verify the rootfs has essential binaries
ESSENTIAL_BINARIES=("sh" "ls" "cat" "echo" "ps" "mount")
MISSING=()

for bin in "${ESSENTIAL_BINARIES[@]}"; do
    found=false
    for path in bin sbin usr/bin usr/sbin; do
        if [ -f "${ROOTFS_DIR}/${path}/${bin}" ] || [ -L "${ROOTFS_DIR}/${path}/${bin}" ]; then
            found=true
            break
        fi
    done
    if [ "$found" = false ]; then
        MISSING+=("$bin")
    fi
done

if [ ${#MISSING[@]} -gt 0 ]; then
    warn "Missing binaries: ${MISSING[*]}"
    warn "The rootfs may not be fully functional."
fi

# Print summary
echo ""
info "Rootfs setup complete!"
info ""
info "  Location: ${ROOTFS_DIR}"
info "  Size:     $(du -sh "$ROOTFS_DIR" | cut -f1)"
info "  Files:    $(find "$ROOTFS_DIR" -type f | wc -l)"
info ""
info "Usage:"
info "  sudo cagectl run --rootfs ${ROOTFS_DIR} -- /bin/sh"
info ""
info "To install packages inside a container:"
info "  apk update && apk add <package>"
