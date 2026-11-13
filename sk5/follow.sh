#!/usr/bin/env bash
# follow.sh — the fork on a newer TECHO5 release, for the image built from it (sk5/follow.yml's job).
#
#   sk5/follow.sh <TECHO5 tag> <out dir> [--patches]
#
# Run in a checkout of the fork's branch with TECHO5's tag fetched. It first merges the tag, as one
# would by hand; a merge that goes through is the fork as it was, on the new release. One that does not
# (or --patches) is built part by part instead (sk5/parts): TECHO5's tree, the fork's own files, and each
# part's changes to TECHO5's files made with a three-way apply, the parts that no longer apply left out.
# Either way the result is then built and tested, and a part whose own files no longer build against
# TECHO5 is left out too, by its build tag. The core cannot be left out.
#
# It leaves the result committed (HEAD), and in <out dir>:
#   result     clean (every part, its tests pass), degraded (a part left out, or a test failing) or
#              failed (the core does not fit: nothing to build)
#   tags       the build tags leaving out the parts left out
#   notes.md   what happened, for the release's notes
#   method     merge or patches
set -euo pipefail

T=${1:?usage: sk5/follow.sh <TECHO5 tag> <out dir> [--patches]}
OUT=${2:?usage: sk5/follow.sh <TECHO5 tag> <out dir> [--patches]}
PATCHES=${3:-}
ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
mkdir -p "$OUT"
NOTES="$OUT/notes.md"
: > "$NOTES"
note() { printf '%s\n' "$*" >> "$NOTES"; }
warn() { note "- ⚠️ $*"; echo "follow: warning: $*" >&2; }
finish() {
	echo "$1" > "$OUT/result"
	echo "${TAGS:-}" > "$OUT/tags"
	echo "follow: $1"
	exit 0
}

# shellcheck source=../tools/linux/gpu-rootfs.base
B=$(. tools/linux/gpu-rootfs.base && echo "$BASE_VERSION")
FORK=$(git rev-parse HEAD)
git rev-parse -q --verify "$T^{commit}" > /dev/null || { echo "follow: no $T here: fetch TECHO5's tags first" >&2; exit 1; }
note "Following TECHO5 $T (the fork was on $B)."
note ""

# The parts, from sk5/parts: names in order, and each one's need, tag, changes and own files.
declare -a PARTS=()
declare -A NEED TAG CHANGES OWNS
part=
while read -r kind a b c; do
	case "$kind" in
	part) part=$a; PARTS+=("$a"); NEED[$a]=$b; TAG[$a]=${c:-}; CHANGES[$a]=; OWNS[$a]= ;;
	change) CHANGES[$part]+="$a " ;;
	own) OWNS[$part]+="$a " ;;
	esac
done < <(grep -v '^[[:space:]]*\(#\|$\)' sk5/parts)

# Every TECHO5 file the fork changes has to be a part's: one no part names would go in with none.
for f in $(git diff --name-only --diff-filter=M "$B" "$FORK"); do
	named=
	for p in "${PARTS[@]}"; do
		case " ${CHANGES[$p]}" in *" $f "*) named=1 ;; esac
	done
	[ -n "$named" ] || { echo "follow: $f is changed by the fork but in no part of sk5/parts" >&2; exit 1; }
done

# ownerOf is the part a file belongs to: the one changing it or owning it; the core for any other file
# of the fork's own; and for one of TECHO5's that the fork leaves alone, techo5.
OWN=" $(git diff --name-only --diff-filter=A "$B" "$FORK" | tr '\n' ' ') "
ownerOf() {
	local f=$1 p pat
	for p in "${PARTS[@]}"; do
		case " ${CHANGES[$p]}" in *" $f "*) echo "$p"; return ;; esac
		for pat in ${OWNS[$p]}; do
			# shellcheck disable=SC2053 # a pattern, matched as one
			[[ $f == $pat ]] && { echo "$p"; return; }
		done
	done
	case "$OWN" in *" $f "*) echo core ;; *) echo techo5 ;; esac
}

