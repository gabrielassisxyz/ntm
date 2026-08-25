#!/usr/bin/env bash
# Manage isolated git worktrees — the way to do parallel agent work in this repo without
# clobbering another session.
#
# WHY: this repo is worked on by many parallel agent sessions, and nothing tells one session
# that another exists. Sharing the main tree means they clobber each other's branches and lose
# each other's uncommitted files. The rule (AGENTS.md) is structural: the main tree stays on the
# default branch as a clean reference, and EVERY session works in its own worktree. This wraps
# the git plumbing so that is one command, always branched off a FRESH origin/<default> — so
# nobody has to remember a `git pull`, and a stale local default can never leak into new work.
#
# It also solves the reason a raw `git worktree add` cannot be used here: `git worktree add`
# materializes only TRACKED files, so `.beads/beads.db` — the live SQLite tracker database — is
# absent from a fresh worktree (it is git-ignored, see .beads/.gitignore). `br` inside such a
# worktree silently falls back to the committed `.beads/issues.jsonl` snapshot: every read is
# stale, and every write is lost or, if committed, overwrites the tracker state of every issue in
# the repo. `br` itself already understands `.beads/redirect` — a file containing the path to
# another repo's `.beads/` directory — so this tool's whole job for the tracker is writing that
# one file; there is no wrapper logic to reimplement.
#
#   worktree new <type>/<desc>   fetch origin, create a worktree + branch off origin/<default>
#   worktree list                list worktrees (alias: ls)
#   worktree status [--strays]   per-worktree hygiene facts: dirty / unpushed / merged / behind N; --strays
#                                 also lists merged branches with no worktree, local and remote (alias: st)
#   worktree rm <branch|task>    remove a worktree (git refuses if it has unsaved work; -f to force)
#   worktree adopt <path>        write .beads/redirect into an existing worktree that lacks it
#
# Worktrees live OUTSIDE the repo, in ~/repositories/.worktrees/<repo>/<task>, so they never
# pollute ~/repositories. Override the base dir with $WORKTREE_BASE.
set -euo pipefail

# Resolve to the physical path immediately: agent sessions run under a shallow orch-home whose
# $HOME/repositories is a symlink to the real one. A derived path that keeps the symlink segment
# does not match the literal keys some tools store, which causes them to silently misbehave.
# $WORKTREE_BASE overrides skip this resolution on purpose — the caller already owns the string
# and knows what it means.
WORKTREE_BASE="${WORKTREE_BASE:-$(realpath -P "$HOME/repositories/.worktrees" 2>/dev/null || printf '%s' "$HOME/repositories/.worktrees")}"

# Repo identity — works whether called from the main tree or from inside a worktree.
# --git-common-dir points at the shared .../<repo>/.git even from a linked worktree.
common="$(realpath "$(git rev-parse --git-common-dir)")"
main_root="$(dirname "$common")"
repo="$(basename "$main_root")"
# `|| true`: under pipefail a missing origin/HEAD ref (a repo set up with `git init`
# + `remote add` instead of a clone never has it) would abort the whole script here,
# silently, before the fallback on the next line ever runs.
def="$(git symbolic-ref --short refs/remotes/origin/HEAD 2>/dev/null | sed 's|^origin/||' || true)"
if [ -z "$def" ]; then
    # `refs/remotes/origin/HEAD` is written by `clone` and by nothing else, so a repo built
    # with `git init` + `remote add` never has it. This used to fall straight to `master`,
    # which in a `main` repo printed a banner saying `master` and then reached for a ref
    # that does not exist — the failure lands after the banner has already said otherwise.
    # So ask what the remote actually carries before assuming anything.
    found=""; n=0
    for cand in main master; do
        if git rev-parse --verify --quiet "refs/remotes/origin/$cand" >/dev/null 2>&1; then
            found="$cand"; n=$(( n + 1 ))
        fi
    done
    if [ "$n" -eq 1 ]; then
        def="$found"                      # unambiguous: infer silently, that is the point
    elif [ "$n" -eq 0 ]; then
        # No remote branch to measure against at all (a remote never pushed to, or none).
        # HEAD is the only honest answer left, and it is still a guess, so say so.
        def="$(git symbolic-ref --short HEAD 2>/dev/null || echo master)"
        printf 'worktree: origin/HEAD is unset and origin carries neither main nor master.\n' >&2
        printf "worktree: assuming '%s', taken from the current checkout.\n" "$def" >&2
        printf 'worktree: fix it once with: git remote set-head origin -a\n' >&2
    else
        def=main
        printf 'worktree: origin/HEAD is unset and origin carries BOTH main and master.\n' >&2
        printf "worktree: assuming 'main' — this is a guess and may be the wrong one.\n" >&2
        printf 'worktree: fix it once with: git remote set-head origin -a\n' >&2
    fi
