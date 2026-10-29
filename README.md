# verified-backlog

A backlog where the state is a **ledger**, not prose. One item at a time goes through a producer, a
blind verifier, and a commit; a small binary owns every write, refuses anything that would make the
record lie, and renders the two human views from the ledger.

```
BRIEF.md              authored: intent, constraints, the breakdown. Never parsed.
.backlog/ledger.jsonl append-only state. Only `bl` writes it.
.backlog/evidence/    round tables, written by `bl`, hashed, re-checked by `bl check`
.backlog/run.log      every invocation: argv, exit code, duration
TODO.md               rendered: Queue · Blocked · Parked · Declined · Closed
PROGRESS.md           rendered: one record per item, with its rounds and notes
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
launches workers. Existing plain `packet` output is unchanged.
