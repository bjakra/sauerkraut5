#!/bin/sh
# build.sh — the tools that check the 1st gen Show 5's GPU path on a unit (docs/building.md, "the GPU
# image"), for armv7, on an x86_64 machine with Go and docker (or podman as docker), with no emulation:
#
#   bin/kbase-probe   the kbase kernel interface alone (static: needs nothing on the unit)
#   bin/gles-smoke    a triangle through EGL and GLES, read back
#   bin/gles-memfd    the canvas turned onto a page in shared memory, compared with the same on the CPU
#   bin/gles-hang     a job the GPU cannot finish
#   bin/gpucheck      what techo5-gpu puts on the panel, against the same pictures made on the cores
#   bin/screenbench   echod's screen path timed, on the cores or the GPU
#
# The GL ones link against Alpine's EGL and GLES and load whatever the unit has: the GPU image's Mesa.
#
#   tools/gpu/kbase/build.sh
#   scp -O bin/kbase-probe bin/gles-* bin/gpucheck bin/screenbench root@<unit>:/tmp/
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/../../.." && pwd)
mkdir -p "$ROOT/bin"
docker run --rm --platform linux/amd64 -v "$ROOT/tools/gpu/kbase:/src:ro" -v "$ROOT/bin:/out" docker.io/amd64/alpine:3.24 sh -ec '
	apk add -q --no-cache clang lld llvm >/dev/null
	apk add -q --no-cache --root /sysroot --arch armv7 --initdb --no-scripts --keys-dir /usr/share/apk/keys/armv7 \
		-X https://dl-cdn.alpinelinux.org/alpine/v3.24/main -X https://dl-cdn.alpinelinux.org/alpine/v3.24/community \
		musl-dev gcc linux-headers mesa-dev >/dev/null
	cc="clang --target=armv7-alpine-linux-musleabihf --sysroot=/sysroot -fuse-ld=lld -march=armv7-a -mfpu=neon -mfloat-abi=hard -mthumb -O2 -Wall -Wextra"
	$cc -static -o /out/kbase-probe /src/kbase-probe.c
	for t in gles-smoke gles-memfd gles-hang; do
		$cc -o /out/$t /src/$t.c -lEGL -lGLESv2
	done
	llvm-strip /out/kbase-probe /out/gles-smoke /out/gles-memfd /out/gles-hang
	ls -l /out/kbase-probe /out/gles-smoke /out/gles-memfd /out/gles-hang'
for c in gpucheck screenbench; do
	(cd "$ROOT/echod" && GOOS=linux GOARCH=arm GOARM=7 CGO_ENABLED=0 "${GO:-go}" build -trimpath -ldflags "-s -w" -o "$ROOT/bin/$c" "./cmd/$c")
	ls -l "$ROOT/bin/$c"
done
