#!/usr/bin/env bash
# Run this repository's test suite inside a container, so a test that reaches
# past its own sandbox cannot reach the machine you are working on.
#
# Usage:
#   scripts/test-in-container.sh                 # go test ./... -count=1
#   scripts/test-in-container.sh ./internal/tmux/ -run TestFoo
#   KEEP_ARTIFACTS=1 scripts/test-in-container.sh
#
# WHY this exists rather than a documented set of environment variables. Every
# containment built on the host failed the same way: it replaced the tmux
# binary via PATH or NTM_TMUX_BINARY, and parts of the suite deliberately
# resolve /usr/bin/tmux by absolute path (findSystemTmuxBinary in
# tests/testutil/throttle.go), so the replacement was simply not consulted. A
# container removes the question: the host's tmux server is not reachable from
# inside one, whatever path the code resolves.
set -euo pipefail

IMAGE=${NTM_TEST_IMAGE:-ntm-suite:local}
REPO_ROOT=$(git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --show-toplevel)

runtime=""
for candidate in podman docker; do
	if command -v "$candidate" >/dev/null 2>&1; then
		runtime=$candidate
		break
	fi
done
if [[ -z $runtime ]]; then
	echo "no container runtime found (looked for podman, docker)" >&2
	exit 1
fi

echo "==> building $IMAGE with $runtime"
"$runtime" build --file "$REPO_ROOT/Containerfile.test" --tag "$IMAGE" "$REPO_ROOT"

# The repository is mounted read-only and copied inside. A test that writes
# into its own checkout - and several do, leaving .ntm/ directories behind -
# then cannot dirty the working tree you are reading on the host.
#
# The module and build caches are named volumes rather than host paths: a
# container writing into the host's ~/go would be one more way for a run to
# change the machine, which is the thing this script exists to prevent.
test_args=("$@")
if [[ ${#test_args[@]} -eq 0 ]]; then
	test_args=("./..." "-count=1")
fi

echo "==> running: go test ${test_args[*]}"
exec "$runtime" run --rm \
	--volume "$REPO_ROOT:/src:ro" \
	--volume ntm-suite-gomod:/go/pkg/mod \
	--volume ntm-suite-gocache:/root/.cache/go-build \
	--workdir /work \
	"$IMAGE" \
	bash -c 'cp -a /src/. /work/ && exec go test "$@"' -- "${test_args[@]}"
