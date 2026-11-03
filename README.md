# verified-backlog

A backlog where the state is a **ledger**, not prose. One item at a time goes through a producer, a
blind verifier, and a commit; a small binary owns every write, refuses anything that would make the
record lie, and renders the two human views from the ledger.

```
BRIEF.md              authored: intent, constraints, the breakdown. Never parsed.
.backlog/ledger.jsonl append-only state. Only `bl` writes it.
.backlog/evidence/    round tables, written by `bl`, hashed, re-checked by `bl check`
.backlog/run.log      every invocation: argv, exit code, duration
TODO.md               rendered, gitignored: Queue · Blocked · Parked · Declined · Closed
PROGRESS.md           rendered, gitignored: one record per item, with its rounds and notes
```

## Why

A markdown backlog asks the model to follow a format. Every paraphrase is a chance to drift, and a
drifted record is invisible until someone audits it. Here the model supplies judgement as arguments
and `bl` supplies everything else: validation, ordering, hashing, rendering.

A refusal exits `2` and writes nothing. That is the whole design in one sentence:

```
$ bl round A1 --verdict PASS --rows -
bl: refused: --rows is empty: a verdict exists only after commands have run
$ bl close A1 --commit HEAD
bl: refused: working tree is dirty — commit or stash before closing: M jobscan.py
$ bl check --views
FAIL TODO.md is stale or hand-edited
```

## Install

Requires Go 1.24+ to build; the binary itself needs nothing.

```sh
git clone git@github.com:simranjeetc/verified-backlog.git ~/codebase/verified-backlog
ln -s ~/codebase/verified-backlog ~/.agents/skills/verified-backlog   # OpenCode skill dir
cd ~/codebase/verified-backlog/scripts && go build -o bl ./cmd/bl
```

## Use

```sh
BL=~/codebase/verified-backlog/scripts/bl

"$BL" init                                  # .backlog/ + empty views
"$BL" file --id A1 --text "…" [--after B2] [--source human]
"$BL" next                                  # one line: what to work on
"$BL" open A1                               # lock it; a second open is refused
"$BL" round A1 --verdict FAIL --cause code --rows table.txt
"$BL" round A1 --verdict PASS --rows table.txt
"$BL" close A1 --commit <sha>               # needs PASS, clean tree, real commit
"$BL" drop A1 --reason "…"                  # the human's veto: never a PASS
"$BL" defer A1 --reason "…"                 # parked: visible, skipped by next
"$BL" check --views                         # invariants, evidence hashes, view drift
"$BL" audit --since 2d                      # arithmetic over the ledger + run.log
```

Exit codes: `0` ok, `2` refused (nothing written), `3` the ledger is corrupt.

The loop itself — producer prompt, blind verifier with two dispatches, fix rounds, close — is in
[`playbooks/item-loop/index.md`](playbooks/item-loop/index.md). The rules that make the verification real are in
[`SKILL.md`](SKILL.md).

## Gates

| gate | refusal |
|---|---|
| duplicate id | `id A1 already exists` |
| two items in flight | `A1 is already in flight` |
| round without a pin | `A1 is not the item in flight` |
| FAIL without a cause | `a FAIL needs --cause code\|harness` |
| verdict with no rows | `a verdict exists only after commands have run` |
| 4th code round | `spent 3 code rounds (cap 3) — escalate` |
| close with no PASS | `A1 has no PASS round` |
| close on a dirty tree | `working tree is dirty` |
| close at an unknown commit | `commit deadbeef does not exist` |
| hand-edited view | `TODO.md is stale or hand-edited` |
| tampered evidence | `round … does not match its hash` |

## Tests

```sh
python3 scripts/bl.test.py      # BL unset builds from the working tree; BL set uses that binary
cd scripts && go test ./...     # 11 unit tests
```

The gate suite asserts two things per refusal: the exit code, and that the ledger is byte-identical
afterwards. A refusal that quietly wrote a line would still look like a refusal in a transcript.

