#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

usage() {
    cat <<'EOF'
Usage: ./build.sh [build|install|clean]

  build    Build bin/sash (default).
  install  Install sash using go install (respects GOBIN and GOPATH).
  clean    Remove the local build output.
EOF
}

if [[ $# -gt 1 ]]; then
    usage >&2
    exit 1
fi

case "${1:-build}" in
    build)
        mkdir -p bin
        go build -o bin/sash ./cmd/sash
        ;;
    install)
        go install ./cmd/sash
        ;;
    clean)
        rm -f bin/sash
        if [[ -d bin ]]; then
            rmdir bin 2>/dev/null || true
        fi
        ;;
    -h|--help|help)
        usage
        ;;
    *)
        printf 'Unknown option: %s\n' "$1" >&2
        usage >&2
        exit 1
        ;;
esac
