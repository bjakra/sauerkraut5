#!/bin/sh
# build.sh — techo5-gpu, in an Alpine container of the root filesystem's architecture, against Alpine's
# EGL/GLES/GBM/libdrm headers; at run time it loads the lean Mesa of tools/linux/build-mesa.sh, which has
# the same libraries.
#
#   tools/gpu/build.sh [arm|arm64] [--cross]
#
# arm (the default) is the armv7 userspace every image runs, athena's on its 64-bit kernel too; arm64 is
# for a 64-bit root filesystem. Output: bin/techo5-gpu-<arch> (deploy-rootfs.sh stages it as
# mkrootfs.sh's bin/techo5-gpu).
#
# --cross builds for arm on an x86_64 machine with no emulation, as build-mesa.sh --cross does: clang
# in an x86_64 Alpine container, against Alpine's armv7 packages unpacked as a sysroot.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
ARCH=arm CROSS=
for arg in "$@"; do
	case "$arg" in
	arm|arm64) ARCH=$arg;;
	--cross) CROSS=1;;
	*) echo "usage: $0 [arm|arm64] [--cross]" >&2; exit 1;;
	esac
done
case "$ARCH" in
arm) PLATFORM=linux/arm/v7 IMAGE=arm32v7/alpine:3.24;;
arm64) PLATFORM=linux/arm64 IMAGE=arm64v8/alpine:3.24;;
esac
if [ -n "$CROSS" ]; then
	[ "$ARCH" = arm ] || { echo "$0: --cross builds arm only" >&2; exit 1; }
	mkdir -p "$ROOT/bin"
	docker run --rm --platform linux/amd64 -v "$ROOT/tools/gpu:/src:ro" -v "$ROOT/bin:/out" docker.io/amd64/alpine:3.24 sh -ec '
		apk add -q --no-cache clang lld llvm >/dev/null
		apk add -q --no-cache --root /sysroot --arch armv7 --initdb --no-scripts --keys-dir /usr/share/apk/keys/armv7 \
			-X https://dl-cdn.alpinelinux.org/alpine/v3.24/main -X https://dl-cdn.alpinelinux.org/alpine/v3.24/community \
			musl-dev gcc linux-headers mesa-dev libdrm-dev >/dev/null
		clang --target=armv7-alpine-linux-musleabihf --sysroot=/sysroot -fuse-ld=lld -march=armv7-a -mfpu=neon \
			-mfloat-abi=hard -mthumb -O2 -Wall -Wextra -Wno-unused-parameter -Wno-sign-compare \
			-I/sysroot/usr/include/libdrm -o /tmp/techo5-gpu /src/techo5-gpu.c -lEGL -lGLESv2 -lgbm -ldrm
		llvm-strip /tmp/techo5-gpu
		cp /tmp/techo5-gpu /out/techo5-gpu-arm
		ls -l /out/techo5-gpu-arm'
	exit 0
fi
# The architecture's own Alpine image: a plain alpine tag pulled for one platform would stand in for
# the other, and the build would come out for the wrong one.
mkdir -p "$ROOT/bin"
docker run --rm --platform "$PLATFORM" -e ARCH="$ARCH" -v "$ROOT/tools/gpu:/src:ro" -v "$ROOT/bin:/out" "$IMAGE" sh -ec '
	apk add -q --no-cache build-base mesa-dev libdrm-dev >/dev/null
	gcc -O2 -Wall -Wextra -Wno-unused-parameter -o /tmp/techo5-gpu /src/techo5-gpu.c \
		$(pkg-config --cflags --libs egl glesv2 gbm libdrm)
	strip /tmp/techo5-gpu
	cp /tmp/techo5-gpu "/out/techo5-gpu-$ARCH"
	ls -l "/out/techo5-gpu-$ARCH"'
