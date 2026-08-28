#!/usr/bin/env bash
# Run this repository's test suite inside a container, so a test that reaches
# past its own sandbox cannot reach the machine you are working on.
#
# Usage:
#   scripts/test-in-container.sh                 # go test ./... -count=1
#   scripts/test-in-container.sh ./internal/tmux/ -run TestFoo
#   NTM_TEST_DECOY=1 scripts/test-in-container.sh
#   NTM_TEST_SRC=/path/to/main/tree NTM_TEST_REF=<commit> scripts/test-in-container.sh
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
# NTM_TEST_SRC overrides which tree is copied in. It exists for NTM_TEST_REF:
# a linked worktree's .git is a file pointing into the main repository, a path
# that does not exist inside the container, so a checkout there fails. Point
# this at the main working tree when you need to test another commit.
SCRIPT_ROOT=$(git -C "$(dirname "${BASH_SOURCE[0]}")/.." rev-parse --show-toplevel)
REPO_ROOT=${NTM_TEST_SRC:-$SCRIPT_ROOT}

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
"$runtime" build --file "$SCRIPT_ROOT/test.Containerfile" --tag "$IMAGE" "$SCRIPT_ROOT"

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

# NTM_TEST_REF checks the copied tree out at another commit before testing.
# The copy carries .git with it, so this needs no second worktree on the host
# and cannot disturb the one you are working in - which is what makes an
# A/B run against a commit from before a fix safe to do at all.
#
# NTM_TEST_DECOY=1 puts a session on the container's own default socket before
# the run and asks afterwards whether it survived. That is the actual question
# behind bd-wkq: not whether the suite passes, but whether it terminates a
# server it did not start. A decoy inside a container is the only place that
# question can be asked without a real session being the experiment.
# The decoy goes up FIRST, and is proven up before anything else runs. A setup
# step that fails after the check is armed but before the session exists would
# otherwise report the session as killed - a false alarm indistinguishable from
# the real finding, which is the one thing this check cannot afford. It already
# happened once: a failed checkout produced a confident "DECOY KILLED" for a
# suite that had not run a single test.
setup=""
if [[ ${NTM_TEST_DECOY:-} == 1 ]]; then
	setup='tmux new-session -d -s decoy && tmux has-session -t decoy || { echo "DECOY SETUP FAILED: no session to watch, the run proves nothing"; exit 2; }; '
fi
if [[ -n ${NTM_TEST_REF:-} ]]; then
	printf -v setup '%sgit -C /work checkout --quiet %q && ' "$setup" "$NTM_TEST_REF"
fi

check=""
if [[ ${NTM_TEST_DECOY:-} == 1 ]]; then
	# The suite's own exit status is kept: a decoy that dies is reported on top
	# of it, never in place of it.
	# shellcheck disable=SC2016  # $? and $status belong to the container's shell, not this one
	check='; status=$?; if tmux has-session -t decoy 2>/dev/null; then echo "DECOY SURVIVED"; else echo "DECOY KILLED: the suite terminated a tmux server it did not start"; status=1; fi; exit $status'
fi

echo "==> running: go test ${test_args[*]}${NTM_TEST_REF:+ (at $NTM_TEST_REF)}${NTM_TEST_DECOY:+ , with decoy}"
exec "$runtime" run --rm \
	--volume "$REPO_ROOT:/src:ro" \
	--volume ntm-suite-gomod:/go/pkg/mod \
	--volume ntm-suite-gocache:/home/ntm/.cache/go-build \
	--workdir /work \
	"$IMAGE" \
	bash -c "cp -a /src/. /work/ && ${setup}go test \"\$@\"${check}" -- "${test_args[@]}"
