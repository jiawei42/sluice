#!/bin/sh
# sluice installer for macOS and Linux.
#
#   curl -fsSL https://raw.githubusercontent.com/jiawei42/sluice/main/install.sh | sh
#
# What it does: downloads sluice_<os>_<arch>.tar.gz and checksums.txt from the
# GitHub release, verifies the SHA-256, and copies the `sluice` binary to
# $SLUICE_INSTALL_DIR (default ~/.local/bin). Nothing else is changed: no PATH
# edits, no sudo, no telemetry.
#
# Options (environment variables):
#   SLUICE_VERSION        release tag, e.g. v1.0.0 (default: latest)
#   SLUICE_INSTALL_DIR    target directory (default: $HOME/.local/bin)
#   SLUICE_DOWNLOAD_BASE  alternative download URL (mirrors, testing)
set -eu

REPO="jiawei42/sluice"
VERSION="${SLUICE_VERSION:-latest}"
INSTALL_DIR="${SLUICE_INSTALL_DIR:-$HOME/.local/bin}"
BASE="${SLUICE_DOWNLOAD_BASE:-}"

say() { printf '%s\n' "sluice-install: $*"; }
die() { say "error: $*" >&2; exit 1; }

if [ -z "$BASE" ]; then
  if [ "$VERSION" = "latest" ]; then
    BASE="https://github.com/$REPO/releases/latest/download"
  else
    BASE="https://github.com/$REPO/releases/download/$VERSION"
  fi
fi

os=$(uname -s | tr '[:upper:]' '[:lower:]')
[ -n "${SLUICE_OS:-}" ] && os="$SLUICE_OS"   # testing override
case "$os" in
  linux|darwin) ;;
  mingw*|msys*|cygwin*) die "on Windows use install.ps1 (see README)" ;;
  *) die "unsupported OS: $os" ;;
esac
arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture: $arch" ;;
esac

file="sluice_${os}_${arch}.tar.gz"
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM

fetch() {
  if command -v curl >/dev/null 2>&1; then
    curl -fsSL --retry 2 "$1" -o "$2"
  elif command -v wget >/dev/null 2>&1; then
    wget -q -O "$2" "$1"
  else
    die "need curl or wget"
  fi
}

say "downloading $file ($VERSION)"
fetch "$BASE/$file" "$tmp/$file" || die "download failed: $BASE/$file"
fetch "$BASE/checksums.txt" "$tmp/checksums.txt" || die "download failed: $BASE/checksums.txt"

expected=$(awk -v f="$file" '$2 == f { print $1 }' "$tmp/checksums.txt")
[ -n "$expected" ] || die "$file is not listed in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$file" | awk '{ print $1 }')
elif command -v shasum >/dev/null 2>&1; then
  actual=$(shasum -a 256 "$tmp/$file" | awk '{ print $1 }')
else
  die "need sha256sum or shasum to verify the download"
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $file (expected $expected, got $actual)"
say "checksum OK"

tar -xzf "$tmp/$file" -C "$tmp" sluice
mkdir -p "$INSTALL_DIR"
cp "$tmp/sluice" "$INSTALL_DIR/sluice.tmp"
chmod 0755 "$INSTALL_DIR/sluice.tmp"
mv -f "$INSTALL_DIR/sluice.tmp" "$INSTALL_DIR/sluice"
say "installed $INSTALL_DIR/sluice"

case ":$PATH:" in
  *":$INSTALL_DIR:"*) ;;
  *) say "note: $INSTALL_DIR is not on your PATH; add it to your shell profile" ;;
esac
"$INSTALL_DIR/sluice" version
say "next: sluice init github   (see: sluice presets)"
