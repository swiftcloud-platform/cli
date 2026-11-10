#!/bin/sh
# Checks that clients already in the field can still find their archive among
# the files a release publishes.
#
# `cloud update` up to 0.3.4 lowercases each release asset name and keeps the
# LAST one containing both its OS and its architecture. Any other file whose
# name holds both (an SBOM, a signature) can therefore be downloaded in place
# of the archive, and the update fails. So for every platform we build there
# must be exactly one such file, and it must be the archive.
#
# Usage: scripts/check-release-assets.sh [dist-dir]   (default: dist)
set -eu

dist=${1:-dist}
[ -d "$dist" ] || { echo "no $dist directory — run goreleaser first" >&2; exit 2; }

status=0
for os in linux darwin windows; do
  for arch in amd64 arm64; do
    ext=tar.gz
    [ "$os" = windows ] && ext=zip
    matches=$(
      for f in "$dist"/*; do
        [ -f "$f" ] || continue
        name=$(basename "$f" | tr '[:upper:]' '[:lower:]')
        case "$name" in *"$os"*) ;; *) continue ;; esac
        case "$name" in *"$arch"*) basename "$f" ;; esac
      done
    )
    count=$(printf '%s' "$matches" | grep -c . || true)
    case "$count:$matches" in
      1:cloud_*_"${os}_${arch}.$ext")
        echo "ok   $os/$arch → $matches" ;;
      *)
        status=1
        echo "FAIL $os/$arch: an updater up to 0.3.4 would see $count candidate(s):" >&2
        printf '       %s\n' $matches >&2 ;;
    esac
  done
done
exit $status