declare -A LEFT=()
TAGS=
# leaveOut leaves a part out of the tree: its changes to TECHO5's files undone, its tag set.
leaveOut() {
	local p=$1 why=$2 f
	[ -z "${LEFT[$p]:-}" ] || return 0
	if [ "${NEED[$p]}" = required ]; then
		warn "the $p cannot be left out ($why): nothing is built"
		finish failed
	fi
	LEFT[$p]=1
	for f in ${CHANGES[$p]}; do
		if git cat-file -e "$T:$f" 2> /dev/null; then
			git checkout -q "$T" -- "$f"
		fi
	done
	[ -z "${TAG[$p]}" ] || TAGS="$TAGS ${TAG[$p]}"
	TAGS=${TAGS# }
	warn "$p left out: $why"
}

METHOD=merge
if [ "$PATCHES" != --patches ] && git merge -q --no-ff --no-edit -m "Merge TECHO5 $T" "$T" > "$OUT/merge.log" 2>&1; then
	note "TECHO5's $T merged into the fork as it was."
else
	git merge --abort > /dev/null 2>&1 || true
	METHOD=patches
	[ "$PATCHES" = --patches ] || note "TECHO5's $T does not merge into the fork as it was ($(grep -c CONFLICT "$OUT/merge.log" || true) conflicts); built part by part instead."
	git checkout -q -B "sk5-follow/$T" "$T"
	# The fork's own files, as they are.
	for f in $(git diff --name-only --diff-filter=A "$B" "$FORK"); do
		git cat-file -e "$T:$f" 2> /dev/null && warn "TECHO5 has a $f of its own now: the fork's is used"
		mkdir -p "$(dirname "$f")"
		git show "$FORK:$f" > "$f"
		[ "$(git ls-tree "$FORK" -- "$f" | cut -c1-6)" != 100755 ] || chmod 755 "$f"
	done
	# Each part's changes to TECHO5's files.
	for p in "${PARTS[@]}"; do
		[ -n "${CHANGES[$p]}" ] || continue
		# shellcheck disable=SC2086 # the part's files, one argument each
		git diff "$B" "$FORK" -- ${CHANGES[$p]} > "$OUT/$p.patch"
		[ -s "$OUT/$p.patch" ] || continue
		if git apply --3way "$OUT/$p.patch" > "$OUT/$p.apply.log" 2>&1; then
			note "- $p: applied"
		else
			leaveOut "$p" "its changes no longer apply to TECHO5's $(grep -E '^(U |error: patch failed)' "$OUT/$p.apply.log" | grep -oE '[^ :]+\.(go|c|sh|md)' | sort -u | tr '\n' ' ')"
		fi
	done
fi
echo "$METHOD" > "$OUT/method"

# Built against TECHO5 as it is now: a part whose own files no longer build is left out by its tag.
for try in 1 2 3 4 5 6 7 8; do
	if (cd echod && go vet -tags "$TAGS" ./... > "$OUT/vet.log" 2>&1); then
		break
	fi
	# The parts the errors are in, each with its first.
	declare -A FIRST=()
	while IFS= read -r line; do
		f=$(printf '%s\n' "$line" | sed 's/^[[:space:]]*//' | grep -oE '^[^ :]*\.go' | head -1 || true)
		[ -n "$f" ] || continue
		f=${f#./}
		[ -e "echod/$f" ] && f=echod/$f
		[ -e "$f" ] || continue
		o=$(ownerOf "$f")
		[ -n "${FIRST[$o]:-}" ] || FIRST[$o]=$line
	done < "$OUT/vet.log"
	unset 'FIRST[techo5]'
	[ ${#FIRST[@]} -gt 0 ] || { warn "the build fails in TECHO5's own files alone: $(head -c 400 "$OUT/vet.log")"; finish failed; }
	progress=
	for o in "${!FIRST[@]}"; do
		[ -z "${LEFT[$o]:-}" ] || continue
		leaveOut "$o" "it no longer builds against TECHO5 (${FIRST[$o]:0:200})"
		progress=1
	done
	unset FIRST
	[ -n "$progress" ] || { warn "the build still fails with every part it names left out"; finish failed; }
	[ "$try" -lt 8 ] || { warn "the build does not come right"; finish failed; }
done

# The tests, as TECHO5's and the fork's own: a failing one does not stop the build, it is a warning.
TESTS=ok
if ! (cd echod && go test -tags "$TAGS" ./... > "$OUT/test.log" 2>&1); then
	TESTS=failing
	warn "tests failing: $(grep -E '^(--- FAIL|FAIL)' "$OUT/test.log" | head -8 | tr '\n' ';')"
fi

if [ "$METHOD" = patches ]; then
	# Committed as the merge it stands for, the fork and TECHO5's tag its parents: the fork's branch can
	# go on from it as from any merge.
	git add -A
	tree=$(git write-tree)
	merge=$(git commit-tree "$tree" -p "$FORK" -p "$(git rev-parse "$T^{commit}")" \
		-m "Merge TECHO5 $T part by part${TAGS:+, without $TAGS}" -m "Made by sk5/follow.sh.")
	git reset -q --hard "$merge"
elif [ -n "$TAGS" ]; then
	git add -A
	git commit -q -m "TECHO5 $T without $TAGS" -m "Made by sk5/follow.sh." || true
fi
note ""
if [ ${#LEFT[@]} -eq 0 ] && [ "$TESTS" = ok ]; then
	note "Every part of the fork is in, and its tests pass."
	finish clean
fi
left="${!LEFT[*]}"
note "Left out: ${left:-none}. Tests: $TESTS."
finish degraded
