# Playbook: item loop

One backlog item, from "pick it up" to "closed and committed".

`BL=~/.config/opencode/skills/verified-backlog/scripts/bl` — every command below is that binary.

## Step 0: Bootstrap, when there is no ledger yet

Work through [bootstrap.md](bootstrap.md): `bl init`, `BRIEF.md`, then file or import the items.

Completion: `bl status` counts the items and `bl next` names one.

## Step 1: Pick the item and pin the scope

```sh
"$BL" next               # one line: the id and text, or "none"
"$BL" open A1            # locks it; refuses a blocked item or a second lock
```

Copy the item's text verbatim from the ledger (`bl show A1` prints it with its rounds). Pick its
lane with [`../lanes.md`](../lanes.md) and record it with `bl note`; a light-lane item applies that
file's cuts to the steps below.

Write each brief clause as an observable result: what a command prints, its exit code, or what the ledger holds;
say what can be observed, never how it is built. Every clause carries at least one `Example:` with a concrete input
and the exact expected output. `bl brief` refuses a numbered clause without one.

Name the files it may touch, and the ones it may read but not write. A file outside that list is a
signal that the item is scoped too loosely, so tighten it before dispatching rather than discovering
it in the diff.

`bl open` records the before-tree itself and refuses a dirty tree (R5), so the round's diff holds
only this item's changes.

Completion: the item is open, its lane is noted, its intent fits one sentence, and its file list is known.

## Step 2: Dispatch the producer

One item, one producer.

Use [producer.md](producer.md) to render a complete prompt from the item brief, and send its prompt file to the producer
at the same time as verifier dispatch 1 in Step 3. Do not wait for either prompt to return before
sending the other. The producer does not get `checks.tsv` or any frozen check list; it can work while
the verifier writes the checks.

Completion: the producer has reported, and a command in its report shows the property holding.

## Step 3: Dispatch the blind verifier

Two dispatches to the same verifier, in this order. One prompt carrying both defeats the purpose: a
model reads its whole prompt at once, so the producer's claims would reach it before it writes a
single check. Dispatch 1 sends the intent and nothing else.

Use [verifier-checks.md](verifier-checks.md) to render the intent-only prompt, and send its prompt file as
dispatch 1, at the same time as the producer dispatch in Step 2. After both dispatches return, record the checks,
then the claims, then the run:

```sh
"$BL" checks A1 --file <evidence dir>/A1-checks.tsv
"$BL" claims A1 --file <evidence dir>/A1-claims.md
"$BL" run A1
```

`bl run` is the gate: it runs the frozen rows, including the repository's full test suites. Do not
run the suites separately as supervisor; the verifier reads the recorded run.

If the producer finishes first, it waits only for this recording, not for a re-dispatch.

Every list, including a docs-only list, needs at least one blocking `decider: bl` row. Use a mechanical
backstop such as links resolving or examples running (R15).
Put every docs clause in a `decider: judge` row with a specific `fail_if`; the verifier decides it by
reading the docs against the clause's example. Never put a docs clause in a test that greps markdown.
A blocking test row must exercise code; an all-`.md` `@_covers` is refused under R37.

Then send the verifier dispatch 2 with the claims and any required cases, labelled as yours rather
than as its rubric. Where the harness cannot continue a subagent, run both as fresh verifiers: the
first returns a check list, the second re-derives with the claims and adds your cases.

Use [verifier-claims.md](verifier-claims.md) to render the claims prompt, passing an explicit required-cases file
when needed, and send its prompt file as dispatch 2.

Completion: the checks are frozen, the producer claims are recorded, and `bl run` has completed; the
verifier has returned a verdict with a command behind every row.

### Record the round — you set the cause, not the verifier

After the verifier reports, record the round before any other ledger write; amend checks, run and file
after it. For an R39 or R10 refusal, or a void verifier window, use [recovery.md](recovery.md).

```sh
# Use the rows TSV and report paths generated from the verifier JSON draft.
"$BL" round A1 --rows "$ROWS_TSV" --report "$REPORT_MD"
"$BL" round A1 --cause code --rows "$ROWS_TSV" --report "$REPORT_MD"  # failure without a finding
```

`--cause` is yours to set, from what you observed:

| what happened | cause |
|---|---|
| the commands ran and the item failed them | `code` |
| the verifier could not run: timeout, missing tool, killed session | `harness` |

Only `code` counts against the cap of 3. A `harness` failure is the wrapper's bug and must not burn
an item. `bl` refuses a 4th code round: hand the defect table to the human.

The rows are written to `.backlog/evidence/<id>-r<N>.txt`, hashed, and recorded. An empty table is
refused — a verdict exists only after commands have run.

### Set the stopping bar

State the stopping bar in the brief, once, at the start of the item: findings below it belong under
a `Filed` heading, not as failing rows. That matters more than it looks. A verifier that returns
PASS with filed findings gives you an outcome you can record; a verifier that returns FAIL over
cosmetic rows leaves you choosing between editing a verdict you are not allowed to edit and running
a round the bar exists to prevent.

Dispatch the verifier through a second model or harness where one is reachable, and record which one
produced the verdict: [`references/second-harness.md`](../../references/second-harness.md).

## Step 4: Fix rounds

On FAIL, dispatch a producer whose brief is the verifier's failing rows, quoted. Fix those defects
and nothing else; a fix round that also refactors is a fix round you cannot verify.

Re-verify with a **fresh** verifier: the defect list, not the previous verifier's session. Each round
is one `bl round` call; each round's table lands in the evidence directory under its own name.

Two rounds that fail on the same premise are one premise that is wrong. Stop fixing and test the
premise directly. After three code FAIL rounds `bl` itself refuses the fourth, so escalation is the
only path left: hand the defect table to the human, or `bl drop`/`bl defer` the item with a reason.

A frozen row that cannot pass, a file outside scope, damaged evidence or a false premise calls for a
restart, not a fix round: [recovery.md](recovery.md). A later attempt's verifier is covered there too.

Completion: PASS, or an explicit decision that the item's premise needs re-baselining, or the defect
table handed over.

## Step 5: Close

The close is three refusals away from a false claim: no PASS round, a dirty tree, a commit that does
not exist. Fix the cause, do not work around it.

```sh
git add <explicit paths> && git commit -m "<item id>: <what changed>"
"$BL" close A1 --commit HEAD
```

1. Re-run one of the verifier's rows, whichever outcome surprised you most, and note the match. One command, and it catches a fabricated table.
2. Commit with explicit paths. Never `git add -A`; an artefact you did not notice becomes part of the
   commit. Push only if `BRIEF.md` authorises it.
3. `bl close <id> --commit <sha>` writes the record and advances the pointer in the same call.
4. File every finding the verifier reported and you did not fix, before you forget the provenance:

   ```sh
   "$BL" file --id D9 --text "tab leak when two scans overlap" \
              --source verifier:A1/r2 --after A1
   ```

   Use `--after` when the defect cannot be worked until the item closes; omit it when it can wait in
   the queue on its own.
5. If the remaining defects are not worth fixing, say so in the record instead of pretending:

   ```sh
   "$BL" drop D9 --reason "cosmetic; the leak is bounded and not user-visible"
   ```

6. `"$BL" check --views` — it re-hashes the evidence and re-renders both views, so a hand edit or a
   tampered table fails here rather than in a later session.

Completion: `bl check --views` exits 0, `bl next` names an open item or `none`, and the commit exists.

## Step 6: Report

`bl status` for the counts, the one number that proves the item, what was filed, and what is parked.
Then the next item, or hand back.
