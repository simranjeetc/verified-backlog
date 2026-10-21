# Glossary — verified-backlog

Terms the skill already uses, in one place, with where each one lives. The second-opinion vocabulary
(`references/opinions/`) sometimes renames these — those are marked **alias**.

| term | what it means | where it lives | who may write it |
|---|---|---|---|
| **item** | one unit of backlog work, e.g. “add `--scope` to `bl open`” | ledger event (`bl file`) | human, or the skill during import |
| **BRIEF.md** | authored, never parsed: intent, constraints, what “done” means, and whether the backlog may push | repo root | human only |
| **brief** | the per-item, machine-written snapshot: item text, scope list, before-state hashes, test command, HEAD | proposed `.backlog/items/<id>/brief.md` | `bl`, at open — **proposed, not implemented** |
| **scope** | the files an item may write | brief / producer prompt | `bl` at open; changed only by a logged rescope |
| **before-state** | `git status --porcelain`, the diff, and hashes of already-dirty files, captured before the producer works | `.backlog/evidence/` | `bl` / the orchestrator at open |
| **producer** | the subagent that edits files and reports what it did | fresh session | — |
| **claim** | the producer's assertion that a property holds, each backed by a command it ran | the producer's report | producer only — it can never be the verdict |
| **check list** | the verifier's list of checks, each a command with an expected result, written **before** it sees any claim | the verifier's output, then `.backlog/items/<id>/checks.txt` | verifier only — **proposed, not implemented** |
| **rubric** | *alias* of “check list”, from the second opinion's vocabulary | — | — |
| **check** | one row of the check list: `{id, command, expected}` | check list | verifier |
| **row** | one line of the recorded evidence table: check, command, expected, observed | `.backlog/evidence/<id>-r<N>.txt` | verifier (hashed by `bl`) |
| **observed** | what actually happened when the command ran | row | the runner — today the verifier, **proposed**: `bl run` |
| **mechanical check** | a check whose expectation is machine-decidable: exit code, pattern match, file exists, hash equality | check list | verifier proposes; `bl` decides — **proposed** |
| **judgment check** | a check no binary can decide (“the message names the offending file”), marked `kind: judgment` | check list | verifier argues; never auto-PASS — **proposed** |
| **round** | one recorded verification attempt: a verdict plus the rows that back it | ledger event (`bl round`) | the orchestrator records it; the binary validates it |
| **verdict** | PASS or FAIL for an item | ledger event | the binary, from evidence — never the producer |
| **cause** | why a round failed: `code` (the item failed its checks) or `harness` (the verifier could not run) | ledger event | the orchestrator sets it, from what it observed |
| **round cap** | 3 code failures per item; a 4th code round is refused and the item escalates | enforced in `bl` | — |
| **fix round** | a new producer, briefed only with the failing rows, followed by a fresh verifier | producer session | — |
| **re-baseline** | correcting an item whose premise turned out false: new premise, logged — never silently dropped | `bl rebaseline` | **proposed**; today it is `bl note` + a new brief |
| **filed** | a finding outside the current item's blast radius, recorded as its own item instead of fixed in place | `bl file --source verifier:<id>/r<N>` | the orchestrator |
| **evidence** | the files under `.backlog/evidence/`: rows, hashes, before-state | `.backlog/evidence/` | `bl` writes, hashes and re-checks |
| **tree pin** | the `git write-tree` hash of the exact state a round verified | ledger event | **proposed** |
| **harness failure** | the verifier could not run (timeout, missing tool, killed session) — does not consume a round | `bl round --cause harness` | the orchestrator |
| **lane** | a verification depth (light / full) — a *proposal*, not part of the skill today | — | — |
| **precheck** | deterministic scope and hygiene check run by the binary before a round | — | **proposed** |
| **in flight** | the one item that may be open at a time; a second `bl open` is refused | ledger | `bl` enforces |
| **fail condition** | the observable condition, frozen with the check list, that makes a judgment row fail | check list | verifier-1, before any claim — **proposed** |
| **why not mechanical** | the required reason a judgment row cannot be a machine check; a weak reason converts the row into a mechanical one | check list | verifier-1 — **proposed** |
| **anchor** | the cited line a judgment row rests on — `file:line "quote"` at the round's tree hash | the filled row | verifier-2; checked by `bl` — **proposed** |
| **inference** | the argument drawn from the anchor; the part only a model can argue | the filled row | verifier-2 — **proposed** |
| **hunk coverage** | every diff hunk must be cited by at least one judgment row; an uncited hunk refuses the round | — | `bl` enforces — **proposed** |
| **planted defect** | a known bug deliberately introduced into a throwaway variant of the diff, to measure whether the verifier rubber-stamps | calibration run | `bl` builds it; the verifier sees it blind — **proposed** |
| **second judge** | an adversarial second pass asked to *find why a row fails*; disagreement is a FAIL with cause `dispute` | — | a different model or harness — **proposed** |
| **dispute** | a `bl round --cause` value for a round where the two judges disagreed | ledger event | **proposed** |
| **decider** | who reads a row's output and decides it: `bl` (compares observed to expected) or `judge` (applies the frozen fail condition) | check list | verifier-1, at authoring time — **proposed** |
| **severity** | `blocking` (a failing row fails the round) or `advisory` (a failing row is recorded and filed) | check list | verifier-1, before any claim — **proposed** |
| **brief_ref** | the clause of the frozen brief a blocking row cites; a blocking row with no `brief_ref` is refused | check list → brief | **proposed** |
| **advisory item** | a finding recorded as its own backlog item because it did not fail the round | `bl file` | orchestrator |
| **reclassification** | a `judge → bl` amendment made after the freeze; `bl → judge` is refused once claims exist | ledger event | **proposed** |

**Two words to watch.** *Claim* and *rubric* are the ones that get used loosely. A **claim** is the producer's
assertion — it is an input to verification, never its output. The **check list** (Opus: “rubric”) is the verifier's
own list, written before it sees any claim; freezing it in the ledger is what makes “the verifier checked blind”
provable after the fact instead of merely promised.
