# `bl` verbs

Usage, flags and refusals, one row per verb. `bl schema` lists the event fields; every verb refuses with `bl: refused: …` and exit 2.

| verb | writes | does |
|---|---|---|
| `bl init` | – | creates `.backlog/` and renders empty views; refuses over a foreign `TODO.md` |
| `bl file --id A1 --text "…" [--after B2] [--source X] [--urgent]` | yes | adds one item; refuses a duplicate id or an unknown dependency; `--urgent` also puts it at the front of the human's priority list, as a `bl bump A1` would |
| `bl open A1` | yes | locks the item in flight; refuses a blocked item or a second lock; after a restart, carries the ended attempt's scope unless either scope flag is given |
| `bl brief A1 --file brief.md` | yes | records or amends the brief; B0 purpose is required, every numbered clause needs an `Example:` (R2), and verifiers declare `Report-only: N` |
| `bl checks A1 --file checks.tsv [--tests tests.py]` | yes | freezes the verifier's check list — format 2, whose rows are test calls or one simple command each — and the test file the rows call; written once, and `--amend` may only add a row or grow the test file by an insertion |
| `bl rebaseline A1 --file checks.tsv --reason "…" [--tests tests.py]` | yes | replaces the frozen list (and, with `--tests`, the test file) with a new premise and the reason for it; refused once claims exist |
| `bl claims A1 --file claims.md` | yes | records the producer's report; once, and only after the list is frozen |
| `bl intake --check|--render --file <intake.jsonl>`; `bl schema intake` | no | validates an intake without writing, or renders it for approval; schema lists source, decision, item and clause records; filing is I111 |
| `bl run A1` | yes | runs every row of the frozen check list under a fixed environment, writes `.backlog/evidence/A1-run1.json` with each row's exit code, `ms` duration and full output, and records the file's hash |
| `bl verdicts A1 --init --output /tmp/draft.json`; `bl verdicts A1 --file /tmp/draft.json --output /tmp/rows.tsv --report /tmp/report.md` | no | prepares evidence from bound JSON judgments; derives mechanical verdicts and report numbering; outputs stay outside the repository; see [verifier preparation](../playbooks/item-loop/verifier-claims.md) |
| `bl round A1 --rows verdicts.tsv --report report.md` | yes | records one round: B0 briefs require `--report`; `bl verdicts` generates `Report-only: N` and each note's disposition (except harness rounds) |
| `bl close A1 --commit 6742cf6` | yes | verified close; needs a PASS round whose pinned tree matches the working directory, no tracked non-artefact diff from the commit, and a real commit |
| `bl drop A1 --reason "…"` | yes | the human's veto: declined, never a PASS |
| `bl defer A1 --reason "…"` | yes | parked: visible, skipped by `next` |
| `bl reopen A1` | yes | reverses a decline or a park |
| `bl restart A1 --reason bad-check --why "…" --change "…"` | yes | ends the item's attempt and starts the next one, carrying its scope into the next `bl open` unless scope flags are given; `--reason` is one of `bad-check`, `extra-file`, `damaged-evidence`, `wrong-premise`. It writes the attempt's failure record and leaves the item open and out of flight |
| `bl note A1 --text "…"` | yes | one note, scoped to an item |
| `bl bump A3 A1` | yes | the human's priority list (tier 1), in the order given; a later bump goes in front of an earlier one; refuses an unknown, closed, dropped or repeated id |
| `bl unbump A3` | yes | takes the id out of tier 1; a later `bl bump` puts it back |
| `bl rank --file ranking.tsv` | yes | the loop's ranking (tier 2): one `id<TAB>reason` line per item, in rank order, replacing the last ranking; refuses a tier-1, unknown, closed, dropped or repeated id and an empty reason |
| `bl loop [--max-items N] [--max-context N] [--max-sessions N] [--poll-ms N] [--timeout SECONDS] [--dispatch-config FILE]` | yes | **frozen since 2026-10-30**: no new loop features while its items park on timeouts and sandbox blocks; drive items with the item loop. Launches a `brain` dispatch per item (default launch budget 3); checks dispatch jobs in the repo and every `git worktree list` worktree before advancing or continuing. Each session's peak request context is input + cache creation + cache read tokens for one distinct assistant message id, capped at 130000 by default; --max-context accepts 1–130000. On overflow it cancels and waits for that job, then continues the same open item in a fresh session, up to 5 sessions per item by default. It reports peak context and cumulative input, cache creation and output usage (cache reads are printed, not counted) per session. Brain dispatch timeout is 3600 seconds; holds `.backlog/loop.lock`, atomically records the current item, token, launch count, session count and job in `.backlog/loop.json`, and resumes a saved job |
| `bl next`, `bl show`, `bl packet <id> --role checks|producer|verifier`, `bl status`, `bl check [--views]`, `bl render`, `bl schema [--json]` | no | reads; `packet` prints an item's brief, notes, cited rules and scope, plus claims for a verifier; `render` only rewrites the views, `schema` is the single source for the fields |
| `bl audit [--since 2d]` | report only | arithmetic over the ledger and `run.log` |

Unused so far and kept out of the default flow: `bl second-check` (a second judge's command for one row) and `bl signoff` (a human sign-off that permits a judge-only list); `bl schema` lists their flags.

`bl next` names the item in flight; otherwise the first eligible item of tier 1 (the bumps, newest first), then tier 2 (the latest ranking), then tier 3 (everything else: the item the most open items wait on first, then filing order). A parked, closed, dropped or blocked item is never named, whatever its tier. Bumps and unbumps record `by: human`, a ranking `by: loop`.
