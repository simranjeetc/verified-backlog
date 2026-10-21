---
name: verified-backlog
description: Drive a list of work items to done, one at a time, each gated by an independent blind verifier that re-derives the evidence. Use when the work is a backlog rather than one task: working through a TODO or PROGRESS file, resuming from PROGRESS.md, "pick up where we left off", a batch of three or more items to get through, review comments or failing tests or lint errors to clear, a queue of issues to address, or any change the user wants proven rather than asserted (backfill, re-key, drop, migrate, deploy). Not for a single edit, or for a plan whose steps share one verification.
---

# verified-backlog

Work moves one item at a time through a **producer**, a **blind verifier**, and a commit. Every claim in the record is a command's output, never the producer's word.

The state is a ledger, not prose. `bl` — a compiled binary in `scripts/` — owns it: it validates your arguments, refuses what breaks a rule, appends one line, and re-renders the two views. You judge. It records. Nothing else writes state.

The verifier is independent: a **newly spawned** subagent that has never seen the producer's plan, so it re-derives the answer from primary sources instead of reading the plan back.

## Non-negotiables

The producer optimises for finishing. That instinct is what the verifier exists to check, and these rules are what keep the check real.

1. **One item in verification at a time.** `bl open <id>` locks it; a second `open` is refused. A FAIL then always belongs to a known item. A second producer may run in parallel on disjoint files; its item still waits its turn.
2. **A verifier is spawned fresh for the item, and never sees the producer's session or an earlier round's.** Its own two dispatches — the checks it writes, then the claims it falsifies — are the same verifier continued. If you cannot spawn a subagent, stop and tell the human: self-verification is not a verdict and must not be recorded as one.
3. **The producer does not declare the item passed.** It reports what it changed and what it could not do.
4. **Every claim carries its command.** A number without the command that produced it is a guess. `bl round --rows` will not accept an empty table.
5. **A test must bind, when the item touches a test.** Mutate the code in a throwaway copy and watch the new test fail. On an item that adds no test, the verifier marks the row N/A with its reason.
6. **Findings are filed, never fixed in place.** A verifier that finds something outside the current item's blast radius reports it; you file it as a new item with `bl file … --source verifier:<id>/r<N>`. Fixing it inside the item conflates two verdicts.
7. **Irreversible work stops for a human.** State rewrites, migrations, deploys and deletions run the `irreversible-op` playbook.
8. **Commit locally; push only when the backlog says it may push.** Record that authorisation in `BRIEF.md` when the work starts. A protected branch, no remote, and no git at all are normal conditions: `bl close` refuses without a commit, so say so and use `bl drop`/`bl defer` with a reason instead of faking one.

## The binary

```sh
BL=~/.config/opencode/skills/verified-backlog/scripts/bl
"$BL" status                       # counts, and what is in flight
"$BL" next                         # one line: the item to work on
```

Build it once if it is missing (`cd scripts && go build -o bl ./cmd/bl` inside the skill dir). It is stdlib-only Go: no runtime, no dependencies, ~5 ms per call over a few hundred events.

| verb | writes | does |
|---|---|---|
| `bl init` | – | creates `.backlog/` and renders empty views; refuses over a foreign `TODO.md` |
| `bl file --id A1 --text "…" [--after B2] [--source X]` | yes | adds one item; refuses a duplicate id or an unknown dependency |
| `bl open A1` | yes | locks the item in flight; refuses a blocked item or a second lock |
| `bl round A1 --verdict FAIL --cause code --rows -` | yes | records one round; rows come from stdin or a file |
| `bl close A1 --commit 6742cf6` | yes | verified close; needs a PASS round, a clean tree, a real commit |
| `bl drop A1 --reason "…"` | yes | the human's veto: declined, never a PASS |
| `bl defer A1 --reason "…"` | yes | parked: visible, skipped by `next` |
| `bl reopen A1` | yes | reverses a decline or a park |
| `bl note A1 --text "…"` | yes | one note, scoped to an item |
| `bl next`, `bl show`, `bl status`, `bl check [--views]`, `bl render`, `bl schema [--json]` | no | reads; `render` only rewrites the views, `schema` is the single source for the fields |
| `bl audit [--since 2d]` | report only | arithmetic over the ledger and `run.log` |

Exit status is the contract: **0** ok, **2** refused and nothing written, **3** the ledger is corrupt. A refusal prints `bl: refused: …` to stderr; read it, fix the argument, call again. A refusal is not a failed round — it never reaches the ledger and never counts against the round cap.

