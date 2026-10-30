# Operate an existing BL loop

**Loop development and new launches remain frozen since 2026-10-30.** The human
reactivated I140 for this recovery guide only. Use the [item loop](item-loop/index.md)
for new work. This document authorizes no live pilot or restart. `bl loop` runs
the standard lane when separately authorized.

## Inspect without launching work

In each terminal, enter the repository and set the command paths:

```sh
BL="$PWD/scripts/bl"
DISPATCH=${BL_DISPATCH:-dispatch}
DISPATCH_STATE=${DISPATCH_HOME:-$HOME/.local/state/dispatch}
"$BL" check --views
"$BL" status
git worktree list
```

These examples use POSIX shell and Python 3, with no zsh features. Use the same
`BL_DISPATCH`, `DISPATCH_HOME` and dispatch configuration as the original launch.
A successful `check` proves ledger/view consistency, not that workers are stopped.
Inspect each worktree's `git status --short` separately when reconciling edits.

Read only the current association and the tail of its brain log:

```sh
if [ -f .backlog/loop.json ]; then
  python3 -m json.tool .backlog/loop.json
  if JOB=$(python3 -c 'import json; a=json.load(open(".backlog/loop.json")); j=a.get("job"); assert j, "association has no job; reconcile before proceeding"; print(j)'); then
    "$DISPATCH" status "$JOB"
    tail -n 30 "$DISPATCH_STATE/jobs/$JOB/log"
  fi
else
  printf '%s\n' 'No loop association; inspect item state and known jobs.'
fi
"$DISPATCH" status
```

If `loop.json` is absent, there is no association to resume. If its job is missing
or empty, stop here and reconcile; do not launch a replacement brain. Read
`bl show ITEM --json` for that item's current stage, not the whole ledger.
`dispatch status` without an ID lists jobs for the current cwd; run it from each
worktree too. A terminal brain does not prove its children ended.

Association fields identify the item, job, ownership token, launch/session count
and recorded session directories (`dirs`). Those directories contain worker
receipts at `workers/*.json`, prompts and output files. Receipts plus dispatch
metadata titles `bl-worker-TOKEN-...` identify managed workers even when their cwd
is outside the repository. Inspect their exact job IDs with `dispatch status ID`.
A `lost` job is unconfirmed, not proof that its processes stopped.

## Cancel and preserve work

Save the current association and job IDs outside `.backlog` if you need a record
of the original job after release. Then, from the repository:

```sh
"$BL" loop --cancel --reason "operator stopped the run for investigation"
"$BL" status
"$BL" check --views
"$DISPATCH" status
git status --short
git worktree list
```

Cancellation requests a live owner to yield its lock (up to 30 seconds). Once it
owns the lock it stops the brain, fences new worker launches, drains jobs and
parks unfinished work. **This cancels dispatch jobs across the repository and its
worktrees, including jobs from other sessions.** Coordinate with their operators
before invoking it. `--timeout 120` is the default cleanup wait setting; dispatch
calls have separate bounds, so it is not a strict whole-command deadline.

Recovery artifacts are under `.backlog/cancelled/`: binary patches for source
changes and full archives of owned worktrees, including ignored files. Detached
commit histories are retained under `refs/bl-cancel/ITEM/COMMIT`. Only worktrees
inside recorded session directories are eligible for removal. Root restoration
preserves unrelated edits and refuses to reverse staged or committed scoped work.
Review patches with `git apply --stat PATH` and archive contents with
`tar -tzf PATH` before restoring them into an isolated recovery workspace.

Read every `filed ITEM-left-N` message. Exit 0 and a released association can
coexist with unresolved jobs or retained files recorded in a follow-up item.
Inspect those items and saved job IDs before further work. Failure to record a
disposition retains the association. Never remove `loop.json`, lock files or
frozen evidence by hand to bypass recovery; do not reset attempt counters.
If lock handoff times out, the request remains: inspect the owner and retry
cancellation after it yields. Staged/committed edits and lost workers require
attended reconciliation, not a force reset or process-name kill.

Managed workers must use `scripts/dispatch.sh` from their assigned workspace.
Direct runtimes without dispatch metadata are outside reliable discovery. While
the monitor is alive, terminal brains and wait timeouts trigger managed-worker
cleanup. Killing the monitor itself installs no watchdog; recover with the CLI.
Cancellation does not unfreeze new loop launches.

