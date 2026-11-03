---
name: verified-backlog
description: Drive a backlog to done, one item at a time, each gated by an independent blind verifier that re-derives the evidence. Use for a TODO or PROGRESS file, "pick up where we left off", a batch of three or more items, review comments or failing tests or lint errors to clear, a queue of issues, or any change the user wants proven rather than asserted (backfill, re-key, drop, migrate, deploy). Not for a single edit, or a plan whose steps share one verification.
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
4. **Every claim carries its command.** A number without the command that produced it is a guess. `bl round --rows` will not accept a verdicts file that does not match the frozen list, the run and the tree.
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

Resume with `bl next` and `bl show <id>`; the views are for humans.

Build it once if it is missing (`cd scripts && go build -o bl ./cmd/bl` inside the skill dir).

Verbs: `init` `file` `open` `brief` `checks` `rebaseline` `claims` `run` `verdicts` `round` `close` · `drop` `defer` `reopen` `restart` `note` · `bump` `unbump` `rank` · `next` `show` `packet` `status` `check` `render` `schema` `audit` · `intake` `import` `lint` `loop` `second-check` `signoff`.

The playbooks carry the flow commands. For a verb outside them, or an unclear refusal, read [`references/verbs.md`](references/verbs.md).

Exit status is the contract: **0** ok, **2** refused and nothing written, **3** the ledger is corrupt. A refusal prints `bl: refused: …` to stderr; read it, fix the argument, call again. A refusal is not a failed round — it never reaches the ledger and never counts against the round cap.

## The files

`TODO.md` and `PROGRESS.md` are rendered from the ledger after every append, and gitignored. So when the human says "add an item", "note that", "this isn't worth it" — you run the verb. `bl check --views` catches a hand edit; `bl render` rebuilds them.

`BRIEF.md` is authored, plain markdown, and never parsed: intent, constraints, and the breakdown you wrote after researching the repo.

`.backlog/` holds `ledger.jsonl` (append-only; only `bl` writes it), `evidence/` (round tables, hashed, re-checked by `bl check`) and `run.log` (every invocation).

Ids are short and unique, case-insensitively: `A1`, `E13`, `C8a`, `PROJ-12`. A duplicate is refused, because two entries on one id collapse into one and an open item disappears.

## Subagents

- **Producer**: the item's text verbatim plus the rules, and not your plan for how to do it, or you will get the plan back as the result.
- **Verifier**: the intent first, then the producer's claims to falsify. Have it write the checks it thinks the item needs *before* it reads the claims, and report both lists. Otherwise the producer's framing becomes the verifier's rubric, which is the one thing that must not leak.
- **Independence comes from the context, and better still from the model.** A fresh session separates the verifier from the producer's reasoning; a different model or harness separates it from the whole family's blind spots. Where one is reachable, dispatch the verifier through it: [`references/second-harness.md`](references/second-harness.md). If only one family is available, say so in the record.
- The verifier fills the bound JSON draft and converts it with `bl verdicts` into rows TSV and a report. **You** pass those files to `bl round --rows ... --report ...` and set the cause from what you observed, because the model never chooses the cause for a verdict it also issued.

## Playbooks

Pick the item's lane first with [`playbooks/lanes.md`](playbooks/lanes.md): a typo needs no item, a small fix runs the light lane, a refusal rule or a migration gets the full loop. Then open the file, copy its steps into your task list verbatim, and work them. A step you skip stays in the list with a one-line reason, so the record shows the choice.

| playbook | for |
|---|---|
| [`playbooks/item-loop/index.md`](playbooks/item-loop/index.md) | the default: one item through producer, blind verifier, fix rounds, close, commit. Its Step 0 initialises the ledger or imports a prose backlog |
| [`playbooks/loop.md`](playbooks/loop.md) | monitoring, cancellation and exit-code recovery for existing loop runs; I140 is authorized for documentation only, while new loop launches remain frozen |
| [`playbooks/irreversible-op.md`](playbooks/irreversible-op.md) | a rewrite of real state: migration, re-key, deletion, deploy |
| [`playbooks/design-doc.md`](playbooks/design-doc.md) | a decision other work will be built on, where being wrong is expensive |

## Principles

- **Sequence into verifiable units.** A unit that ends in "and then the next unit will tell us" has no completion criterion, so it cannot be verified.
- **Prove it on the real artifact.** A green test over a fixture the producer also wrote is a tautology. Prefer the number a third party could reproduce.
- **Re-baseline a wrong premise; do not drop it.** When an item's own premise turns out false, correct it in place with the measurement that overturned it.
- **Stop when the marginal finding stops being worth its fix.** A round that can only report cosmetics or deliberately malformed input is the end of the loop. Agree that bar with the verifier before the first round, so a strict reviewer cannot extend the loop on reflex; then stopping is a decision you both already made — and the human's answer to a low-value item is `bl drop <id> --reason "…"`, recorded, not argued.
