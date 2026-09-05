#!/bin/sh
# Install the SwiftCloud CLI.
#
#   curl -fsSL https://cloud.co.zm/install.sh | sh
#
# Downloads the latest release for this OS and architecture from GitHub,
# verifies its SHA-256 against the release's checksums.txt, and installs the
# `cloud` binary to /usr/local/bin (or $CLOUD_INSTALL_DIR). Linux and macOS
# only; Windows has install.ps1, which does the same thing.
set -eu

REPO="swiftcloud-platform/cli"
INSTALL_DIR="${CLOUD_INSTALL_DIR:-/usr/local/bin}"
VERSION="${CLOUD_VERSION:-}"

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "need '$1' on PATH"; }
need curl; need tar; need uname

os=$(uname -s | tr '[:upper:]' '[:lower:]')
case "$os" in
  linux|darwin) ;;
  *) die "unsupported OS '$os' — on Windows run: irm https://cloud.co.zm/install.ps1 | iex" ;;
esac

arch=$(uname -m)
case "$arch" in
  x86_64|amd64) arch=amd64 ;;
  arm64|aarch64) arch=arm64 ;;
  *) die "unsupported architecture '$arch'" ;;
esac

if [ -z "$VERSION" ]; then
  # Resolve the latest tag without needing jq: follow the /latest redirect.
  VERSION=$(curl -fsSLI -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')
  [ -n "$VERSION" ] || die "could not determine the latest version"
fi
ver="${VERSION#v}"

base="https://github.com/$REPO/releases/download/$VERSION"
archive="cloud_${ver}_${os}_${arch}.tar.gz"

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

say "Downloading cloud $VERSION for $os/$arch…"
curl -fsSL -o "$tmp/$archive" "$base/$archive"
curl -fsSL -o "$tmp/checksums.txt" "$base/checksums.txt"

# Verify before anything is executed or installed.
expected=$(grep " $archive\$" "$tmp/checksums.txt" | cut -d' ' -f1)
[ -n "$expected" ] || die "no checksum for $archive in checksums.txt"
if command -v sha256sum >/dev/null 2>&1; then
  actual=$(sha256sum "$tmp/$archive" | cut -d' ' -f1)
else
  actual=$(shasum -a 256 "$tmp/$archive" | cut -d' ' -f1)
fi
[ "$expected" = "$actual" ] || die "checksum mismatch for $archive (expected $expected, got $actual)"

tar -xzf "$tmp/$archive" -C "$tmp" cloud

if [ -w "$INSTALL_DIR" ]; then
  install -m 0755 "$tmp/cloud" "$INSTALL_DIR/cloud"
else
  say "Installing to $INSTALL_DIR requires sudo."
  sudo install -m 0755 "$tmp/cloud" "$INSTALL_DIR/cloud"
fi

say "Installed: $("$INSTALL_DIR/cloud" version)"
say "Next: cloud login"