fi
# Spelled out rather than `origin/$def`, because that short form is a DWIM name and a
# tag literally named `origin/master` outranks the remote-tracking ref in git's
# resolution order. The ambiguity warning is swallowed by the `2>/dev/null` every
# merge-base call here carries, so an unmerged branch measured against such a tag
# comes back `merged` — silently, and in the dangerous direction of the two.
def_ref="refs/remotes/origin/$def"

slug() { printf '%s' "$1" | tr '/' '-'; }

usage() {
    cat >&2 <<EOF
worktree — isolated worktrees for $repo (default branch: $def)

  worktree new <type>/<desc>   fetch origin, create a worktree + branch off origin/$def
  worktree list                list worktrees (alias: ls)
  worktree status [--strays]   per-worktree hygiene: dirty / unpushed / merged / behind N; --strays also
                                lists merged branches with no worktree (alias: st)
  worktree rm <branch|task>    remove a worktree (-f to force past unsaved work)
  worktree adopt <path>        write .beads/redirect into a worktree that was created without it
  worktree prune-merged [--yes] delete merged branches with no worktree (lists them without --yes)

Worktrees live in $WORKTREE_BASE/$repo/<task>.
EOF
}

# Point a worktree's `br` at the main tree's live tracker database.
#
# WHY a redirect file and not a symlink to .beads/: `br` (beads_rust) already reads
# `.beads/redirect` itself — confirmed against the installed build, whose error strings name
# the exact file ("Redirect target not found", "Redirect target must be a .beads or _beads
# directory", "Redirect loop detected") — and `.beads/.gitignore` already documents the file for
# this exact purpose. So there is no wrapper logic to write: one text file containing the path
# to the main tree's `.beads/` directory is the whole mechanism, and `br` does the rest (all
# reads and writes in the worktree land on the SAME sqlite database the main tree uses). A
# symlink to `.beads/` itself was the alternative and was rejected: a live sqlite database
# opened concurrently from two directory entries that both claim to be its home is a sharper
# edge than a tracker reading one line out of a text file it was already designed to read.
#
# Absolute path, not relative: this is git-ignored and never leaves this machine (same
# reasoning as any other machine-local pointer file), so a machine path costs nothing and
# needs no re-deriving relative to wherever the worktree happens to sit.
write_beads_redirect() {
    local dir="$1" src="$main_root/.beads"
    [ -d "$src" ] || return 0
    [ -e "$dir/.beads/redirect" ] && { echo "==> .beads/redirect already present, left alone"; return 0; }
    mkdir -p "$dir/.beads"
    printf '%s' "$src" > "$dir/.beads/redirect"
    echo "==> wrote .beads/redirect -> $src"
}

# Confirm `br` actually honors the file just written, rather than trusting the write alone.
# Silent (not fatal) when `br` is not on PATH, or not installed as a tracker in this repo at
# all: neither is this tool's problem to solve.
verify_beads_redirect() {
    local dir="$1" out
    command -v br >/dev/null 2>&1 || return 0
    [ -e "$dir/.beads/redirect" ] || return 0
    if ! out="$(cd "$dir" && br where 2>&1)"; then
        echo "WARNING: br cannot open the tracker from $dir even with .beads/redirect in place:" >&2
        printf '%s\n' "$out" >&2
        return 0
    fi
    case "$out" in
        *"via redirect from"*) echo "==> br confirms it is reading the tracker via redirect" ;;
        *) echo "WARNING: br succeeded in $dir but did not report using the redirect — inspect manually:" >&2
           printf '%s\n' "$out" >&2 ;;
    esac
}

