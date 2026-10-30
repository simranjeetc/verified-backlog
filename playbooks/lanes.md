# Lanes: how much process an item gets

Pick the lane before `bl open`, from the size and the risk of the change. The process scales with the
item; the guarantees `bl` enforces do not. Every lane that uses the ledger still gets a B0 brief with
an `Example:` per clause (R2), a frozen check list with a blocking `decider: bl` row (R15) naming every
changed path (R10), a fresh blind verifier, and a close on a real commit.

## Decide

Take the highest lane whose row matches. When two rows fit, take the higher one.

| lane | pick when | path |
|---|---|---|
| **direct** | no behaviour change: a typo, a comment, formatting, an ignore-file or config-hygiene line | no item; commit the explicit paths with a message that says why |
| **light** | one observable behaviour, ≤ 3 files and ≈ 50 changed lines, no change to a refusal rule, the ledger or evidence format, hashing, or how `bl run` executes rows | the item loop, with the light-lane cuts below |
| **standard** | anything bigger; any change to a refusal rule (R-number), the ledger or evidence format, hashing, or how `bl run` executes rows; an item that already had a FAIL or a restart | the [item loop](item-loop/index.md) as written |
| **heavy** | irreversible state (migration, re-key, deletion, deploy), or a decision other work builds on | standard plus [`irreversible-op`](irreversible-op.md) or [`design-doc`](design-doc.md) |

Record the pick before the brief: `bl note A1 --text "lane: light — 2 files, one message, no rule change"`.

Escalate when the work outgrows its lane: a diff past the light bound, a third file, or a refusal rule
touched mid-item makes it standard from that point, with a `bl note` saying why. A lane only goes up.

## Examples from this backlog

| item | change | rounds | lane |
|---|---|---|---|
| typo in `README.md`; `.lavish/` added to `.gitignore` | no behaviour | – | direct |
| I106 dispatch.sh takes the final message when the out file is missing (`ee9c74c`) | 1 file, 15 lines | P | light |
| I105 R27's hash skips `.backlog/run.log` (`3ad7754`) | 3 files, 37 lines, but hashing: the rule outranks the size | P | standard |
| I103 one-line message when go cannot start (`1ea9c6a`) | 2 files, 28 lines | FP | light |
| I118 refuse invalid scope entries at open (`61a68a3`) | 3 files, 94 lines, a refusal rule | FFP | standard |
| I115 fail-closed Report-only markers (`a645a71`) | 14 files, 379 lines, round format | FFFP | standard |
| re-keying ledger ids; deleting `.backlog/evidence/` | irreversible | – | heavy |

## Light-lane cuts

The light lane runs every step of the item loop; these are the parts it shrinks.

1. **Brief**: B0 plus one or two numbered clauses, each with its `Example:`. The bar line reads
   `Bar: blocking rows for the clauses only; every other finding is Filed`.
2. **Checks**: the suite gate plus one blocking row per clause and per changed path. The verifier
   writes them blind, as always; the bar tells it to stop there.
3. **Verifier**: one model family is enough; say so in the record. A second-harness verifier belongs to the standard lane.
4. **Report**: report-only notes are `filed` or `waived`, never a reason for another round.
5. **Close**: re-run the one surprising row, commit, `bl close`. A FAIL round makes the item standard.

`bl loop` runs every item in the standard lane.
