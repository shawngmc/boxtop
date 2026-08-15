#!/bin/sh
# Tears down the container started by create-test-container.sh.
set -eu

CONTAINER_NAME="${BOXTOP_TEST_CONTAINER:-boxtop-manual-test}"

if ! command -v docker >/dev/null 2>&1; then
	echo "docker not found in PATH" >&2
	exit 1
fi

if ! docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
	echo "container '$CONTAINER_NAME' does not exist, nothing to do"
	exit 0
fi

echo "removing container '$CONTAINER_NAME'..."
docker rm -f "$CONTAINER_NAME" >/dev/null
echo "done"