cmd_new() {
    local branch="${1:-}"
    [ -n "$branch" ] || { echo "usage: worktree new <type>/<desc>" >&2; exit 2; }
    local task dir
    task="$(slug "$branch")"
    dir="$WORKTREE_BASE/$repo/$task"
    [ -e "$dir" ] && { echo "worktree already exists: $dir" >&2; exit 1; }
    echo "==> fetching origin/$def (so the branch starts fresh) ..."
    git fetch --quiet origin "$def"
    mkdir -p "$WORKTREE_BASE/$repo"
    # --no-track: branch off a fresh origin/<default> WITHOUT adopting it as upstream. A new
    # feature branch has no remote of its own until `git push -u`; leaving upstream unset is
    # what lets `status` flag it "no-remote" (unbacked work) instead of comparing to master.
    git worktree add --no-track -b "$branch" "$dir" "origin/$def"
    write_beads_redirect "$dir"
    verify_beads_redirect "$dir"
    echo
    echo "worktree ready — enter it with:"
    echo "  cd $dir"
}

cmd_list() { git worktree list; }

# True if $1 matches one of the remaining args — used to skip a branch already
# printed as a worktree row when the branch-only scan below walks refs/heads.
contains() {
    local needle="$1" straw
    shift
    for straw in "$@"; do
        [ "$straw" = "$needle" ] && return 0
    done
    return 1
}

# State for a branch with no worktree, computed the same way a worktree row's state
# is: `unpushed`/`no-remote` against its upstream (or origin/$def with none), plus
# `merged`. There is no working tree to be `dirty`, and unlike a worktree row this
# must never fall through empty to the `clean` default below — a branch with nothing
# pointing at it reading as `clean` is exactly backwards, and the case that would
# fall through empty (unmerged, but already fully pushed to its own upstream) is
# reachable. refs/heads/$br, not the bare name, so a same-named tag can never be
# what merge-base or rev-list actually resolves.
no_worktree_state() {
    local br="$1" state="" ahead
    if git rev-parse --abbrev-ref --symbolic-full-name "$br@{u}" >/dev/null 2>&1; then
        ahead="$(git rev-list --count "$br@{u}..refs/heads/$br" 2>/dev/null || echo 0)"
        [ "$ahead" -gt 0 ] && state="$state unpushed"
    else
        ahead="$(git rev-list --count "$def_ref..refs/heads/$br" 2>/dev/null || echo 0)"
        [ "$ahead" -gt 0 ] && state="$state no-remote"
    fi
    git merge-base --is-ancestor "refs/heads/$br" "$def_ref" 2>/dev/null && state="$state merged"
    [ -z "$state" ] && state=" unmerged"
    printf '%s' "${state# }"
}

