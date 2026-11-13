#!/usr/bin/env bash
# check-image.sh — a GPU image checked as it is, not on trust in the script that made it: no vendor tree,
# the helper and the libraries it loads there, the daemon saying the version, and Mesa with its kbase
# backend. Writes SHA256SUMS beside it.
#
#   sk5/check-image.sh <image.tar.gz> <version>
set -euo pipefail
F=${1:?}; V=${2:?}
cd "$(dirname "$F")"
f=$(basename "$F")
list=$(mktemp)
trap 'rm -f "$list"' EXIT
tar -tzf "$f" > "$list"
if grep -E '^\./vendor/.+' "$list"; then echo "the image carries a vendor tree" >&2; exit 1; fi
for p in ./usr/local/bin/techo5 ./usr/local/bin/techo5-gpu ./usr/lib/libEGL.so.1 ./usr/lib/libGLESv2.so.2 ./usr/lib/libdrm.so.2 ./usr/share/licenses/mesa/MIT; do
	grep -qxF "$p" "$list" || { echo "the image lacks $p" >&2; exit 1; }
done
tar -xzOf "$f" ./etc/techo5-release | tee /dev/stderr | grep -q "rootfs $V " || { echo "the image is not $V" >&2; exit 1; }
gallium=$(grep -oE '^\./usr/lib/libgallium-[0-9.]+\.so$' "$list" | head -1)
tar -xzOf "$f" "$gallium" | grep -ac "kbase: " > /dev/null || { echo "Mesa in the image has no kbase backend" >&2; exit 1; }
sha256sum "$f" > SHA256SUMS
cat SHA256SUMS
