#!/usr/bin/env bash
# Exercises the repo-owned git hooks (.githooks/pre-commit + .githooks/commit-msg)
# end to end, in a scratch clone of this repository, so the hooks run exactly as a
# fresh checkout would run them and nothing in the real tracker or any live worktree
# is touched.
#
# Scenario 1 — the reproduction this bead exists for, inverted: a bead created in the
# clone reaches the committed .beads/issues.jsonl on the next commit, and
# `git status --porcelain .beads` is empty afterwards. Without the pre-commit hook
# the commit succeeds while the export stays unstaged — the failure that let two
# beads never reach the committed export at all.
#
# Scenario 2 — a commit made from a linked worktree created by scripts/worktree.sh
# new, where .beads/redirect stands in for the database. br resolves every read and
# write through the redirect, so the flushed export lands in the main clone's
# tracked copy while the worktree's own copy stays stale; a hook that only flushes
# and stages would commit that stale copy and silently lose the new bead. The hook
# must copy the flushed export back into the worktree's tracked file first.
#
# Scenario 3 — the attribution guard survives the install: a commit message that
# credits an AI assistant is refused after core.hooksPath points at .githooks, and a
# clean message is accepted. A repo-local hooks path OVERRIDES the global one rather
# than adding to it, so without the tracked .githooks/commit-msg this install would
# silently disable the guard the global path provides today.
#
# Scenario 4 — stale export on disk while the database is ahead: exercises the
# hook's `br sync --flush-only`. An ordinary `br create` auto-flushes the JSONL
# export on every mutation, leaving the file on disk already current before the
# hook runs; in that happy path the flush is never exercised and deleting it leaves
# all tests green. Creating a bead with `br --no-auto-flush` advances the database
# without updating the export on disk (simulating an export failure such as /tmp
# quota exhaustion); the hook must flush the database to disk before staging so
# the new bead reaches the commit.
#
# WHY --allow-empty in the commits. git computes committability from the index it
# read BEFORE running the pre-commit hook and only re-reads the index on the success
# path, so a hook that stages the export cannot make a plain, previously-empty
# commit proceed — git refuses it as "no changes added". The reproduction this bead
# inverts commits with `git commit --allow-empty` ("commit anything at all"), which
# skips that refusal and then re-reads the hook's staging; `git commit -a` is the
# plain alternative. Scenario 1 asserts both forms carry the export.
#
# The clone bootstraps with `br init --no-db`: a fresh clone has no SQLite database
# (it is git-ignored and never travels), and JSONL-only mode is the one br supports
# for exactly that situation.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

PASSED=0
FAILED=0
pass() { PASSED=$((PASSED + 1)); echo "PASS: $*"; }
fail() { FAILED=$((FAILED + 1)); echo "FAIL: $*" >&2; }

