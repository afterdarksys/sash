#!/usr/bin/env bash
# Install the sash skill from a checkout: copies skills/sash to
# ${1:-$HOME/.claude/skills}/sash and prints each file it wrote.
# Without a checkout, use the copy inside the binary: sash --skill (README).
#
# Threats: writing through a symlink planted at the target (the skills
# directory, sash/, reference/, or a file) into some other place. Every
# one of those is refused if it is a symlink; nothing is followed. It
# copies only the files in this directory and runs nothing else.
set -euo pipefail

src="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)/sash"
if [[ $# -gt 1 ]]; then
    echo "usage: skills/install.sh [skills-dir]" >&2
    exit 2
fi
dest="${1:-$HOME/.claude/skills}"

refuse_link() {
    if [[ -L "$1" ]]; then
        echo "install: $1 is a symlink; refusing to follow it" >&2
        exit 1
    fi
}

refuse_link "$dest"
mkdir -p -- "$dest"
for d in "$dest/sash" "$dest/sash/reference"; do
    refuse_link "$d"
    mkdir -p -- "$d"
done

cd -- "$src"
while IFS= read -r f; do
    out="$dest/sash/$f"
    refuse_link "$out"
    cp -- "$f" "$out"
    echo "wrote $out"
done < <(find . -type f -name '*.md' | sed 's|^\./||' | LC_ALL=C sort)