The round cap is 3 **code** failures. A `harness` failure is the wrapper's fault, not the item's, so it does not consume a round. At the cap, `bl` refuses further code rounds: hand the defect table to the human.

## The views

`TODO.md` and `PROGRESS.md` are **rendered** from the ledger after every append. They are for humans; no program reads them, and no program should. A hand edit is caught by fact, not policy:

```sh
"$BL" check --views      # FAIL … TODO.md is stale or hand-edited
```

So when the human says "add an item", "note that", "this isn't worth it" — you run the verb. You never type into a view.

`BRIEF.md` is the exception: it is authored, plain markdown, and never parsed. It holds intent, constraints, and the breakdown you wrote after researching the repo. Nothing reads it back.

## Subagents

- **Producer**: the item's text verbatim plus the rules, and not your plan for how to do it, or you will get the plan back as the result.
- **Verifier**: the intent first, then the producer's claims to falsify. Have it write the checks it thinks the item needs *before* it reads the claims, and report both lists. Otherwise the producer's framing becomes the verifier's rubric, which is the one thing that must not leak.
- **Independence comes from the context, and better still from the model.** A fresh session separates the verifier from the producer's reasoning. A *different model*, or a different harness entirely, separates it from the whole family's blind spots: the same brief against a second model is a second opinion, and agreement between two is the strongest signal available. Where a second model or harness is reachable, dispatch the verifier through it — [`references/second-harness.md`](references/second-harness.md) has the invocations and the caveats. If only one family is available, say so in the record, so a single-family verdict is not read as a corroborated one.
- The verifier returns a table and a `Verdict:` line. **You** pass that table to `bl round --rows`, and you set `--cause` from what you observed: `code` when the commands ran and the item failed them, `harness` when the verifier itself could not run (timeout, missing tool, killed session). Never let the model choose the cause for a verdict it also issued.
- Re-run one of the verifier's rows yourself, whichever outcome you would find most surprising, and note the match. One command, and it catches a fabricated table.

## Playbooks

Open the file, copy its steps into your task list verbatim, then work them. A step you skip stays in the list with a one-line reason, so the record shows the choice.

| playbook | for |
|---|---|
| [`playbooks/item-loop.md`](playbooks/item-loop.md) | the default: one item through producer, blind verifier, fix rounds, close, commit. Its Step 0 initialises the ledger or imports a prose backlog |
| [`playbooks/irreversible-op.md`](playbooks/irreversible-op.md) | a rewrite of real state: migration, re-key, deletion, deploy |
| [`playbooks/design-doc.md`](playbooks/design-doc.md) | a decision other work will be built on, where being wrong is expensive |

## Principles

- **Sequence into verifiable units.** A unit that ends in "and then the next unit will tell us" has no completion criterion, so it cannot be verified.
- **Prove it on the real artifact.** A green test over a fixture the producer also wrote is a tautology. Prefer the number a third party could reproduce.
- **Re-baseline a wrong premise; do not drop it.** When an item's own premise turns out false, correct it in place with the measurement that overturned it.
- **Stop when the marginal finding stops being worth its fix.** A round that can only report cosmetics or deliberately malformed input is the end of the loop. Agree that bar with the verifier before the first round, so a strict reviewer cannot extend the loop on reflex; then stopping is a decision you both already made — and the human's answer to a low-value item is `bl drop <id> --reason "…"`, recorded, not argued.

## The files

```
BRIEF.md            authored: intent, constraints, the breakdown. Never parsed.
.backlog/ledger.jsonl   append-only state. Only bl writes it.
.backlog/evidence/      round tables, written by bl, hashed, re-checked by `bl check`
.backlog/run.log        every invocation: argv, exit code, duration
TODO.md             rendered: Queue · Blocked · Parked · Declined · Closed
PROGRESS.md         rendered: one record per item, with its rounds and notes
```

Ids are short and unique, case-insensitively: `A1`, `E13`, `C8a`, `PROJ-12`. A duplicate is refused, because two entries on one id collapse into one and an open item disappears.

`bl check` re-hashes every evidence file, so a tampered table is a FAIL rather than a footnote.

## Migrating a prose backlog

One release of `scripts/check-backlog.py` remains, as a bridge:

```sh
"$BL" lint TODO.md PROGRESS.md     # what would import, and what it cannot read
"$BL" import TODO.md PROGRESS.md   # one pass into a fresh ledger
```

The import is a claim, not a proof: a closed item becomes a PASS round and a close carrying `legacy:unknown`, with a note saying it was taken on the prose record's word. It runs once, refuses a non-empty ledger, and refuses to overwrite a `TODO.md` it did not render. After that, `check-backlog.py` is deleted.
