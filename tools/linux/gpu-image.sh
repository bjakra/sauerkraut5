#!/usr/bin/env bash
# gpu-image.sh — everything from a checkout to the 1st gen Show 5's root filesystem with the screen on
# its GPU, on an x86_64 Linux machine with Go, curl and docker (or podman as docker), and no emulation:
#
#   tools/linux/build-mesa.sh --kbase --cross    Mesa with the kbase patches   (about ten minutes; kept
#                                                 in bin/ until the patches or the build change)
#   tools/gpu/build.sh --cross                   the GPU helper
#   the release named in gpu-rootfs.base          fetched and checked against its SHA-256
#   tools/linux/gpu-rootfs.sh                    this tree's daemon and the above laid into it
#
#   tools/linux/gpu-image.sh [--version vX.Y.Z_sk5.N] [--out rootfs.tar.gz]
#
# The version defaults to the base's with this commit as build detail (vX.Y.Z_sk5.dev.<commit>), the
# output to bin/techo5-rootfs-gpu-<version>.tar.gz. Install it on a unit with
#
#   scp -O bin/techo5-rootfs-gpu-<version>.tar.gz root@<unit>:/data/techo5-linux/
#   ssh root@<unit> 'slotctl install /data/techo5-linux/techo5-rootfs-gpu-<version>.tar.gz && reboot'
#
# which puts it in the slot not running, on trial: a slot that does not come up three times is left for
# the one running now (tools/linux/README.md, "Updates and rollback").
#
# This is what the fork's GitHub Actions workflow (.github/workflows/gpu-image.yml) runs too.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
VERSION=; OUT=
while [ $# -gt 0 ]; do
	case "$1" in
	--version) VERSION=$2; shift 2;;
	--out) OUT=$2; shift 2;;
	*) echo "usage: $0 [--version vX.Y.Z_sk5.N] [--out rootfs.tar.gz]" >&2; exit 1;;
	esac
done
. "$ROOT/tools/linux/gpu-rootfs.base"
[ -n "$VERSION" ] || VERSION=${BASE_VERSION}_sk5.dev.$(git -C "$ROOT" rev-parse --short HEAD)
[ -n "$OUT" ] || OUT=$ROOT/bin/techo5-rootfs-gpu-$VERSION.tar.gz
mkdir -p "$ROOT/bin"

# Mesa is rebuilt when what it is made from has changed: the script, the cross file and the patches.
MESA=$ROOT/bin/techo5-mesa-arm-kbase.tar.gz
made_of=$(cat "$ROOT/tools/linux/build-mesa.sh" "$ROOT/tools/linux/mesa-armv7-cross.ini" "$ROOT"/tools/linux/patches/mesa/*.patch | sha256sum | cut -d' ' -f1)
if [ -e "$MESA" ] && [ "$(cat "$MESA.made-of" 2>/dev/null)" = "$made_of" ]; then
	echo "== Mesa: bin/$(basename "$MESA") is current"
else
	echo "== Mesa (about ten minutes)"
	rm -f "$MESA.made-of"
	sh "$ROOT/tools/linux/build-mesa.sh" --kbase --cross
	echo "$made_of" > "$MESA.made-of"
fi

echo "== the GPU helper"
sh "$ROOT/tools/gpu/build.sh" --cross

BASE=$ROOT/bin/rootfs-$BASE_VERSION.tar.gz
if [ ! -e "$BASE" ] || [ "$(sha256sum "$BASE" | cut -d' ' -f1)" != "$BASE_SHA256" ]; then
	echo "== fetching the $BASE_VERSION root filesystem"
	curl -fsSL --retry 3 --retry-all-errors -o "$BASE.part" "https://github.com/HuskerMinion/techo5/releases/download/$BASE_VERSION/rootfs-$BASE_VERSION.tar.gz"
	mv "$BASE.part" "$BASE"
fi

bash "$ROOT/tools/linux/gpu-rootfs.sh" --base "$BASE" --sha256 "$BASE_SHA256" --version "$VERSION" --out "$OUT"
