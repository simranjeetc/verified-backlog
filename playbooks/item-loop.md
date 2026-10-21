# Playbook: item loop

One backlog item, from "pick it up" to "closed and committed".

`BL=~/.config/opencode/skills/verified-backlog/scripts/bl` — every command below is that binary.

## Step 0: Bootstrap, when there is no ledger yet

```sh
"$BL" init              # .backlog/ + empty views; refuses over a foreign TODO.md
```

Write `BRIEF.md` first with the human: intent, constraints, what "done" means, and whether this
backlog may push. Then research the repo, write the breakdown back into `BRIEF.md` as prose, and
file the items it describes — one call each:

```sh
"$BL" file --id A1 --text "wellfound remote sweep alongside the location sweep" --source human
"$BL" file --id A2 --text "pagination flags wiring test" --after A1 --source human
```

`--after` records a dependency: the item exists now, `bl next` skips it until A1 closes.

If a prose backlog already exists, do not rewrite it by hand:

```sh
"$BL" lint TODO.md PROGRESS.md      # read-only: what parses, what does not
"$BL" import TODO.md PROGRESS.md    # one pass, fresh ledger only
```

An imported close is a claim, not a proof: the ledger records that the prose said PASS and which
commit it named, and a note says so. Treat imported items as untrusted until a real round runs.

Completion: `bl status` counts the items and `bl next` names one.

## Step 1: Pick the item and pin the scope

```sh
"$BL" next               # one line: the id and text, or "none"
"$BL" open A1            # locks it; refuses a blocked item or a second lock
```

Copy the item's text verbatim from the ledger (`bl show A1` prints it with its rounds).

Name the files it may touch, and the ones it may read but not write. A file outside that list is a
signal that the item is scoped too loosely, so tighten it before dispatching rather than discovering
it in the diff.

Record the before-state, so the verifier can tell your item's changes from everything else:
`git status --porcelain` **and** `git diff` into a file under `.backlog/evidence/`, plus a hash of
each file that is already dirty, or a marker file when there is no git. Pass that path into the
verifier's brief. Without it, a file that was already modified and got modified again looks
unchanged.

Completion: the item is open, its intent fits one sentence, its file list is known, and the
before-state is recorded.

## Step 2: Dispatch the producer

One item, one producer.

```
You are the PRODUCER for one item in a verify-driven loop.

Repo: <path>. Read AGENTS.md, and read <the files this item concerns>.

ITEM (<id>), verbatim from the backlog:
  <item text>

PRODUCE
  <what to change, and the property it must hold>

RULES
  - Do NOT commit or push.
  - Do NOT edit the ledger, TODO.md, PROGRESS.md, BRIEF.md, or any spec the item points at.
  - Do NOT fix anything outside this item, even when you find it broken. Report it.
  - Do NOT declare the item passed. A separate verifier decides.
  - Do NOT weaken an existing test to make it pass. If a guard blocks you, report it.
  - Leave no untracked file behind in the repo.
  - Write your evidence (commands and output) to <evidence dir>/<id>-evidence.md.

REPORT BACK: files changed, the property you implemented and how it holds, the
commands that prove it, the repo's test command and its result, and anything you
could not do.
```

Completion: the producer has reported, and a command in its report shows the property holding.

## Step 3: Dispatch the blind verifier

Two dispatches to the same verifier, in this order. One prompt carrying both defeats the purpose: a
model reads its whole prompt at once, so the producer's claims would reach it before it writes a
single check.

Dispatch 1 sends the intent and nothing else:

```
You are an independent VERIFIER for one item. Repo: <path>.

INTENT — <id>: "<item text, verbatim>"

Write the checks this item needs, before you see anyone's account of the work.
Each check is a command with an expected result. Include the case that must still
hold after the change, and the number you would re-derive.

OUTPUT: the numbered check list, and nothing else.
```

Dispatch 2 sends the claims, plus any cases you require, labelled as yours rather than as its rubric.
Where the harness cannot continue a subagent, run both as fresh verifiers: the first returns a check
list, the second re-derives with the claims and adds your cases.

```
The producer claims:
  - <claim 1, with the number it reported>
  - <claim 2>

Falsify each claim with your own checks, plus:
  - <the property, on a case you construct yourself>
  - test binding: when the item touches a test, mutate the code in a copy and
    confirm that test fails. Otherwise mark this row N/A with your reason
  - the repo's documented test command; if there is none, or it is slow, name the
    subset you ran and why
  - hygiene: nothing changed outside <the item's file list>, no untracked file
    left behind, and no difference against <before-state file> — add the file list
    of any parallel item, whose edits are expected to show

Work on copies under the evidence dir; leave the real data read-only. Do NOT edit
tracked files.

OUTPUT: | check | command | expected | observed |, then a Verdict line,
     Verdict: PASS
     Verdict: FAIL - <the rows that failed>
then the strongest counterexample you found or "none found, tried X, Y, Z".
Write this to <evidence dir>/<id>-verify-r<N>.md verbatim.
```

Completion: the verifier has returned a verdict with a command behind every row, and its own check
list beside it.

### Record the round — you set the cause, not the verifier

```sh
"$BL" round A1 --verdict PASS --rows <evidence dir>/A1-verify-r1.md
"$BL" round A1 --verdict FAIL --cause code --rows -      # table on stdin
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

### Getting the verifier out of your own family

State the stopping bar in the brief, once, at the start of the item: findings below it belong under
a `Filed` heading, not as failing rows. That matters more than it looks. A verifier that returns
PASS with filed findings gives you an outcome you can record; a verifier that returns FAIL over
cosmetic rows leaves you choosing between editing a verdict you are not allowed to edit and running
a round the bar exists to prevent.

A verifier on the same model as the producer shares its blind spots. Where a second model or a
second harness is reachable, dispatch the verifier through it: the invariant, the invocations for
the harnesses known to work, and the caveats are in
[`references/second-harness.md`](../references/second-harness.md).

Two of those caveats bite every time, so they are worth repeating here. Write the brief to a file and
pass the file (`"$(cat <brief>)"`) rather than an inline prompt, because a multi-line brief does not
survive shell quoting. And record which model or harness produced the verdict: if only one family
was reachable, say so, because a single-family pass is a weaker claim than a corroborated one.

## Step 4: Fix rounds

On FAIL, dispatch a producer whose brief is the verifier's failing rows, quoted. Fix those defects
and nothing else; a fix round that also refactors is a fix round you cannot verify.

Re-verify with a **fresh** verifier: the defect list, not the previous verifier's session. Each round
is one `bl round` call; each round's table lands in the evidence directory under its own name.

Two rounds that fail on the same premise are one premise that is wrong. Stop fixing and test the
premise directly. After three code FAIL rounds `bl` itself refuses the fourth, so escalation is the
only path left: hand the defect table to the human, or `bl drop`/`bl defer` the item with a reason.

Completion: PASS, or an explicit decision that the item's premise needs re-baselining, or the defect
table handed over.

## Step 5: Close

The close is three refusals away from a false claim: no PASS round, a dirty tree, a commit that does
not exist. Fix the cause, do not work around it.

```sh
git add <explicit paths> && git commit -m "<item id>: <what changed>"
"$BL" close A1 --commit HEAD
```

1. Re-run one of the verifier's rows, whichever outcome surprised you most, and note the match.
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