command -v br >/dev/null 2>&1 || { echo "FAIL: br (beads_rust) not on PATH — cannot run this test" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq not on PATH — cannot run this test" >&2; exit 1; }

TMP_BASE="$(mktemp -d)"
CLONE="$TMP_BASE/clone"
WT_BASE="$TMP_BASE/worktrees"
WT_BRANCH="test/beads-hook-wt"
WT_DIR=""
CREATED_BEAD_IDS=()

cleanup() {
    # Remove the worktree and its branch from the clone, then the clone itself.
    if [ -n "$WT_DIR" ] && [ -d "$WT_DIR" ]; then
        git -C "$CLONE" worktree remove --force "$WT_DIR" >/dev/null 2>&1 || true
        git -C "$CLONE" branch -D "$WT_BRANCH" >/dev/null 2>&1 || true
    fi
    rm -rf "$TMP_BASE"
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# Setup: scratch clone, tracker bootstrapped in JSONL-only mode, hooks installed
# with the documented one-line command.
# ---------------------------------------------------------------------------
if ! git clone -q "$REPO_ROOT" "$CLONE"; then
    echo "FAIL: could not clone $REPO_ROOT — aborting" >&2
    exit 1
fi

if ! (cd "$CLONE" && br init --no-db >/dev/null 2>&1); then
    fail "setup: br init --no-db failed in the scratch clone"
    exit 1
fi
pass "setup: scratch clone bootstrapped with br init --no-db"

if ! (cd "$CLONE" && git config core.hooksPath .githooks); then
    fail "setup: could not set core.hooksPath (the documented one-line install)"
    exit 1
fi
pass "setup: core.hooksPath set with the documented one-line command"

create_bead() { # dir title -> id on stdout
    (cd "$1" && br create --title="$2" --type=chore --json 2>/dev/null | jq -r '.id // empty')
}

# ---------------------------------------------------------------------------
# Scenario 1: main-tree commit — a created bead reaches the committed export.
# ---------------------------------------------------------------------------
id="$(create_bead "$CLONE" "beads-hook test bead (auto-closed, throwaway)")"
if [ -z "$id" ] || [ "$id" = "null" ]; then
    fail "s1: could not create a probe bead in the clone"
else
    CREATED_BEAD_IDS+=("$id")
    if [ -n "$(git -C "$CLONE" status --porcelain .beads)" ]; then
        pass "s1: the new bead dirties the tracked export before the commit (the defect's precondition)"
    else
        fail "s1: export clean after br create — the tracker did not touch .beads/issues.jsonl"
    fi

    if (cd "$CLONE" && git commit --allow-empty -q -m "test: beads-hook scenario 1 probe commit"); then
        if [ -z "$(git -C "$CLONE" status --porcelain .beads)" ]; then
            pass "s1: .beads clean after the commit (pre-commit hook staged the export)"
        else
            fail "s1: .beads dirty after the commit — the export was not staged"
        fi
        if git -C "$CLONE" show HEAD:.beads/issues.jsonl | grep -q "$id"; then
            pass "s1: bead $id is in the committed issues.jsonl"
        else
            fail "s1: bead $id is NOT in the committed issues.jsonl"
        fi
    else
        fail "s1: commit failed — see git output above"
    fi

    # The plain form: a tracked change staged with -a (the export is the only dirty
    # tracked file) must also carry the export.
    id2="$(create_bead "$CLONE" "beads-hook test bead two (auto-closed, throwaway)")"
    if [ -z "$id2" ] || [ "$id2" = "null" ]; then
        fail "s1: could not create a second probe bead"
    else
        CREATED_BEAD_IDS+=("$id2")
        if (cd "$CLONE" && git commit -a -q -m "test: beads-hook scenario 1b probe commit"); then
            if git -C "$CLONE" show HEAD:.beads/issues.jsonl | grep -q "$id2"; then
                pass "s1: bead $id2 in the committed export via git commit -a"
            else
                fail "s1: bead $id2 missing from the export committed via git commit -a"
            fi
        else
            fail "s1: git commit -a failed — see git output above"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Scenario 2: commit from a linked worktree made by scripts/worktree.sh new.
# ---------------------------------------------------------------------------
wt_out="$(cd "$CLONE" && WORKTREE_BASE="$WT_BASE" bash "$REPO_ROOT/scripts/worktree.sh" new "$WT_BRANCH" 2>&1)"
WT_DIR="$(printf '%s\n' "$wt_out" | awk '/^  cd /{print $2; found=1} END{exit !found}')"
if [ -z "$WT_DIR" ] || [ ! -d "$WT_DIR" ]; then
    fail "s2: worktree.sh new did not report a usable worktree path"
else
    if [ -f "$WT_DIR/.beads/redirect" ]; then
        pass "s2: worktree has .beads/redirect"
    else
        fail "s2: worktree lacks .beads/redirect — the scenario cannot stand in for the redirect case"
    fi

    id="$(create_bead "$WT_DIR" "beads-hook worktree test (auto-closed, throwaway)")"
    if [ -z "$id" ] || [ "$id" = "null" ]; then
        fail "s2: could not create a probe bead from the worktree"
    else
        CREATED_BEAD_IDS+=("$id")
        if [ -z "$(git -C "$WT_DIR" status --porcelain .beads)" ]; then
            pass "s2: worktree's own export untouched before the commit (the redirect trap's precondition)"
        else
            fail "s2: worktree's tracked export changed before the commit — unexpected"
        fi

        if (cd "$WT_DIR" && git commit --allow-empty -q -m "test: beads-hook worktree probe commit"); then
            if [ -z "$(git -C "$WT_DIR" status --porcelain .beads)" ]; then
                pass "s2: worktree .beads clean after the commit"
            else
                fail "s2: worktree .beads dirty after the commit"
            fi
            if git -C "$WT_DIR" show HEAD:.beads/issues.jsonl | grep -q "$id"; then
                pass "s2: bead $id appears in the export committed from the worktree (copy-back worked)"
            else
                fail "s2: bead $id missing from the worktree commit — the hook staged the stale copy"
            fi
        else
            fail "s2: worktree commit failed — see git output above"
        fi
    fi
fi

# ---------------------------------------------------------------------------
# Scenario 3: the attribution guard still refuses a violating message after the
# repository-local hooks path replaced the global one.
# ---------------------------------------------------------------------------
if (cd "$CLONE" && git commit --allow-empty -q -m "test: Co-authored-by: Claude <claude@anthropic.com>"); then
    fail "s3: a commit crediting an AI assistant was ACCEPTED — the guard is gone"
else
    pass "s3: a commit crediting an AI assistant was refused"
fi

if (cd "$CLONE" && git commit --allow-empty -q -m "test: beads-hook clean message"); then
    pass "s3: a clean commit message is accepted"
else
    fail "s3: a clean commit message was refused — the guard misfires"
fi

# ---------------------------------------------------------------------------
# Scenario 4: stale export on disk while the database is ahead — exercises the
# hook's export flush (`br sync --flush-only`).
#
# WHY an ordinary br create cannot exercise this path: br auto-flushes the JSONL
# export on every mutation, so by the time the hook runs the file is already
# current on disk and only the staging loop is exercised. If all scenarios use
# the happy path, removing `br sync --flush-only` from .githooks/pre-commit
# leaves every test green.
#
# In production, an export can fail while the DB write succeeds (e.g. /tmp quota
# exhaustion during br create, or br invoked with --no-auto-flush). In that state
# the database holds the bead, the export on disk does not, and the hook's flush
# is the only thing between that and a commit whose tracker snapshot silently omits
# the bead. Creating a bead with `br --no-auto-flush` reproduces this precondition.
# ---------------------------------------------------------------------------
id4="$( (cd "$CLONE" && br --no-auto-flush create --title="beads-hook stale-export probe bead (auto-closed, throwaway)" --type=chore --json 2>/dev/null | jq -r '.id // empty') )"
if [ -z "$id4" ] || [ "$id4" = "null" ]; then
    fail "s4: could not create a probe bead with --no-auto-flush in the clone"
else
    CREATED_BEAD_IDS+=("$id4")
    if ! grep -q "$id4" "$CLONE/.beads/issues.jsonl"; then
        pass "s4: bead $id4 exists in database but NOT in issues.jsonl on disk (stale-export precondition)"
    else
        fail "s4: bead $id4 unexpectedly present in issues.jsonl before flush"
    fi

    if (cd "$CLONE" && git commit --allow-empty -q -m "test: beads-hook scenario 4 stale-export probe commit"); then
        if [ -z "$(git -C "$CLONE" status --porcelain .beads)" ]; then
            pass "s4: .beads clean after the commit (pre-commit hook flushed and staged the export)"
        else
            fail "s4: .beads dirty after the commit — the export was not staged"
        fi
        if git -C "$CLONE" show HEAD:.beads/issues.jsonl | grep -q "$id4"; then
            pass "s4: bead $id4 is in the committed issues.jsonl"
        else
            fail "s4: bead $id4 is NOT in the committed issues.jsonl"
        fi
    else
        fail "s4: commit failed — see git output above"
    fi
fi

# ---------------------------------------------------------------------------
# Summary
# ---------------------------------------------------------------------------
echo
echo "beads-hook tests: $PASSED passed, $FAILED failed"
[ "$FAILED" -eq 0 ]
