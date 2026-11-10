#!/usr/bin/env bash
# gpu-rootfs.sh — the 1st gen Show 5's root filesystem with the screen drawn on its GPU: a released
# root filesystem with this tree's daemon, the GPU helper (tools/gpu) and the Mesa it loads (panfrost on
# the vendor kernel's kbase driver, build-mesa.sh --kbase) laid into it.
#
#   tools/linux/gpu-rootfs.sh --base rootfs-vX.Y.Z.tar.gz --version vX.Y.Z_sk5.N --out rootfs.tar.gz
#                             [--sha256 <the base's>]
#
# A released root filesystem and not one built here by mkrootfs.sh, because that needs the receivers,
# the video decoder and the rest built first (each in an emulated ARM container) and QEMU to run the
# packages' scripts; the release has all of that, and nothing this adds touches it. What changes:
#
#   /usr/local/bin/techo5       this tree's daemon, stamped with --version
#   /usr/local/bin/techo5-gpu   bin/techo5-gpu-arm (tools/gpu/build.sh)
#   /usr/lib                    bin/techo5-mesa-arm-kbase.tar.gz (tools/linux/build-mesa.sh --kbase), and
#                               libdrm, the one library of those it links that the release lacks,
#                               installed by apk so the package database knows it
#   /usr/share/licenses/mesa    tools/linux/licenses/mesa
#   /etc/techo5-release, /etc/techo5-packages
#
# With the helper in the image echod draws the screen through it and falls back to the cores when it
# fails (hardware/screen); without /dev/mali0 or with a kernel that has DRM the same image behaves as
# the helper does there. The base must carry no vendor tree (a published one never does): what comes
# out is as publishable as what went in.
#
# The version has to be one Home Assistant can rank (update.ValidVersion): the base's own with build
# detail after an underscore ranks as the base does, so the next release is still offered. Installing
# that one takes the unit back to the release's own image, drawn on the cores.
#
# Runs the laying-in inside an x86_64 Alpine container (docker, or podman as docker), as root there, so
# the files keep their owners; nothing of the root filesystem is run.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/../.." && pwd)
GO=${GO:-go}
BASE=; VERSION=; OUT=; SHA256=
while [ $# -gt 0 ]; do
	case "$1" in
	--base) BASE=$2; shift 2;;
	--version) VERSION=$2; shift 2;;
	--out) OUT=$2; shift 2;;
	--sha256) SHA256=$2; shift 2;;
	*) echo "usage: $0 --base rootfs.tar.gz --version vX.Y.Z_detail --out out.tar.gz [--sha256 hex]" >&2; exit 1;;
	esac
done
[ -n "$BASE" ] && [ -n "$VERSION" ] && [ -n "$OUT" ] || { echo "usage: $0 --base rootfs.tar.gz --version vX.Y.Z_detail --out out.tar.gz [--sha256 hex]" >&2; exit 1; }
[ -f "$BASE" ] || { echo "$BASE: no such file" >&2; exit 1; }
echo "$VERSION" | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.]+)?(_[0-9A-Za-z.-]+)?$' ||
	{ echo "$VERSION is not a version Home Assistant can rank: vX.Y.Z, optionally -prerelease and _detail" >&2; exit 1; }
base_sum=$(sha256sum "$BASE" | cut -d' ' -f1)
[ -z "$SHA256" ] || [ "$SHA256" = "$base_sum" ] || { echo "$BASE has sha256 $base_sum, not $SHA256" >&2; exit 1; }

G=$ROOT/bin/techo5-gpu-arm M=$ROOT/bin/techo5-mesa-arm-kbase.tar.gz
[ -e "$G" ] || { echo "bin/techo5-gpu-arm is missing: build it with tools/gpu/build.sh" >&2; exit 1; }
[ -e "$M" ] || { echo "bin/techo5-mesa-arm-kbase.tar.gz is missing: build it with tools/linux/build-mesa.sh --kbase" >&2; exit 1; }
# An ELF32 for ARM (class 1, e_machine 40), like the rest of the userspace.
[ "$(od -An -tx1 -j4 -N1 "$G" | tr -d ' ')" = 01 ] && [ "$(od -An -tu2 -j18 -N2 "$G" | tr -d ' ')" = 40 ] ||
	{ echo "bin/techo5-gpu-arm is not an armv7 binary: build it with tools/gpu/build.sh" >&2; exit 1; }

