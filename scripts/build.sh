#!/bin/sh
# Builds a slim boxtop binary, matching the release workflow's build step.
# Usage: scripts/build.sh [goarch]
# goarch defaults to the local machine's architecture (via `go env GOARCH`).
set -eu

cd "$(dirname "$0")/.."

if ! command -v go >/dev/null 2>&1; then
	echo "go not found in PATH" >&2
	exit 1
fi

GOARCH="${1:-$(go env GOARCH)}"
GOOS="${GOOS:-linux}"
VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT="boxtop-${GOOS}-${GOARCH}"

echo "building ${OUT} (version ${VERSION})..."
GOOS="$GOOS" GOARCH="$GOARCH" CGO_ENABLED=0 \
	go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o "$OUT" .
echo "done: $OUT"
