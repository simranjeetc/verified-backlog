# Lanes: how much process an item gets

Pick the lane before `bl open`, from the size and the risk of the change. The process scales with the
item; the guarantees `bl` enforces do not. Every lane that uses the ledger still gets a B0 brief with
an `Example:` per clause (R2), a frozen check list with a blocking `decider: bl` row (R15), derived
coverage for changed code paths (R10), judge rows for green reverts and documentation, a fresh blind
verifier, and a close on a real commit.

A **contract** below is anything another party depends on: a public API, a schema, a file or wire
format, CLI flags, auth and permissions, money or persisted data, concurrency, the build or test setup.

## Decide

Take the highest lane whose row matches. When two rows fit, take the higher one.

| lane | pick when | path |
|---|---|---|
| **direct** | no behaviour change: copy, a comment, formatting, an ignore-file or config-hygiene line | no item; commit the explicit paths with a message that says why |
| **light** | one observable behaviour, ≤ 3 files and ≈ 50 changed lines, no contract touched | the item loop, with the light-lane cuts below |
| **standard** | anything bigger; any contract touched; an item that already had a FAIL or a restart | the [item loop](item-loop/index.md) as written |
| **heavy** | irreversible state (migration, backfill, re-key, deletion, deploy, release), or a decision other work builds on | standard plus [`irreversible-op`](irreversible-op.md) or [`design-doc`](design-doc.md) |

Record the pick before the brief: `bl note A1 --text "lane: light — 2 files, one handler, no contract"`.

Escalate when the work outgrows its lane: a diff past the light bound, a fourth file, or a contract
touched mid-item makes it standard from that point, with a `bl note` saying why. A lane only goes up.

## Examples

| item | why | lane |
|---|---|---|
| fix a typo in the onboarding copy; add `.env.local` to `.gitignore` | no behaviour | direct |
| `GET /orders/:id` returns 404 instead of 500 for an unknown id | one handler and its test | light |
| the date formatter no longer crashes on a null timestamp | one function, one guard | light |
| add the missing `aria-label` to the search button | one component | light |
| add pagination to `GET /orders` | API contract: every client reads the response shape | standard |
| rate-limit login attempts | auth | standard |
| change the job queue's retry and backoff | concurrency | standard |
| rename a column three services read | schema | standard |
| backfill `users.email_normalized` in production | irreversible data write | heavy |
| choose the datastore for the events service | other work builds on it | heavy |

The bounds come from this skill's own ledger: items of ≤ 3 files and about 40 lines passed in one or two
rounds (I106, I103), while items that changed a contract took three or four (I118, I115).

## Light-lane cuts

The light lane runs every step of the item loop; these are the parts it shrinks.

1. **Brief**: B0 plus one or two numbered clauses, each with its `Example:`. The bar line reads
   `Bar: blocking rows for the clauses only; every other finding is Filed`.
2. **Checks**: the suite gate plus one blocking row per clause and per changed path. The verifier
   writes them blind, as always; the bar tells it to stop there.
3. **Verifier**: one model family is enough; say so in the record. A second-harness verifier belongs
   to the standard lane.
4. **Report**: report-only notes are `filed` or `waived`, never a reason for another round.
5. **Close**: re-run the one surprising row, commit, `bl close`. A FAIL round makes the item standard.

`bl loop` runs every item in the standard lane.