echo "== building the daemon for armv7 ($VERSION)"
pkg=github.com/HuskerMinion/techo5/echod/internal/layout
commit=$(git -C "$ROOT" rev-parse --short HEAD)
[ -z "$(git -C "$ROOT" status --porcelain -- echod tools/gpu tools/linux/patches 2>/dev/null)" ] || commit=$commit-dirty
date=${BUILD_DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}
(cd "$ROOT/echod" && GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 "$GO" build -trimpath \
	-ldflags "-s -w -X $pkg.Version=$VERSION -X $pkg.GitCommit=$commit -X $pkg.BuildDate=$date" -o "$ROOT/bin/echod-arm" ./cmd/echod)

STAGE=$ROOT/bin/gpu-rootfs-stage
rm -rf "$STAGE"
mkdir -p "$STAGE/licenses"
cp "$BASE" "$STAGE/base.tar.gz"
cp "$ROOT/bin/echod-arm" "$STAGE/techo5"
cp "$G" "$STAGE/techo5-gpu"
cp "$M" "$STAGE/mesa.tar.gz"
cp -r "$ROOT/tools/linux/licenses/mesa" "$STAGE/licenses/mesa"
# The line slotctl shows for a slot, in mkrootfs.sh's form (its date is read from it), then what this
# image was made of.
echo "techo5 rootfs $VERSION built $(date -u '+%Y-%m-%dT%H:%MZ'), daemon echod version $VERSION ($commit, $date); on $(basename "$BASE") (sha256 ${base_sum:0:16}) with the GPU helper and Mesa for kbase" > "$STAGE/release"
echo "== daemon $(sha256sum "$STAGE/techo5" | cut -d' ' -f1), gpu $(sha256sum "$G" | cut -d' ' -f1), mesa $(sha256sum "$M" | cut -d' ' -f1)"

echo "== laying them into $(basename "$BASE")"
docker run --rm --platform linux/amd64 -v "$STAGE:/stage" docker.io/amd64/alpine:3.24 sh -ec '
	apk add -q --no-cache tar >/dev/null
	R=/root-fs
	mkdir $R
	tar --numeric-owner -xpzf /stage/base.tar.gz -C $R
	for f in sbin/init usr/local/bin/techo5 etc/techo5/boot.sh etc/inittab etc/apk/arch; do
		[ -e "$R/$f" ] || { echo "the base is not a techo5 rootfs: $f missing" >&2; exit 1; }
	done
	[ "$(cat $R/etc/apk/arch)" = armv7 ] || { echo "the base is for $(cat $R/etc/apk/arch), not armv7" >&2; exit 1; }
	# Nothing of Amazon and the chip makers goes out with an image: a unit mounts its own.
	[ -z "$(ls -A $R/vendor 2>/dev/null)" ] || { echo "the base carries a vendor tree: a development image, not one to build on" >&2; exit 1; }
	[ ! -e $R/usr/local/bin/techo5-gpu ] || { echo "the base already has a GPU helper" >&2; exit 1; }
	install -m 755 /stage/techo5 $R/usr/local/bin/techo5
	install -m 755 /stage/techo5-gpu $R/usr/local/bin/techo5-gpu
	# What Mesa links to, as mkrootfs.sh adds it; all but libdrm are in a release already. No package
	# script is run (there is no emulation here to run one with), and none of these has any.
	apk --root $R --arch armv7 --no-cache --no-scripts add libdrm expat zlib zstd-libs libstdc++ libgcc
	tar --numeric-owner -xzf /stage/mesa.tar.gz -C $R
	mkdir -p $R/usr/share/licenses/mesa
	cp -r /stage/licenses/mesa/. $R/usr/share/licenses/mesa/
	find $R/usr/share/licenses/mesa -type f -exec chmod 644 {} +
	apk --root $R --arch armv7 info -v | sort > $R/etc/techo5-packages
	cp /stage/release $R/etc/techo5-release
	chmod 644 $R/etc/techo5-release
	tar --numeric-owner -czf /stage/out.tar.gz -C $R .
	cat $R/etc/techo5-release
	echo "$(du -sh $R | cut -f1) unpacked"
'
mkdir -p "$(dirname "$OUT")"
mv "$STAGE/out.tar.gz" "$OUT"
rm -rf "$STAGE"
echo "built: $OUT ($(du -h "$OUT" | cut -f1), sha256 $(sha256sum "$OUT" | cut -d' ' -f1))"
