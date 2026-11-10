#!/bin/sh
# build-mesa.sh — a lean Mesa for the GPU helper (tools/gpu) on the mainline kernels of the Show 8
# 2nd gen and Show 5 2nd gen: panfrost (their Mali-G72 and Mali-T720) with EGL, GLES and GBM, and
# nothing else. Alpine's own mesa-dri-gallium links LLVM for its software and AMD drivers, 170 MB
# that panfrost never uses; this one is built without it. Panfrost compiles some of its own GPU
# programs from OpenCL C when Mesa is built, with mesa_clc and a precompiler that need LLVM; so the
# container builds those two tools first, with LLVM, and then the libraries with them and without
# it. LLVM never leaves the container.
#
#   tools/linux/build-mesa.sh [arm|arm64] [--kbase] [--cross]
#
# arm (the default) is the armv7 userspace every image runs, athena's on its 64-bit kernel too
# (panfrost's ioctls are the same for a 32-bit caller); arm64 is for a 64-bit root filesystem. Runs
# in an Alpine container of that architecture (docker --platform; QEMU's binfmt runs it on another
# machine, slowly: the arm build took an hour on an Apple Silicon host), from the same Mesa release
# Alpine packages, checked against the sha512 Alpine's APKBUILD pins for that tarball (aports,
# 3.24-stable, main/mesa). Output: bin/techo5-mesa-<arch>.tar.gz, the files to lay over the root
# filesystem (/usr/lib; mkrootfs.sh's inputs/techo5-mesa.tar.gz). At run time they need libdrm,
# expat, zlib, zstd-libs, libstdc++ and libgcc from Alpine (mkrootfs.sh adds them with the tarball).
#
# --kbase applies tools/linux/patches/mesa: panfrost on Arm's own kernel driver, kbase (/dev/mali0),
# which is what the 1st gen Show 5's vendor kernel has for its Mali-T720, with no DRM at all. The
# output is then bin/techo5-mesa-<arch>-kbase.tar.gz, so that it is never taken for the other: the
# patches leave the DRM path as it was, but only the kbase path has been run with them.
#
# --cross builds for arm on an x86_64 machine with no emulation, ten minutes instead of an hour: an
# x86_64 Alpine container with clang and lld (mesa-armv7-cross.ini), and an armv7 sysroot filled from
# Alpine's armv7 packages, none of which is run. clang, unlike GCC, cannot turn NEON on for the one
# file of Mesa that asks for it with a pragma, so it is on for all of it: every board TECHO5 runs on
# has it.
set -eu
ROOT=$(CDPATH= cd -- "$(dirname "$0")/../.." && pwd)
ARCH=arm KBASE= CROSS=
for arg in "$@"; do
	case "$arg" in
	arm|arm64) ARCH=$arg;;
	--kbase) KBASE=1;;
	--cross) CROSS=1;;
	*) echo "usage: $0 [arm|arm64] [--kbase] [--cross]" >&2; exit 1;;
	esac
done
case "$ARCH" in
arm) PLATFORM=linux/arm/v7 IMAGE=arm32v7/alpine:3.24;;
arm64) PLATFORM=linux/arm64 IMAGE=arm64v8/alpine:3.24;;
esac
if [ -n "$CROSS" ]; then
	[ "$ARCH" = arm ] || { echo "$0: --cross builds arm only" >&2; exit 1; }
	PLATFORM=linux/amd64 IMAGE=docker.io/amd64/alpine:3.24
