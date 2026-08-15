#!/bin/sh
# Builds boxtop, launches a cgroup-limited alpine container to run it in,
# copies the binary in, and drops you into a shell there. Mirrors the
# --memory/--cpus setup integration/cgroup_container_test.go uses, but
# leaves the container running (via `sleep infinity`) instead of --rm'ing
# it on exit, so you can exec back in after `exit`-ing the shell. Pair with
# delete-test-container.sh to clean up.
set -eu

CONTAINER_NAME="${BOXTOP_TEST_CONTAINER:-boxtop-manual-test}"
MEMORY_LIMIT="${BOXTOP_TEST_MEMORY:-256m}"
MEMORY_SWAP_LIMIT="${BOXTOP_TEST_MEMORY_SWAP:-384m}"
CPU_LIMIT="${BOXTOP_TEST_CPUS:-1.5}"

REPO_ROOT="$(dirname -- "$0")/.."
REPO_ROOT="$(cd -- "$REPO_ROOT" && pwd)"

if ! command -v docker >/dev/null 2>&1; then
	echo "docker not found in PATH" >&2
	exit 1
fi
if ! docker info >/dev/null 2>&1; then
	echo "docker daemon not reachable" >&2
	exit 1
fi

if docker inspect "$CONTAINER_NAME" >/dev/null 2>&1; then
	echo "container '$CONTAINER_NAME' already exists — run delete-test-container.sh first" >&2
	exit 1
fi

echo "building boxtop..."
# CGO_ENABLED=0 for a static binary: a default cgo build dynamically links
# against glibc, which alpine (musl, no glibc) can't exec — it fails with a
# misleading "not found" even though the file is right there.
( cd "$REPO_ROOT" && CGO_ENABLED=0 go build -o boxtop . )

echo "starting container '$CONTAINER_NAME' (memory=$MEMORY_LIMIT memory-swap=$MEMORY_SWAP_LIMIT cpus=$CPU_LIMIT)..."
docker run -d --name "$CONTAINER_NAME" \
	--memory "$MEMORY_LIMIT" \
	--memory-swap "$MEMORY_SWAP_LIMIT" \
	--cpus "$CPU_LIMIT" \
	alpine sleep infinity >/dev/null

echo "copying boxtop into container..."
docker cp "$REPO_ROOT/boxtop" "$CONTAINER_NAME:/boxtop"

echo "dropping into shell — run /boxtop once inside; 'exit' to leave the container running"
exec docker exec -it "$CONTAINER_NAME" /bin/sh
