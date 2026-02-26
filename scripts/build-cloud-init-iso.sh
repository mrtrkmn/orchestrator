#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/../cloud-init"

ISO_NAME="cloud-init.iso"

if command -v cloud-localds >/dev/null 2>&1; then
  echo "[cloud-init] Using cloud-localds to build ${ISO_NAME}"
  cloud-localds -v "${ISO_NAME}" user-data meta-data
else
  echo "[cloud-init] cloud-localds not found; falling back to genisoimage/mkisofs"
  if command -v genisoimage >/dev/null 2>&1; then
    genisoimage -output "${ISO_NAME}" -volid cidata -joliet -rock user-data meta-data
  elif command -v mkisofs >/dev/null 2>&1; then
    mkisofs -output "${ISO_NAME}" -volid cidata -joliet -rock user-data meta-data
  else
    echo "Error: neither cloud-localds nor genisoimage/mkisofs is available." >&2
    echo "Install cloud-image-utils or genisoimage and retry." >&2
    exit 1
  fi
fi

echo "[cloud-init] Built $(pwd)/${ISO_NAME}"