## Interpret exit codes

Read stderr as well as the code: global CLI errors overlap loop-specific codes.

| code | meaning | action |
|---|---|---|
| 0 | backlog complete (including a parked-only backlog), launch budget exhausted, or cancellation completed | inspect item disposition and follow-ups; budget exhaustion or a parked count is not a closure |
| 1 | ordinary I/O/process error (or a failed command) | preserve files; inspect the specific error, association and jobs before retrying |
| 2 | refused arguments, lock contention, or cancel with no association | fix arguments; for contention inspect the owner; with no association inspect known jobs/item state rather than deleting files |
| 3 | loop: open/blocked items remain but none is eligible; global CLI: corrupt ledger | distinguish `bl loop:` from `bl: corrupt ledger:`; inspect dependencies, including parked blockers, for the former; preserve and reconcile ledger integrity for the latter |
| 4 | loop consistency check failed | run `bl check --views`; resolve named evidence/view problems without rewriting frozen evidence |
| 5 | stopped job, timeout, context/session cap, stale association, surviving writer or incomplete recovery | inspect exact item/job/stage; use cancellation for deliberate abandonment; resume only after reconciliation and separate human authorization |

With a valid saved association, `bl loop` attaches to its saved job **and can then
advance the queue**, using a default budget of three launches. Bare `bl loop` is
not an observation command. A terminal failed/lost/cancelled job normally returns
exit 5; attachment does not generally restart that worker. However, the over-context
branch runs before terminal-state handling and can start a fresh brain when the
session budget permits. Under the freeze, use observation/cancellation and the
item loop.

Only when the human separately authorizes resuming monitoring of that saved job, bound the total
launch budget to its recorded count, so finishing it cannot launch the next item:

```sh
RECORDED_LAUNCHES=$(python3 -c 'import json; n=json.load(open(".backlog/loop.json"))["launches"]; assert isinstance(n, int) and n >= 1; print(n)')
if [ -n "$RECORDED_LAUNCHES" ]; then
  "$BL" loop --max-items "$RECORDED_LAUNCHES" --max-sessions 1 \
    --max-context 130000 --timeout 1800
fi
```

This permits no fresh context-limit continuation; a still-running saved job can
continue doing work and incur usage. If its recorded peak already exceeds 130000,
attachment cancels it immediately and the one-session cap prevents a replacement.
A stopped job still requires attended reconciliation. The 1800 seconds is the monitor wait budget; dispatch calls and
shutdown can extend elapsed time. It changes neither the saved dispatch job's role
timeout nor its original start time. Inspect the association first and serialize
operators: a concurrently replaced association invalidates this recipe.

An in-flight item with no association requires attended reconciliation. Reopening
preserves attempt count; it grants no fourth attempt. For unrelated committed baseline drift see
[baseline recovery](../README.md#recovering-unrelated-committed-changes).

## Context and time limits

The repository's default brain adapter uses native `--autocompact 120000`.
Claude reserves margin, so this is not an exact 120000-input-token event.
`--max-context` is a separate BL fallback, accepting **1–130000**, default 130000.
It observes the largest reported input + cache-write + cache-read count per
distinct assistant message, then cancels and may continue in a fresh brain.
It is a retrospective check, not a pre-request hard limit. Lower settings can cause
more cancellations/continuations; they are not automatically cheaper. Keep the
default 130000 unless deliberately choosing a lower fallback threshold.

Defaults: 3 item launches, up to 5 brain sessions per item, 5000 ms polling,
14400 seconds of loop wait budget (dispatch calls and shutdown can extend elapsed time). The dispatch config separately gives each brain
3600 seconds. `--timeout` on `bl loop` changes the wait budget, not that dispatch
role timeout. A custom `--dispatch-config` can change the adapter and its limits;
inspect the actual config before attributing compaction behavior to BL.

Processed tokens include repeated cached reads and are distinct from peak context
or subscription quota. Loop summaries count the brain job only; include worker
jobs separately when comparing costs. Native compaction and fallback continuation
are different events; record their counts separately. For an investigation, save
item/job IDs, timestamps, outcome, runs/rounds/retries, brain and worker usage, and
leftover jobs or worktrees. Final usage may be missing or provisional after an
interruption. `bl audit` summarizes ledger/command activity, not complete token
usage; it writes under `.backlog`, so wait until verifier windows are finished or
void. No live pilot is part of I140.
