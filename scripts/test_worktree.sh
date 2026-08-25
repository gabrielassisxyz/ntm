#!/usr/bin/env bash
# Exercises scripts/worktree.sh: the slug() helper in isolation, then the two things the whole
# tool exists for — a fresh worktree and a repaired raw one both reaching the live `br` tracker
# — and the three `rm` refusals, each driven red on purpose so a refusal that silently stopped
# refusing would be caught here rather than trusted on the strength of the code reading right.
#
# Every worktree and branch here is created by this script and removed by it before exit; none
# of it ever runs `git worktree remove`, `git branch -d/-D` or `git worktree prune` against a
# path or branch it did not create itself. Bead state: one throwaway bead is created for the
# tracker-visibility checks and closed again at the end — nothing pre-existing is touched.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKTREE_SH="$REPO_ROOT/scripts/worktree.sh"

# Same derivation scripts/worktree.sh itself uses, so this always targets the real main tree
# and the real live database regardless of which worktree this test happens to run from.
common_dir="$(git -C "$REPO_ROOT" rev-parse --git-common-dir)"
main_root="$(cd "$(dirname "$common_dir")" && pwd)"

PASSED=0
FAILED=0
pass() { PASSED=$((PASSED + 1)); echo "PASS: $*"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL: $*" >&2; }

# ---------------------------------------------------------------------------
# Unit: slug(). Extracted verbatim from the script under test with sed rather than
# reimplemented here, so a future change to slug() cannot silently drift out of sync with
# what this asserts — a copy would test the copy, not the tool.
# ---------------------------------------------------------------------------
eval "$(sed -n '/^slug() {/,/^}/p' "$WORKTREE_SH")"

check_slug() {
    local input="$1" want="$2" got
    got="$(slug "$input")"
    if [ "$got" = "$want" ]; then pass "slug '$input' -> '$got'"; else fail "slug '$input' -> '$got' (want '$want')"; fi
}
check_slug "feature/x" "feature-x"
check_slug "agent/bd-123" "agent-bd-123"
check_slug "a/b/c" "a-b-c"

# ---------------------------------------------------------------------------
# Integration setup: a scratch WORKTREE_BASE so `new` never touches the shared
# ~/repositories/.worktrees/<repo> directory other sessions are using right now.
# ---------------------------------------------------------------------------
TMP_BASE="$(mktemp -d)"
CREATED_BRANCHES=()
CREATED_PATHS=()
BEAD_ID=""

cleanup() {
    local b p
    for p in "${CREATED_PATHS[@]}"; do
        [ -d "$p" ] && git -C "$main_root" worktree remove --force "$p" >/dev/null 2>&1 || true
    done
    for b in "${CREATED_BRANCHES[@]}"; do
        git -C "$main_root" branch -D "$b" >/dev/null 2>&1 || true
    done
    # Explicit cd: this trap can fire while the shell's cwd is a worktree that has no
    # `.beads/redirect` of its own (this repo's own dev worktrees are exactly that until this
    # bead lands), and `br` with no redirect falls back to the stale committed JSONL — the
    # close would silently fail there and the `|| true` below would hide it.
    [ -n "$BEAD_ID" ] && (cd "$main_root" && br close "$BEAD_ID" --reason "worktree.sh test run complete" >/dev/null 2>&1) || true
    rm -rf "$TMP_BASE"
}
trap cleanup EXIT

run_tool() { WORKTREE_BASE="$TMP_BASE/worktrees" bash "$WORKTREE_SH" "$@"; }

# Path printed by `new`'s own "cd <dir>" line — parsed rather than recomputed, so this test
# stays a black-box check of what the tool actually reports, not a second copy of its layout
# logic that could drift from the real one.
#
# Does NOT register the branch in CREATED_BRANCHES itself: every call site captures this in a
# command substitution (`dir="$(new_worktree ...)"`), which bash runs in a SUBSHELL — an array
# append made in there is invisible to the parent shell the moment the subshell exits. Cleanup
# would silently stop deleting branches with no error at all. Callers register the branch
# themselves, in the shell that actually runs cleanup.
new_worktree() {
    local branch="$1" out
    out="$(run_tool new "$branch")"
    printf '%s\n' "$out" | awk '/^  cd /{print $2; found=1} END{exit !found}'
}

# ---------------------------------------------------------------------------
# Integration: tracker visibility. This is the criterion the whole bead exists for.
# ---------------------------------------------------------------------------
BEAD_ID="$(cd "$main_root" && br create --title="worktree.sh integration test (throwaway, auto-closed)" --type=task --priority=3 --json | jq -r '.id')"
if [ -z "$BEAD_ID" ] || [ "$BEAD_ID" = "null" ]; then
    fail "tracker-visibility: could not create a throwaway bead to test with"
else
    wt_dir="$(new_worktree "test/wt-tool-tracker-visibility")"
    CREATED_BRANCHES+=("test/wt-tool-tracker-visibility")
    if [ -z "$wt_dir" ] || [ ! -d "$wt_dir" ]; then
        fail "tracker-visibility: 'new' did not report a usable worktree path"
    else
        CREATED_PATHS+=("$wt_dir")
        (cd "$main_root" && br update "$BEAD_ID" --status in_progress --json >/dev/null)
        seen="$(cd "$wt_dir" && br show "$BEAD_ID" --json 2>/dev/null | jq -r '.[0].status // empty')"
        if [ "$seen" = "in_progress" ]; then
            pass "tracker-visibility: status changed in the main tree is visible from a worktree made by 'new' ($seen)"
        else
            fail "tracker-visibility: worktree read '$seen', main tree was updated to in_progress"
        fi

        # The .beads/redirect this wrote must never be treated as an at-risk file of its own —
        # it is bookkeeping the tool recreates for free, not a worktree-only artifact.
        if git -C "$wt_dir" check-ignore -q .beads/redirect; then
            pass "tracker-visibility: .beads/redirect is git-ignored (never stageable by git add -A)"
        else
            fail "tracker-visibility: .beads/redirect is NOT git-ignored — a git add -A would commit it"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Integration: adopt repairs a worktree made the wrong way (raw `git worktree add`).
# ---------------------------------------------------------------------------
raw_dir="$TMP_BASE/raw-adopt-target"
raw_branch="test/wt-tool-raw-adopt"
if git -C "$main_root" worktree add --no-track -b "$raw_branch" "$raw_dir" HEAD >/dev/null 2>&1; then
    CREATED_BRANCHES+=("$raw_branch")
    CREATED_PATHS+=("$raw_dir")

    if [ -n "$BEAD_ID" ] && (cd "$raw_dir" && br show "$BEAD_ID" >/dev/null 2>&1); then
        fail "adopt: a raw worktree unexpectedly reached the live tracker before adopt ran"
    else
        pass "adopt: a raw worktree cannot read the tracker before adopt runs (the defect this bead fixes)"
    fi

    run_tool adopt "$raw_dir" >/dev/null
    if [ -n "$BEAD_ID" ]; then
        seen="$(cd "$raw_dir" && br show "$BEAD_ID" --json 2>/dev/null | jq -r '.[0].status // empty')"
        if [ "$seen" = "in_progress" ]; then
            pass "adopt: the same worktree reaches the live tracker after adopt runs ($seen)"
        else
            fail "adopt: worktree read '$seen' after adopt, expected in_progress"
        fi
    fi
else
    fail "adopt: could not create a raw worktree to adopt"
fi

# ---------------------------------------------------------------------------
# Integration: each `rm` refusal, driven red on purpose.
# ---------------------------------------------------------------------------
rm_branch="test/wt-tool-rm-refusals"
rm_dir="$(new_worktree "$rm_branch")"
CREATED_BRANCHES+=("$rm_branch")
if [ -z "$rm_dir" ] || [ ! -d "$rm_dir" ]; then
    fail "rm-refusals: 'new' did not report a usable worktree path"
else
    CREATED_PATHS+=("$rm_dir")

    # 1) dirty: an uncommitted change to a tracked file.
    echo "test-dirty-$$" >> "$rm_dir/README.md"
    if run_tool rm "$rm_branch" >"$TMP_BASE/out" 2>&1; then
        fail "rm-refusals: dirty worktree was removed instead of refused"
    else
        if grep -qi "modified or untracked" "$TMP_BASE/out"; then
            pass "rm-refusals: dirty worktree is refused"
        else
            fail "rm-refusals: dirty worktree refused, but not with the expected git message"
        fi
    fi
    rm -f "$TMP_BASE/out"
    git -C "$rm_dir" add README.md
    git -C "$rm_dir" -c user.email=test@example.com -c user.name=test commit -q -m "test: fold dirty change into an unpushed commit"

    # 2) unpushed commit: the branch is now ahead of origin/<default> with no upstream at all.
    if run_tool rm "$rm_branch" >"$TMP_BASE/out" 2>&1; then
        fail "rm-refusals: worktree with an unpushed commit was removed instead of refused"
    else
        if grep -qi "not pushed\|no upstream at all" "$TMP_BASE/out"; then
            pass "rm-refusals: unpushed commit is refused"
        else
            fail "rm-refusals: refused, but not with the expected unpushed-commit message"
        fi
    fi
    rm -f "$TMP_BASE/out"

    # 3) an ignored file that exists only in this worktree (not under .beads/, which is exempt).
    echo "scratch" > "$rm_dir/storage.sqlite3"
    if run_tool rm "$rm_branch" >"$TMP_BASE/out" 2>&1; then
        fail "rm-refusals: worktree with an ignored-only file was removed instead of refused"
    else
        if grep -q "storage.sqlite3" "$TMP_BASE/out"; then
            pass "rm-refusals: ignored-only file is refused"
        else
            fail "rm-refusals: refused, but the ignored file was not named in the warning"
        fi
        if grep -q "^  \.beads/" "$TMP_BASE/out"; then
            fail "rm-refusals: .beads/* was listed as at-risk (should be exempt)"
        fi
    fi
    rm -f "$TMP_BASE/out"

    # 4) -f overrides every refusal above.
    if run_tool rm "$rm_branch" -f >"$TMP_BASE/out" 2>&1; then
        pass "rm-refusals: -f removes past all three refusals"
    else
        fail "rm-refusals: -f still refused to remove"
    fi
    rm -f "$TMP_BASE/out"
fi

echo
echo "== $PASSED passed, $FAILED failed =="
[ "$FAILED" -eq 0 ]
