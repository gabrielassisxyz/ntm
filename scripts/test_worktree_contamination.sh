#!/usr/bin/env bash
# Reproduces bd-c2h and proves `scripts/worktree.sh contamination` catches it.
#
# The defect: `git stash` / `git stash pop` share ONE stash stack across every worktree of a
# repository (the stack lives in the common git dir's refs/stash). Two agents stashing and
# popping concurrently each restore the OTHER's changes into their own tree. This test drives
# that interleaving deliberately in a scratch repository, then asserts the contamination check
# reports the foreign files and exits nonzero — and that a clean pair of worktrees passes.
#
# Everything here is created by this script in a scratch directory and removed by it before
# exit; nothing touches the real repository, its branches, or its worktrees.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKTREE_SH="$REPO_ROOT/scripts/worktree.sh"

PASSED=0
FAILED=0
pass() { PASSED=$((PASSED + 1)); echo "PASS: $*"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL: $*" >&2; }

# A scratch repository with a real origin, so `contamination` has a refs/remotes/origin/main
# to measure each branch's own diff against. The worktrees live inside the scratch dir, so
# removing the dir removes them and their registrations together.
SCRATCH="$(mktemp -d)"
ORIGIN="$SCRATCH/origin.git"
REPO="$SCRATCH/repo"
cleanup() { rm -rf "$SCRATCH"; }
trap cleanup EXIT

git init -q --bare "$ORIGIN"
git init -q -b main "$REPO"
git -C "$REPO" config user.email test@example.com
git -C "$REPO" config user.name test
printf 'base-a\n' > "$REPO/a.txt"
printf 'base-b\n' > "$REPO/b.txt"
git -C "$REPO" add a.txt b.txt
git -C "$REPO" commit -qm init
git -C "$REPO" remote add origin "$ORIGIN"
git -C "$REPO" push -q -u origin main

# Two worktrees, each on its own branch, like two agents dispatched into one repository.
git -C "$REPO" worktree add -q -b agent-a "$SCRATCH/wt-a" origin/main
git -C "$REPO" worktree add -q -b agent-b "$SCRATCH/wt-b" origin/main

run_check() { (cd "$REPO" && bash "$WORKTREE_SH" contamination); }

# ---------------------------------------------------------------------------
# Positive: two clean worktrees pass.
# ---------------------------------------------------------------------------
if out="$(run_check 2>&1)"; then
    pass "contamination: clean worktrees pass (exit 0)"
else
    fail "contamination: clean worktrees were reported contaminated: $out"
fi

# ---------------------------------------------------------------------------
# Negative: the crossing. Each agent modifies its own tracked file, then both
# stash and pop in the interleaving that makes each pop restore the OTHER's change.
# ---------------------------------------------------------------------------
printf 'a-work\n' > "$SCRATCH/wt-a/a.txt"
printf 'b-work\n' > "$SCRATCH/wt-b/b.txt"
git -C "$SCRATCH/wt-a" stash -q
git -C "$SCRATCH/wt-b" stash -q
git -C "$SCRATCH/wt-a" stash pop -q
git -C "$SCRATCH/wt-b" stash pop -q

# Sanity: the crossing actually happened before we ask the check to see it.
wt_a_dirty="$(git -C "$SCRATCH/wt-a" status --porcelain)"
wt_b_dirty="$(git -C "$SCRATCH/wt-b" status --porcelain)"
if printf '%s' "$wt_a_dirty" | grep -q 'b.txt' && printf '%s' "$wt_b_dirty" | grep -q 'a.txt'; then
    pass "reproduction: each worktree now holds the other's change (b.txt in wt-a, a.txt in wt-b)"
else
    fail "reproduction: crossing did not happen (wt-a: '$wt_a_dirty', wt-b: '$wt_b_dirty')"
fi

if out="$(run_check 2>&1)"; then
    fail "contamination: crossed worktrees passed (exit 0) — the check is blind to the crossing"
else
    if printf '%s' "$out" | grep -q 'CONTAMINATED: agent-a' && printf '%s' "$out" | grep -q 'b.txt'; then
        pass "contamination: agent-a's foreign file b.txt is reported"
    else
        fail "contamination: agent-a's foreign file b.txt was not named: $out"
    fi
    if printf '%s' "$out" | grep -q 'CONTAMINATED: agent-b' && printf '%s' "$out" | grep -q 'a.txt'; then
        pass "contamination: agent-b's foreign file a.txt is reported"
    else
        fail "contamination: agent-b's foreign file a.txt was not named: $out"
    fi
fi

echo
echo "== $PASSED passed, $FAILED failed =="
[ "$FAILED" -eq 0 ]