fi
OUT=techo5-mesa-$ARCH${KBASE:+-kbase}.tar.gz
# The architecture's own Alpine image: a plain alpine tag pulled for one platform would stand in for
# the other, and the build would come out for the wrong one.
VERSION=26.1.6
SHA512=6746b73f5b87e8ff3723eee586b4b5f033dbe6d13cd0309d743b7fa484c350268859ec8ff21aadcbca7a04070cdaa7828146dd28dde4b1935bb879891d687f0b
mkdir -p "$ROOT/bin"
docker run --rm --platform "$PLATFORM" -e VERSION="$VERSION" -e SHA512="$SHA512" -e ARCH="$ARCH" -e KBASE="$KBASE" \
	-e CROSS="$CROSS" -e OUT="$OUT" -v "$ROOT/bin:/out" -v "$ROOT/tools/linux/patches/mesa:/patches:ro" \
	-v "$ROOT/tools/linux/mesa-armv7-cross.ini:/cross.ini:ro" "$IMAGE" sh -ec '
	apk add -q --no-cache build-base meson samurai pkgconf python3 py3-mako py3-yaml py3-packaging \
		bison flex glslang libdrm-dev expat-dev zlib-dev zstd-dev elfutils-dev curl xz tar patch \
		llvm-dev clang-dev libclc-dev spirv-llvm-translator-dev spirv-tools-dev >/dev/null
	cd /tmp
	curl -fsSLO "https://mesa.freedesktop.org/archive/mesa-$VERSION.tar.xz"
	echo "$SHA512  mesa-$VERSION.tar.xz" | sha512sum -c -
	tar -xJf "mesa-$VERSION.tar.xz"
	cd "mesa-$VERSION"
	if [ -n "$KBASE" ]; then
		for p in /patches/*.patch; do
			echo "patch: $(basename "$p")"
			patch -p1 -s < "$p"
		done
	fi
	CROSS_FILE= STRIP=strip
	if [ -n "$CROSS" ]; then
		# The compiler, and what it links against: Alpine armv7 packages unpacked under /sysroot.
		apk add -q --no-cache clang lld llvm >/dev/null
		apk add -q --no-cache --root /sysroot --arch armv7 --initdb --no-scripts --keys-dir /usr/share/apk/keys/armv7 \
			-X https://dl-cdn.alpinelinux.org/alpine/v3.24/main -X https://dl-cdn.alpinelinux.org/alpine/v3.24/community \
			musl-dev gcc g++ libstdc++-dev linux-headers libdrm-dev expat-dev zlib-dev zstd-dev >/dev/null
		CROSS_FILE="--cross-file /cross.ini" STRIP=llvm-strip
	fi
	# armv7: TLS descriptors (-mtls-dialect=gnu2, the default) are broken in binutils there, and Mesa
	# built with them reads its thread-local state from the wrong place (an alignment fault in
	# libgallium on the first GL call); Alpine builds its own Mesa with the old dialect for the same reason
	# (aports main/mesa, alpine/aports#14140, mesa/mesa#13236).
	# (clang has no such dialect for 32-bit ARM to begin with, and in a cross build the tools built
	# first are for the build machine.)
	[ "$ARCH" = arm ] && [ -z "$CROSS" ] && export CFLAGS="-mtls-dialect=gnu" CXXFLAGS="-mtls-dialect=gnu"
	meson setup tools --prefix=/usr --buildtype=release -Dgallium-drivers=panfrost -Dvulkan-drivers= \
		-Dplatforms= -Degl=disabled -Dgbm=disabled -Dopengl=false -Dgles1=disabled -Dgles2=disabled \
		-Dglx=disabled -Dllvm=enabled -Dmesa-clc=enabled -Dinstall-mesa-clc=true \
		-Dprecomp-compiler=enabled -Dinstall-precomp-compiler=true -Dbuild-tests=false \
		>/tmp/tools.log 2>&1 || { tail -25 /tmp/tools.log; exit 1; }
	ninja -C tools >/tmp/tools-ninja.log 2>&1 || { grep -B2 -A8 -m3 error /tmp/tools-ninja.log; exit 1; }
	find tools -type f -perm -u+x \( -name mesa_clc -o -name "*_compile" -o -name vtn_bindgen2 \) -exec cp {} /usr/bin/ \;
	ls /usr/bin | grep -E "mesa_clc|_compile|vtn_bindgen" | tr "\n" " "; echo
	# The libraries need neither LLVM (-Dllvm=disabled) nor SPIRV-Tools, which meson would link if it
	# found it; the tools above still run on both, so their packages stay and only SPIRV-Tools is hidden.
	# (A cross build looks for its libraries under /sysroot, where it is not.)
	mkdir -p /tmp/hidden && mv /usr/lib/pkgconfig/SPIRV-Tools*.pc /tmp/hidden/
	meson setup build $CROSS_FILE --prefix=/usr --buildtype=release -Db_ndebug=true \
		-Dgallium-drivers=panfrost -Dvulkan-drivers= -Dllvm=disabled -Dplatforms= \
		-Degl=enabled -Dgles1=disabled -Dgles2=enabled -Dopengl=true -Dglx=disabled -Dgbm=enabled \
		-Dmesa-clc=system -Dprecomp-compiler=system \
		-Dvalgrind=disabled -Dlibunwind=disabled -Dzstd=enabled \
		-Dtools= -Dbuild-tests=false >/tmp/meson.log 2>&1 || { tail -25 /tmp/meson.log; exit 1; }
	ninja -C build >/tmp/ninja.log 2>&1 || { grep -i -B2 -A8 -m3 error /tmp/ninja.log; exit 1; }
	DESTDIR=/stage ninja -C build install >/dev/null
	cd /stage
	find . -name "*.a" -o -name "*.pc" -o -path "*/include/*" | xargs rm -rf
	rm -rf usr/include usr/share/pkgconfig usr/lib/pkgconfig
	find . -type f -name "*.so*" -exec $STRIP --strip-unneeded {} +
	tar --owner=0 --group=0 --numeric-owner -czf "/out/$OUT" .
	du -sh /stage; find /stage -type f -name "*.so*" | xargs ls -l | awk "{print \$5, \$9}"
'
