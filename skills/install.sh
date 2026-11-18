#!/usr/bin/env bash
# Install the sash skill from a checkout: copies skills/sash to
# ${1:-$HOME/.claude/skills}/sash and prints each file it wrote. It
# overwrites the four files it installs (SKILL.md and reference/*.md) and
# leaves any other file there alone. Without a checkout, use the copy
# inside the binary: sash --skill (README).
#
# Threats: writing through a symlink or a hard link planted at the target
# (the skills directory, sash/, reference/, or a file) into some other
# place, and a relative target resolved against the wrong directory. The
# target is made absolute before anything else and the script never
# changes directory; a symlink anywhere at the target, a file with more
# than one hard link, or a directory where a file goes is refused before
# anything is written. It does not guard against another process changing
# the target while it runs. It copies only these files and runs nothing else.
set -euo pipefail

usage() {
    echo "usage: skills/install.sh [skills-dir]" >&2
    exit 2
}
[[ $# -le 1 ]] || usage
src="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/sash"
dest="${1-$HOME/.claude/skills}"
[[ -n "$dest" && "$dest" != -* ]] || usage
if [[ "$dest" != /* ]]; then
    dest="$PWD/$dest"
fi

refuse() {
    echo "install: $1" >&2
    exit 1
}

files=(SKILL.md reference/features.md reference/language.md reference/verbs.md)
for d in "$dest" "$dest/sash" "$dest/sash/reference"; do
    if [[ -L "$d" ]]; then refuse "$d is a symlink; refusing to follow it"; fi
    if [[ -e "$d" && ! -d "$d" ]]; then refuse "$d is not a directory"; fi
done
for f in "${files[@]}"; do
    out="$dest/sash/$f"
    if [[ -L "$out" ]]; then refuse "$out is a symlink; refusing to follow it"; fi
    if [[ -d "$out" ]]; then refuse "$out is a directory"; fi
    if [[ -e "$out" && -n "$(find "$out" -prune -links +1)" ]]; then
        refuse "$out has more than one hard link; refusing to write through it"
    fi
done
mkdir -p -- "$dest/sash/reference"
for f in "${files[@]}"; do
    out="$dest/sash/$f"
    cp -- "$src/$f" "$out"
    echo "wrote $out"
done