## Migrating a prose backlog

```sh
"$BL" lint TODO.md PROGRESS.md      # what parses, what does not
"$BL" import TODO.md PROGRESS.md    # one pass into a fresh ledger
```

An imported close is a claim, not a proof: the ledger records that the prose said PASS and which
commit it named, and a note says so. Import runs once, refuses a non-empty ledger, and refuses to
overwrite a `TODO.md` it did not render.

## Licence

MIT.

Loop usage reports `peak-context` separately from cumulative `processed-tokens`
(input + cache writes + cache reads + output). These counts describe processing,
not subscription allowance or billed cost. Repeated streamed messages are counted
once with updated usage; cumulative `result.modelUsage` checkpoints supply final
counts without being added together. Work after the latest checkpoint, or a log
without one, is labelled `usage provisional`: output may remain incomplete if a
job stops before reporting its final usage. Accounting covers the brain job only;
child jobs and other sessions are not included.

Each fresh loop brain launch, including a context-limit continuation, receives a
supervisor packet derived from the current ledger: intent, attempt and stage,
recorded and pending artifacts, local CLI commands, routing authority, and the
next action to assess. The packet points to `playbooks/item-loop/index.md` and
does not depend on handoff prose. Producer workspaces are explicitly unknown
until reconciled with worktrees and existing jobs. Generating the packet does
not change the ledger or issue a verifier packet; role packets and gates remain
responsible for blindness and evidence recording.

The default Claude brain adapter sets `--autocompact 120000`, a native compaction
window (Claude reserves a margin, so this is not an exact input-token trigger).
The independent BL peak-context safety cap is 130000 by default and cannot be raised above 130000; lower
`--max-context` values are allowed. It uses fresh
session continuation after cancellation and terminal-job confirmation.


Role prompts can be assembled without a model call:

```sh
mkdir -p /tmp/bl-prompts
"$BL" packet A1 --role producer --render --workspace "$PRODUCER_WORKTREE" --output-dir /tmp/bl-prompts > /dev/null
"$BL" packet A1 --role checks --render --workspace "$REPO" --output-dir /tmp/bl-prompts > /dev/null
# After recording checks, claims and the run:
"$BL" packet A1 --role verifier --render --workspace "$REPO" --output-dir /tmp/bl-prompts > /dev/null
```

Rendering can print the complete prompt and writes an attempt-specific prompt file
in the output directory. Suppress stdout as above when dispatching the saved file
to avoid loading worker instructions into supervisor context. Both directories must exist. Templates are versioned and
embedded in the binary, so target repositories need no template installation.
Producer/checks prompts exclude claims and frozen checks; verifier rendering
retains the existing verifier-window recording. Prepare other prompts first and
keep the backlog unchanged through verification. Optional verifier-only
`--required-cases-file FILE` adds explicitly requested cases. Rendering never
launches workers. Plain `packet` remains available.

Prepare verifier evidence without hand-writing TSV or metadata:

```sh
# After a fresh verifier packet, before investigating; use a new draft path:
"$BL" verdicts A1 --init --output /tmp/bl-prompts/A1-judgments.json
# The independent verifier fills summary, judge verdicts/anchors, finding and notes.
"$BL" verdicts A1 --file /tmp/bl-prompts/A1-judgments.json --output /tmp/bl-prompts/A1-rows.tsv --report /tmp/bl-prompts/A1-report.md
"$BL" round A1 --rows /tmp/bl-prompts/A1-rows.tsv --report /tmp/bl-prompts/A1-report.md
```

Keep the generated `binding` unchanged. It pins the item, attempt, verifier packet,
checks and recorded run/tree; changed source or evidence requires fresh verification,
not editing metadata. BL derives mechanical verdicts from the hash-verified run,
orders all rows, escapes the evidence structure, and generates report-only tags,
counts and disposition comments. The verifier supplies only judgment rows and its
independent observations; `summary` is required even for an all-mechanical list.
Notes use `text` and `disposition` (`fix`, `filed` with `item`, or `waived` with
`reason`). Findings remain blocking disputes, and a `fix` note prevents PASS.
A mechanical FAIL without a finding needs `--cause code` when recording the round.

