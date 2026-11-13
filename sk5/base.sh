#!/usr/bin/env bash
# base.sh — the GPU image on a TECHO5 release: tools/linux/gpu-rootfs.base pointed at <tag>'s root
# filesystem, its SHA-256 taken from the release's manifest once TECHO5's key is found to have signed
# it (echod/cmd/sk5base), not from a checksum file anything could serve beside it.
#
#   sk5/base.sh <TECHO5 tag>
set -euo pipefail
T=${1:?usage: sk5/base.sh <TECHO5 tag>}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
# shellcheck source=techo5.env
. "$ROOT/sk5/techo5.env"
D=$(mktemp -d)
trap 'rm -rf "$D"' EXIT
for f in manifest.json manifest.json.sig; do
	curl -fsSL --retry 3 --retry-all-errors -o "$D/$f" "https://github.com/$TECHO5_REPO/releases/download/$T/$f"
done
read -r version sha size url < <(cd "$ROOT/echod" && go run ./cmd/sk5base -manifest "$D/manifest.json" -sig "$D/manifest.json.sig" -key "$TECHO5_RELEASE_KEY")
[ "$version" = "$T" ] || { echo "base: $T's manifest is for $version" >&2; exit 1; }
[ "$(basename "$url")" = "rootfs-$T.tar.gz" ] || { echo "base: $T's root filesystem is $url, not rootfs-$T.tar.gz" >&2; exit 1; }
sed -i "s/^BASE_VERSION=.*/BASE_VERSION=$T/; s/^BASE_SHA256=.*/BASE_SHA256=$sha/" "$ROOT/tools/linux/gpu-rootfs.base"
echo "base: $T, rootfs sha256 $sha ($size bytes), from its signed manifest"
