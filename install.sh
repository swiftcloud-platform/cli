#!/bin/sh
# Install the SwiftCloud CLI.
#
#   curl -fsSL https://cloud.co.zm/install.sh | sh
#
# Downloads the latest release for this OS and architecture from GitHub,
# verifies its SHA-256 against the release's checksums.txt, and installs the
# `cloud` binary to /usr/local/bin (or $CLOUD_INSTALL_DIR). Linux and macOS
# only; Windows has install.ps1, which does the same thing.
#
# If cosign is on PATH, the release's signature over checksums.txt is checked
# too, which proves the release was built by this repository's release
# workflow and not merely uploaded beside it. Without cosign that check is
# skipped, and the script prints the command to run it by hand.
#
# Everything is inside main(), called on the last line, so a download cut off
# part-way through runs nothing at all.
set -eu

REPO="swiftcloud-platform/cli"

# Releases after v0.3.4 carry checksums.txt.sigstore.json; from this version on,
# a missing signature is a failure, not a skipped check.
FIRST_SIGNED="0.3.5"

# The identity the release workflow signs with, and who vouches for it.
SIGNER_IDENTITY='^https://github\.com/swiftcloud-platform/cli/\.github/workflows/release\.yml@refs/tags/v'
SIGNER_ISSUER='https://token.actions.githubusercontent.com'

say() { printf '%s\n' "$*" >&2; }
die() { say "install.sh: $*"; exit 1; }

need() { command -v "$1" >/dev/null 2>&1 || die "need '$1' on PATH"; }

# HTTPS only, TLS 1.2 or newer, also across redirects.
fetch() { curl --proto '=https' --tlsv1.2 -fsSL "$@"; }

# version_ge A B: is release version A (no "v") at or after B? Compares the
# numeric MAJOR.MINOR.PATCH only; a pre-release counts as its release.
version_ge() {
  awk -v a="$1" -v b="$2" 'BEGIN {
    sub(/[-+].*/, "", a); sub(/[-+].*/, "", b)
    split(a, x, "."); split(b, y, ".")
    for (i = 1; i <= 3; i++) {
      if (x[i] + 0 > y[i] + 0) exit 0
      if (x[i] + 0 < y[i] + 0) exit 1
    }
    exit 0
  }'
}

main() {
  INSTALL_DIR="${CLOUD_INSTALL_DIR:-/usr/local/bin}"
  VERSION="${CLOUD_VERSION:-}"

  need curl; need tar; need uname; need awk

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
    VERSION=$(fetch -I -o /dev/null -w '%{url_effective}' "https://github.com/$REPO/releases/latest" | sed 's#.*/tag/##')
    [ -n "$VERSION" ] || die "could not determine the latest version"
  fi
  ver="${VERSION#v}"

  base="https://github.com/$REPO/releases/download/$VERSION"
  archive="cloud_${ver}_${os}_${arch}.tar.gz"

  tmp=$(mktemp -d)
  trap 'rm -rf "$tmp"' EXIT

  say "Downloading cloud $VERSION for $os/$arch…"
  fetch -o "$tmp/$archive" "$base/$archive"
  fetch -o "$tmp/checksums.txt" "$base/checksums.txt"

  # ── signature over checksums.txt (when cosign is available) ──────────────
  if command -v cosign >/dev/null 2>&1; then
    if fetch -o "$tmp/checksums.txt.sigstore.json" "$base/checksums.txt.sigstore.json" 2>/dev/null; then
      if ! cosign verify-blob "$tmp/checksums.txt" \
        --bundle "$tmp/checksums.txt.sigstore.json" \
        --certificate-identity-regexp "$SIGNER_IDENTITY" \
        --certificate-oidc-issuer "$SIGNER_ISSUER" >"$tmp/cosign.log" 2>&1; then
        cat "$tmp/cosign.log" >&2
        die "the signature on checksums.txt does not verify — nothing was installed"
      fi
      say "Signature verified: built by $REPO's release workflow."
    elif version_ge "$ver" "$FIRST_SIGNED"; then
      die "release $VERSION has no checksums.txt.sigstore.json, but every release from $FIRST_SIGNED on is signed — nothing was installed"
    else
      say "Release $VERSION predates signed releases ($FIRST_SIGNED); checking its checksum only."
    fi
  else
    say "cosign is not installed, so the release signature was not checked. To check it yourself:"
    say "  curl -fsSLO $base/checksums.txt -O $base/checksums.txt.sigstore.json"
    say "  cosign verify-blob checksums.txt --bundle checksums.txt.sigstore.json \\"
    say "    --certificate-identity-regexp '$SIGNER_IDENTITY' \\"
    say "    --certificate-oidc-issuer $SIGNER_ISSUER"
  fi

  # ── checksum of the archive, before anything is extracted or installed ───
  # An exact match on the file-name field: a grep for the name would also
  # match it inside a longer name, and "." in it would match any character.
  # goreleaser may write a "*" before the name (binary mode); that is not
  # part of it.
  expected=$(awk -v f="$archive" '{ n = $2; sub(/^\*/, "", n) } n == f { print $1; exit }' "$tmp/checksums.txt")
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
}

main "$@"
