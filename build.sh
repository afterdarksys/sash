#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
cd "$ROOT_DIR"

usage() {
    cat <<'EOF'
Usage: ./build.sh [build|install|clean|test]

  build    Build bin/sash (default). Add SASH_TAGS=sashwasm for wasm modules.
  install  Install sash using go install (respects GOBIN and GOPATH).
  clean    Remove the local build output.
  test     gofmt, vet, test, and -race, without and with the sashwasm tag,
           then govulncheck when it is installed (it is not installed for
           you).
EOF
}

if [[ $# -gt 1 ]]; then
    usage >&2
    exit 1
fi

case "${1:-build}" in
    build)
        mkdir -p bin
        go build -tags "${SASH_TAGS:-}" -o bin/sash ./cmd/sash
        ;;
    test)
        unformatted="$(gofmt -l cmd internal)"
        if [[ -n "$unformatted" ]]; then
            echo "gofmt: $unformatted" >&2
            exit 1
        fi
        for tags in "" sashwasm; do
            go vet -tags "$tags" ./...
            go test -tags "$tags" ./...
            go test -tags "$tags" -race ./internal/lang ./internal/line
        done
        if command -v govulncheck >/dev/null; then
            govulncheck ./...
            govulncheck -tags sashwasm ./...
        else
            echo "govulncheck not installed: go install golang.org/x/vuln/cmd/govulncheck@latest" >&2
        fi
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
