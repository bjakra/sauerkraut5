#!/usr/bin/env bash
# manifest.sh — the signed manifest a release of the fork carries, which the fork's daemons check every
# day (echod/internal/update) and Home Assistant offers from: the image under the fork's own rootfs key
# (sk5/release.env), the daemon beside it, signed with the fork's release key, then checked against the
# public half the daemons carry, so a release never goes out with a manifest they would refuse.
#
#   sk5/manifest.sh <version> <release tag> <rootfs.tar.gz> <echod-arm> <key file> <out dir> [notes]
set -euo pipefail
V=${1:?}; TAG=${2:?}; ROOTFS=${3:?}; DAEMON=${4:?}; KEY=${5:?}; OUT=${6:?}; NOTES=${7:-}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=release.env
. "$ROOT/sk5/release.env"
ROOTFS=$(realpath "$ROOTFS"); DAEMON=$(realpath "$DAEMON"); KEY=$(realpath "$KEY")
mkdir -p "$OUT"; OUT=$(realpath "$OUT")
repo=${SK5_RELEASES#https://github.com/}; repo=${repo%/releases}
cd "$ROOT/echod"
go run ./cmd/mkmanifest -version "$V" -title "TECHO5 $V (GPU)" -notes "${NOTES:-sauerkraut5 $V}" \
	-release-url "https://github.com/$repo/releases/tag/$TAG" -from "$SK5_RELEASES/download/$TAG" \
	-arm "$DAEMON" -rootfs "arm-$SK5_BOARD_ROOTFS=$ROOTFS" -out "$OUT/manifest.json" -sign-key "$KEY"
go run ./cmd/sk5base -manifest "$OUT/manifest.json" -sig "$OUT/manifest.json.sig" -key "$SK5_RELEASE_KEY" -rootfs "arm-$SK5_BOARD_ROOTFS" > /dev/null
echo "manifest: $V for arm-$SK5_BOARD_ROOTFS, signed, and the daemons' key takes it"