Outputs must be outside the repository, including symlink aliases. Initialization
requires a new draft path. Conversion writes the rows and report but records no
round and grants no PASS; existing round gates still apply independently. If an
output I/O operation fails, do not use a partial pair: rerun conversion successfully.
Legacy TSV input to `round` remains supported. Rendered verifier prompts include
exact commands and artifact paths for this workflow.

### Recovering unrelated committed changes

When another commit changes files outside an in-flight item's explicit scope, run
`bl baseline ID --reason "describe the external commit"`. This records the old and
new baseline trees, the current HEAD, accepted paths, and reason without spending
an attempt. Only changed outside-scope entries that exactly match HEAD are accepted;
the original baseline inside scope stays intact, including producer edits already
committed. The working tree and real Git index are untouched.

Recovery refuses uncommitted outside-scope changes, ignore/attribute policy changes,
a missing explicit scope, damaged frozen artifacts, any previous PASS in the attempt,
and a still-valid verifier window. Finish that window first, or use `bl run` to void
it before recovery. After recovery, obtain a fresh `bl run`, verifier packet, and
independent verifier; earlier runs cannot support another round. Earlier rounds,
round budgets, attempts, and frozen checks remain unchanged. This differs from
`bl rebaseline`, which replaces a check list before claims. It does not reset an
exhausted attempt or recover a bad check list.

Repositories with submodules may require attended recovery: the verified tree
records their working files, whereas HEAD records a Git link. This command refuses
entries it cannot match rather than excluding them. As with other ledger commands,
serialize writers; its final tree, HEAD and ledger checks detect intervening changes
but do not provide a cross-process transaction.

### Cancel a supervised loop

Use the [operator runbook](playbooks/loop.md) for monitoring and exit-code recovery.
This recovery guide does not unfreeze new launches or authorize a pilot.

Run `scripts/bl loop --cancel --reason "why you stopped it"` from the repository.
It requests a running loop to yield its lock, cancels the brain first, then cancels
registered workers and dispatch jobs in the repository and its worktrees. It parks
unfinished work and releases the association. `--timeout SECONDS` bounds the wait
for jobs to stop (default 120 seconds; dispatch calls have their own short bounds).
A lost job cannot prove that its processes ended and is reported as a leftover.

Partial work is saved under `.backlog/cancelled/`. Root changes get a binary patch;
restoration requires unchanged committed entries within scope and no staged work
there. Committed or staged changes are preserved and filed for attended recovery.
Detached worktree HEADs are retained under `refs/bl-cancel/ITEM/COMMIT` to preserve
commit history. Only worktrees under recorded loop session directories are removed, after a binary
patch and a full archive preserve their tracked, untracked and ignored files.
Unrelated worktrees remain. When a worker cannot be confirmed stopped, files and
worktrees remain; a follow-up item records the exact jobs or paths requiring recovery.
Preservation failures retain the source files and file a follow-up. Failures to
record the disposition retain the association.

Worker launches must use `scripts/dispatch.sh` from the assigned workspace. A loop
passes ownership through the environment; the wrapper registers each dispatch job
under a session launch lock before waiting. Shutdown fences new launches and waits
for that lock before discovering jobs. Ownership tags in dispatch metadata cover the gap before receipt
creation if the wrapper is killed. Receipts live in the loop session directory outside the repository,
so they do not alter verifier evidence. While `bl loop` is monitoring, a brain's
completion, failure or timeout triggers cancellation of registered leftover workers;
the context-limit continuation also cleans workers before starting another brain.
Errors name the worker jobs that could not be confirmed stopped. A timeout of the
loop wait also cancels its brain. Killing the monitor itself does not install an
external watchdog: resume it or run `--cancel` to recover. Legacy direct runtime
processes without dispatch jobs cannot be identified safely by this command.