cmd_status() {
    local strays=""
    if [ $# -gt 0 ]; then
        # Anything but exactly `--strays` is a usage error, not a quiet no-op:
        # `--stray` (the singular typo) used to print the default table and exit 0,
        # which answers "are there strays?" with silence — indistinguishable from
        # "none".
        [ "$1" = "--strays" ] && [ $# -eq 1 ] || { echo "usage: worktree status [--strays]" >&2; exit 2; }
        strays=1
    fi

    echo "==> refreshing origin/$def ..." >&2
    # --prune: the branch scan below reads refs/remotes/origin/* directly, and the
    # single-branch fetch above never refreshed or pruned that namespace. Without a
    # prune, a branch already deleted on the origin still has a stale remote-tracking
    # ref here and gets reported as a leftover to delete. A prune only rewrites
    # remote-tracking refs, the same category of write as the fetch already here, not
    # a new exception to "status is read-only".
    git fetch --prune --quiet origin 2>/dev/null || true

    printf '%-34s %-26s %s\n' "BRANCH" "WORKTREE" "STATE"
    local wt ref br state ahead behind
    local -a wt_branches=()

    # `merged` — everything below — means "is an ancestor of origin/$def", so without
    # that ref the branch scan cannot tell a leftover from unbacked work and would
    # call every branch at-risk (a repo made with `git init` + `remote add`, same case
    # the origin/HEAD fallback above already has to allow for). Skip the whole scan
    # and say so, rather than print a table that looks complete and is not.
    local have_def_remote=1
    git rev-parse --verify --quiet "$def_ref" >/dev/null 2>&1 || have_def_remote=""
    [ -n "$have_def_remote" ] || echo "==> no refs/remotes/origin/$def — branch scan skipped, mergedness unknowable" >&2

    # The full set of branches merged on the origin, keyed by their real name via
    # `lstrip=3` rather than `:short` — `:short` abbreviates only as far as stays
    # unambiguous, so a branch that shares a name with a tag comes back as
    # `heads/<name>`/`origin/<name>` and matches nothing below, up to and including
    # the default branch itself. `lstrip=3` also makes origin/HEAD arrive as the
    # literal `HEAD` instead of shortening to the bare string `origin`, which is what
    # actually made the old `HEAD` guard fire — by accident, not by design.
    local -A remote_merged=()
    local -A remote_absorbed=()
    if [ -n "$have_def_remote" ]; then
        while IFS= read -r br; do
            [ "$br" = "HEAD" ] && continue
            [ "$br" = "$def" ] && continue
            git merge-base --is-ancestor "refs/remotes/origin/$br" "$def_ref" 2>/dev/null \
                && remote_merged["$br"]=1
        done < <(git for-each-ref --format='%(refname:lstrip=3)' --sort=refname refs/remotes/origin)
    fi

    while IFS=$'\t' read -r wt ref; do
        br="${ref#refs/heads/}"
        wt_branches+=("$br")
        state=""
        [ -n "$(git -C "$wt" status --porcelain 2>/dev/null)" ] && state="$state dirty"
        if git -C "$wt" rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1; then
            ahead="$(git -C "$wt" rev-list --count '@{u}..HEAD' 2>/dev/null || echo 0)"
            [ "$ahead" -gt 0 ] && state="$state unpushed"
        else
            ahead="$(git -C "$wt" rev-list --count "$def_ref..HEAD" 2>/dev/null || echo 0)"
            [ "$ahead" -gt 0 ] && state="$state no-remote"
        fi
        # refs/heads/$br, not the bare name: a same-named tag would otherwise be what
        # merge-base actually resolves, and a genuinely unmerged branch could print
        # `merged` because the tag it collides with happens to sit on origin/$def.
        git merge-base --is-ancestor "refs/heads/$br" "$def_ref" 2>/dev/null && state="$state merged"
        [ "$br" = "$def" ] && state="$state [main-ref]"
        # `new` branches off a FRESH origin/$def and then nothing ever mentions that the base
        # aged. Guarding against file collisions was never the whole job — this is the other
        # half: a worktree can be `merged` (no commits of its own yet) and still behind, and
        # that is exactly the moment when acting on a stale base is cheapest. On the
        # default-branch row it is the `git pull --ff-only` signal for the main tree.
        behind="$(git -C "$wt" rev-list --count "HEAD..$def_ref" 2>/dev/null || echo 0)"
        [ "$behind" -gt 0 ] && state="$state behind $behind"
        # A merged branch is not necessarily an orphan: `origin/<branch>` can still be
        # sitting there, and deleting it is a step nobody has taken yet. A second row
        # would violate "one branch, one row", so the fact is folded into this one
        # instead of being dropped — `merged +origin` is the line that says both
        # deletions (local worktree already gone would be one thing; here the origin
        # ref) are still pending.
        if [ -n "${remote_merged[$br]:-}" ]; then
            state="$state +origin"
            remote_absorbed["$br"]=1
        fi
        [ -z "$state" ] && state=" clean"
        printf '%-34s %-26s %s\n' "$br" "$(basename "$wt")" "${state# }"
    done < <(git worktree list --porcelain | awk '/^worktree /{wt=$2} /^branch /{print wt"\t"$2}')

    if [ -z "$have_def_remote" ]; then
        return 0
    fi

    # A local branch with no worktree is invisible above — the blind spot this
    # command exists to close. Unmerged means unbacked work with nothing pointing at
    # it, so it prints inline unconditionally; merged means a leftover with no
    # urgency, counted here and listed only under --strays. `lstrip=2` for the same
    # tag-collision reason as the remote scan above.
    local -a local_strays=()
    local st
    while IFS= read -r br; do
        [ "$br" = "$def" ] && continue
        contains "$br" "${wt_branches[@]}" && continue
        if git merge-base --is-ancestor "refs/heads/$br" "$def_ref" 2>/dev/null; then
            local_strays+=("$br")
        else
            st="$(no_worktree_state "$br")"
            if [ -n "${remote_merged[$br]:-}" ]; then
                st="$st +origin"
                remote_absorbed["$br"]=1
            fi
            printf '%-34s %-26s %s\n' "$br" "no worktree" "$st"
        fi
    done < <(git for-each-ref --format='%(refname:lstrip=2)' --sort=refname refs/heads)

    if [ -n "$strays" ]; then
        for br in "${local_strays[@]}"; do
            st="$(no_worktree_state "$br")"
            if [ -n "${remote_merged[$br]:-}" ]; then
                st="$st +origin"
                remote_absorbed["$br"]=1
            fi
            printf '%-34s %-26s %s\n' "$br" "no worktree" "$st"
        done
        # Whatever is left in remote_merged with no absorption has no local ref at
        # all — the everyday case is the remote branch of a PR nobody ever deletes
        # after merge. Sorted explicitly: associative-array key order is unspecified.
        # Guarded on non-empty: `printf '%s\n'` with zero args still emits one blank
        # line, so an empty remote_merged would otherwise feed a single empty string
        # into remote_sorted, and indexing an associative array with it is a fatal
        # bash error — in a tidy repo, exactly the case this command must not crash on.
        if [ ${#remote_merged[@]} -gt 0 ]; then
            local -a remote_sorted=()
            mapfile -t remote_sorted < <(printf '%s\n' "${!remote_merged[@]}" | sort)
            for br in "${remote_sorted[@]}"; do
                [ -n "${remote_absorbed[$br]:-}" ] && continue
                printf '%-34s %-26s %s\n' "origin/$br" "no worktree" "merged"
            done
        fi
    fi

    # The remote count is a statement about the origin, so it is the full size of
    # remote_merged — every branch merged there, deduplicated rows included — not
    # the number of standalone rows `--strays` prints. Silent when both counts are
    # zero: a repo with nothing to report prints exactly what it always has.
    if [ ${#local_strays[@]} -gt 0 ] || [ ${#remote_merged[@]} -gt 0 ]; then
        printf "  %d merged local branches with no worktree, %d merged on origin — \`worktree status --strays\` to list\n" \
            "${#local_strays[@]}" "${#remote_merged[@]}"
    fi
}

# Bring an existing worktree up to the shape `new` creates. A worktree made with a plain
# `git worktree add` materializes only tracked files, so `.beads/redirect` is absent and `br`
# inside it falls back to the stale committed JSONL — silently, until a `br doctor` nobody runs
# reports it. This writes the missing file in place, reusing write_beads_redirect() so `new` and
# `adopt` can never drift apart, and refuses a path that is not a worktree of this repo.
cmd_adopt() {
    local dir="${1:-}"
    [ -n "$dir" ] || { echo "usage: worktree adopt <path>" >&2; exit 2; }
    dir="$(realpath -m -- "$dir")"
    [ -d "$dir" ] || { echo "worktree: not a directory: $dir" >&2; exit 1; }

    # Refuse anything that is not a registered worktree of THIS repo before writing a single
    # file: pointing an arbitrary directory's tracker at this repo's database is exactly the
    # damage a wrong path would do. `git worktree list` is the record — `git rev-parse
    # --git-common-dir` alone cannot tell a worktree from a subdirectory of one, because it
    # walks up to the same common dir.
    local wt
    while IFS= read -r wt; do
        [ "$(realpath -m -- "$wt")" = "$dir" ] && { write_beads_redirect "$dir"; verify_beads_redirect "$dir"; return 0; }
    done < <(git worktree list --porcelain | sed -n 's|^worktree ||p')

    # Name the actual problem rather than just "no": a path with no git dir at all,
    # a path in another repo, and a subdirectory of a worktree are three different
    # mistakes and three different fixes.
    local wt_common
    wt_common="$(git -C "$dir" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)"
    if [ -z "$wt_common" ]; then
        echo "worktree: $dir is not a git worktree of $repo (no .git directory)" >&2
    elif [ "$(realpath -m -- "$wt_common")" != "$common" ]; then
        echo "worktree: $dir is not a worktree of $repo (its git dir is $wt_common, not $common)" >&2
    else
        echo "worktree: $dir is inside $repo but is not a worktree root (run: git worktree list)" >&2
    fi
    exit 1
}

# The path a worktree still holds a branch at, and whether git already calls that
# registration prunable — or both empty if no registration holds it at all. The
# filesystem is not the record: `rm -rf` on a worktree directory does not deregister
# it, git keeps a `prunable` entry, and `git branch -d/-D` still refuses with "cannot
# delete branch 'x' used by worktree at <path>" regardless of merge status. The same
# is true, minus the `prunable` marker, for a branch simply checked out in a worktree
# that lives outside $WORKTREE_BASE, where the derived $dir was never going to exist.
worktree_holding() {
    local want="refs/heads/$1" wt br pr
    while IFS=$'\t' read -r wt br pr; do
        [ "$br" = "$want" ] || continue
        printf '%s\t%s' "$wt" "$pr"
        return 0
    done < <(git worktree list --porcelain | awk '
        /^worktree /{ if (wt != "") print wt "\t" br "\t" pr; wt = substr($0, 10); br = ""; pr = "" }
        /^branch /{ br = $2 }
        /^prunable/{ pr = "1" }
        END{ if (wt != "") print wt "\t" br "\t" pr }
    ')
    return 1
}

# How to get rid of a branch this command deliberately leaves alone, printed on stderr next
# to the diagnosis that raised it.
#
# WHY not `git branch -D` here: `git branch -d` REFUSES to delete a branch that is not merged
# into HEAD's upstream, which is the safety net that makes it acceptable for a script to run
# unattended at all — even if the merge-base scan above were ever wrong, the worst outcome is a
# refusal, never a lost commit. `-D` throws that guarantee away, so it is deliberately not
# offered here regardless of merge state.
#
# %q on the branch name: it may legally contain shell metacharacters (`chore/foo;uname`
# passes `git check-ref-format`), and this line is a command someone will paste. Quoting is
# what makes pasting it delete the branch and run nothing else.
# $1 non-empty means merged.
# Prints on stdout; the diagnostic callers redirect it, so the same wording serves both a
# failure on stderr and the success report at the end of `rm`.
delete_hint() {
    if [ -n "$1" ]; then
        printf '  delete it with: scripts/worktree.sh prune-merged --yes  (every merged branch with no worktree, this one included)\n'
    else
        printf '  unmerged, so nothing here will delete it: git branch -D %q is the only route, and it needs human approval\n' "$2"
    fi
}

cmd_rm() {
    local key="" force=""
    local a
    for a in "$@"; do
        case "$a" in
            -f|--force) force=1 ;;
            -h|--help)
                cat >&2 <<EOF
usage: worktree rm <branch|task> [-f]

Removes a worktree (git refuses if it has unsaved work; -f to force past it).
EOF
                exit 0
                ;;
            *) key="$a" ;;
        esac
    done
    [ -n "$key" ] || { echo "usage: worktree rm <branch|task> [-f]" >&2; exit 2; }
    local task dir
    task="$(slug "$key")"
    dir="$WORKTREE_BASE/$repo/$task"
    # "no worktree at $dir" was always true and always a dead end: the branch a
    # session actually cares about (a stray worktree got removed by hand, a handoff
    # named a branch whose worktree was already gone) still exists and this told
    # nobody. Report it instead of just the absence, but delete nothing — this
    # command's job is removing worktrees, not branches.
    if [ ! -d "$dir" ]; then
        if git show-ref --verify --quiet "refs/heads/$key"; then
            local holder="" holder_pr=""
            # `|| true` on both: no registration holding the branch means the
            # process substitution prints nothing, and `read` hitting EOF with no
            # line to read exits nonzero — under `set -e` that would abort the whole
            # command instead of leaving holder/holder_pr empty, which is a valid,
            # common outcome here, not an error.
            IFS=$'\t' read -r holder holder_pr < <(worktree_holding "$key" || true) || true

            local mergedness="not merged into $def" merged=""
            # refs/heads/$key, not the bare $key: a same-named tag would otherwise be
            # what merge-base actually resolves, and the branch would silently be
            # reported as unmerged regardless of its real state.
            git merge-base --is-ancestor "refs/heads/$key" "$def_ref" 2>/dev/null \
                && { mergedness="merged into $def"; merged=1; }

            if [ -n "$holder" ]; then
                # A suggestion that cannot work is worse than the dead end it
                # replaced — the dead end at least did not send anyone down a wrong
                # path. `git branch -D` fails here no matter what this says, so say
                # what actually has to happen first. Mergedness is stated in every
                # shape: it is the fact that decides whether any of this is worth
                # doing, and it does not depend on who holds the branch.
                if [ -n "$holder_pr" ]; then
                    echo "no worktree at $dir — but a stale (prunable) registration still holds branch '$key' ($mergedness) at $holder" >&2
                    echo "  first: git worktree prune" >&2
                elif [ "$holder" = "$main_root" ] || [ "$(realpath "$holder" 2>/dev/null || printf '%s' "$holder")" = "$main_root" ]; then
                    # The main tree cannot be removed at all — git answers `fatal:
                    # '<path>' is a main working tree` — so suggesting it would be
                    # the same dead end in a new costume. Checking it out elsewhere
                    # is what actually frees the branch.
                    echo "no worktree at $dir — but branch '$key' ($mergedness) is checked out in the MAIN tree: $holder" >&2
                    echo "  first: check out another branch there — git refuses to remove a main working tree" >&2
                else
                    echo "no worktree at $dir — but branch '$key' ($mergedness) is checked out in another worktree: $holder" >&2
                    printf '  first: git worktree remove %q\n' "$holder" >&2
                fi
                delete_hint "$merged" "$key" >&2
                exit 1
            fi

            echo "no worktree at $dir — but local branch '$key' exists, $mergedness" >&2
            delete_hint "$merged" "$key" >&2
            exit 1
        fi
        echo "no worktree at $dir" >&2
        exit 1
    fi

    # Everything tracked lives in the object store and survives this. Git-ignored files do NOT:
    # `git worktree remove` checks for dirty and untracked content but ignores ignored paths
    # entirely, so it deletes them without a word, with or without -f. `.beads/*` is excluded on
    # purpose — `.beads/redirect` and the lock/db files next to it are bookkeeping this tool (or
    # a fresh `br init`) recreates for free, and the tracker state they point at lives in the
    # main tree regardless; nothing under `.beads/` is ever unique to a worktree copy. Anything
    # ELSE git-ignored and not `.beads/*` is a real, irreplaceable file this worktree alone holds.
    local at_risk=()
    while IFS= read -r rel; do
        [ -n "$rel" ] || continue
        case "$rel" in
            .beads/*) continue ;;
        esac
        at_risk+=("$rel")
    done < <(git -C "$dir" ls-files --others --ignored --exclude-standard --directory 2>/dev/null)

    # Read before removal either way, because afterwards the worktree is gone and the branch
    # name is not recoverable from the task name — slug() maps `/` to `-` and does not invert.
    local held=""
    held="$(git -C "$dir" symbolic-ref --quiet --short HEAD 2>/dev/null || true)"

    # Commits that exist ONLY on this worktree's branch, pushed nowhere: `git worktree remove`
    # happily deletes a CLEAN worktree even when its branch is ahead of its upstream (or of
    # $def_ref with no upstream at all) — the branch ref keeps the commits alive for the moment,
    # but the working tree that made them visible and easy to push is gone, and a later branch
    # deletion (by hand, or by `prune-merged` once it merges) would take the only copy with it.
    # So this is refused up front alongside the other two categories, not left to a note after
    # the fact.
    local ahead_unpushed=0 has_upstream=""
    if [ -n "$held" ]; then
        if git -C "$dir" rev-parse --abbrev-ref --symbolic-full-name '@{u}' >/dev/null 2>&1; then
            has_upstream=1
            ahead_unpushed="$(git -C "$dir" rev-list --count '@{u}..HEAD' 2>/dev/null || echo 0)"
        else
            ahead_unpushed="$(git -C "$dir" rev-list --count "$def_ref..HEAD" 2>/dev/null || echo 0)"
        fi
    fi

    local refuse=""
    if [ ${#at_risk[@]} -gt 0 ]; then
        echo "WARNING: these git-ignored paths exist only in this worktree and will be destroyed:" >&2
        printf '  %s\n' "${at_risk[@]}" >&2
        echo >&2
        echo "Nothing tracked is at risk — only these. Move what matters out before removing." >&2
        refuse=1
    fi
    if [ "$ahead_unpushed" -gt 0 ]; then
        if [ -n "$has_upstream" ]; then
            echo "WARNING: branch '$held' has $ahead_unpushed commit(s) not pushed to its upstream." >&2
        else
            echo "WARNING: branch '$held' has $ahead_unpushed commit(s) not on $def_ref and no upstream at all." >&2
        fi
        refuse=1
    fi
    if [ -n "$refuse" ] && [ -z "$force" ]; then
        echo "Refusing to remove. Re-run with -f once you have looked." >&2
        exit 1
    fi

    if [ -n "$force" ]; then
        git worktree remove --force "$dir"
    else
        git worktree remove "$dir"
    fi
    # The directory this process may be standing in has just been unlinked, and
    # `cd <worktree> && scripts/worktree.sh rm <task>` is the natural end-of-task shape, so it
    # usually IS standing in it. git refuses to do anything at all from a cwd that no longer
    # exists — `fatal: Unable to read current working directory`, exit 128, before a single ref
    # is read — and the mergedness probe below swallows stderr and reads any nonzero exit as
    # "not merged". Land in the main tree instead: it is the same repository, it is guaranteed
    # to exist, and it makes every command after this point immune to that trap.
    cd "$main_root"
    echo "removed $dir"

    # Removing the worktree leaves the branch, on purpose (the comment on prune-merged says
    # why the two are separate). What was missing is anyone SAYING so — say it here, where the
    # session that created the branch is still around, not in an unrelated gate later.
    [ -n "$held" ] || return 0
    if git merge-base --is-ancestor "refs/heads/$held" "$def_ref" 2>/dev/null; then
        echo "branch '$held' is still local and merged into $def — delete it or it lingers"
        delete_hint 1 "$held"
    else
        echo "branch '$held' is still local and not merged into $def — kept, it is the only thing left holding its commits"
    fi
}

# Deleting the branches `status --strays` merely counted.
#
# `git branch -d` is the safety net and the reason this is allowed to be scripted at all: it
# REFUSES to delete a branch that is not merged into HEAD's upstream. So even if the merge-base
# scan below were wrong, the worst outcome is a refusal, never a lost commit. `-D` is
# deliberately not offered.
cmd_prune_merged() {
    local yes="" check=""
    while [ $# -gt 0 ]; do
        case "$1" in
            --yes|-y)  yes=1; shift ;;
            --check)   check=1; shift ;;
            *) echo "usage: worktree prune-merged [--yes|--check]" >&2; exit 2 ;;
        esac
    done
    [ -n "$yes" ] && [ -n "$check" ] && { echo "worktree: --yes and --check are exclusive" >&2; exit 2; }

    # --check is meant as a CI gate entry point, so it must not touch the network: a gate that
    # fetches is a gate that fails offline.
    if [ -z "$check" ]; then
        echo "==> refreshing $def_ref ..."
        git fetch -q origin 2>/dev/null || true
    fi
    git rev-parse --verify --quiet "$def_ref" >/dev/null 2>&1 || {
        echo "worktree: $def_ref does not exist — refusing to judge what is merged" >&2
        exit 1
    }

    local -a wt_branches=()
    while IFS= read -r b; do [ -n "$b" ] && wt_branches+=("$b"); done < <(
        git worktree list --porcelain | sed -n 's|^branch refs/heads/||p')

    local -a doomed=()
    local br
    while IFS= read -r br; do
        [ "$br" = "$def" ] && continue
        contains "$br" "${wt_branches[@]}" && continue   # a live worktree owns it
        git merge-base --is-ancestor "refs/heads/$br" "$def_ref" 2>/dev/null && doomed+=("$br")
    done < <(git for-each-ref --format='%(refname:lstrip=2)' --sort=refname refs/heads)

    if [ ${#doomed[@]} -eq 0 ]; then
        [ -n "$check" ] || echo "no merged branches without a worktree — nothing to prune"
        return 0
    fi

    if [ -n "$check" ]; then
        printf '%d merged branch(es) with no worktree were never deleted:\n' "${#doomed[@]}" >&2
        printf '%s\n' "${doomed[@]}" | sed 's/^/  /' >&2
        printf 'clear them with: scripts/worktree.sh prune-merged --yes\n' >&2
        return 1
    fi

    printf '%s\n' "${doomed[@]}" | sed 's/^/  /'
    if [ -z "$yes" ]; then
        printf '\n%d merged branch(es) would be deleted. Re-run with --yes to do it.\n' "${#doomed[@]}"
        return 0
    fi

    local failed=0
    for br in "${doomed[@]}"; do
        git branch -d "$br" >/dev/null 2>&1 || { echo "refused: $br" >&2; failed=$(( failed + 1 )); }
    done
    printf 'deleted %d, refused %d\n' "$(( ${#doomed[@]} - failed ))" "$failed"
    [ "$failed" -eq 0 ]
}

case "${1:-}" in
    new)             shift; cmd_new "$@" ;;
    list|ls)         cmd_list ;;
    status|st)       shift; cmd_status "$@" ;;
    rm|remove)       shift; cmd_rm "$@" ;;
    adopt)           shift; cmd_adopt "$@" ;;
    prune-merged)    shift; cmd_prune_merged "$@" ;;
    ""|-h|--help)    usage ;;
    *)               echo "unknown subcommand: $1" >&2; usage; exit 2 ;;
esac
