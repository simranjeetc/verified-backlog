# Claude Opus 5.5 — check classification: is every check mechanical, and what happens to the ones that are not?

- **Date:** 2026-10-21
- **Model:** `claude-opus-5-5`, via `claude -p --model claude-opus-5-5 --tools "" --permission-mode plan` (Claude Code 2.1.302)
- **Prompt used:** [`2026-10-21-claude-opus-5-5-check-classification-prompt.md`](2026-10-21-claude-opus-5-5-check-classification-prompt.md) in this folder
- **Series:** third question. It was given its own first two answers and asked to revise them.
- **Status:** an opinion. Not implemented, not measured.

## The question

The author noticed that the whole design rests on an unstated premise — that a check can be mechanical — and asked for
a strategy for the checks that cannot be: the verifier should be able to *categorise* the checks it writes, and the
non-mechanical ones need a handling strategy.

## The answer, verbatim

Your answer mostly holds, with three fixes: severity has to be tied to the brief rather than set by a default, verdict-time findings must not go to advisory, and I'm revising my earlier hunk-coverage rule.

**Reframe first.** No check is "non-mechanical". Every row runs a command. What differs is who reads the output and decides pass/fail. So `kind` should be `decider: bl | judge`.

```
 row ──► anchor cmd runs (always) ──► output hashed by bl
                     │
        decider=bl ──┴── decider=judge
           │                  │
   bl compares observed   judge applies the frozen fail_if
   to expected            to the hashed output
           └───────┬──────────┘
          verdict = AND(blocking rows)   ◄── bl computes this, not the model
```

### 1. Classify at authoring time: yes

| Classify at… | What goes wrong |
|---|---|
| Verdict time (verifier-2) | It reclassifies whatever is awkward as `judge` after seeing the claim, and the fail condition gets written to fit the result. |
| The binary | `bl` can't read meaning. Text heuristics get gamed within a week. |
| Authoring time (verifier-1) | Only risk: classes the author didn't think of. Calibration covers that. |

- One amendment is allowed after the freeze: `judge` → `bl` (stricter), recorded in the ledger. Never `bl` → `judge` once claims exist.
- `bl` checks shape only: `decider` is present; `bl` rows have `cmd` and `expect`; `judge` rows have `cmd`, `fail_if` and `why_not_mech`.

### 2. Blocking/advisory: right idea, wrong default

- Frozen severity on pre-written rows isn't where problems get buried. Verifier-1 sets it before any claim exists.
- **The real dumping ground is new findings at verdict time.** Verifier-2 spots a real bug that no row covers and files it as advisory.
- **Fix:** a verdict-time finding verifier-2 calls serious makes the round `DISPUTE`, not PASS. That escalates to the second judge, then the human. It never goes straight to advisory.
- **Severity rule (the binary can check it):** a row is `blocking` if and only if it cites a clause ID in the frozen brief, e.g. `brief_ref: B2`, and `bl` confirms B2 exists. Rows with no citation are forced to advisory and filed as items automatically.
- **No ratio target.** A quota gets gamed. Severity follows the brief, so a sharp brief produces blocking rows.
- **Guards against the dumping ground:**
  - Every advisory item must be closed with a reason or promoted within K items.
  - The ledger tracks advisory items that later caused a blocking failure or a bug item, i.e. severity that was set too low.
  - Planted defects include semantic, blocking-grade bugs. Reporting a plant as advisory counts as a calibration failure.
- Advisory failures never count toward the 3-failure cap. Agreed: cosmetic arguments must not use up rounds.

### 3. Misclassified rows: fail only when the verdict changes

- Don't fail the producer. They didn't write the row; it's a verifier quality defect.
- **Main detector: the second judge, given a constructive task:** "write a command that decides this row." `bl` runs that command.
  - Its result disagrees with the judge's verdict → hard fail of the round.
  - It agrees → record the reclassification and count it against the author. The round stands.
- **Planted defects** detect it statistically, after the fact. That feeds audits, not the current verdict.
- **Binary:** lint flag only. Example: a `judge` row whose `fail_if` is "exit non-zero" or "contains X" gets flagged, never failed.
- **Human:** reads the clustered `why_not_mech` strings at audit time.

### 4. "High judgment share ⇒ vague brief": weak as stated

| Cause of a high judgment share | Right response |
|---|---|
| Vague brief | Re-baseline the brief |
| No test harness | File an infrastructure item, not a brief rewrite |
| Domain is semantic (docs, copy, naming) | Expected; check the mechanical outskirts (links, examples run) |
| Lazy author (convertible rows) | Calibration and second-judge conversion |

- **Better signal:** `judge` rows that can't cite a brief clause. That points at the brief directly.
- **Hard rule instead of a threshold:** every item needs at least one `bl`-decided blocking row, or a human signs off before close. This holds for docs too: links resolve, examples run.
- **Soft flag:** `judge` rows above ~1/3 of an item's blocking rows → read the `why_not_mech` cluster before arguing about the verdict. That 1/3 is my starting point; there's no data behind it yet.

### 5. Where this changes my earlier answers

- **Union rule:** "one failing row fails the round" becomes "one failing *blocking* row fails it". Advisory failures are recorded and filed. Verdict-time new findings become `DISPUTE`.
- **Hunk coverage, I was wrong:** I said every hunk must be cited by a *judgment* row. That forces judgment rows and works against the conversion pressure. New rule: every hunk is covered by at least one *blocking* row of either decider; advisory rows don't count.
- **`--verdict`:** `bl round` should derive the verdict and refuse a supplied value that doesn't match. This sharpens my first answer.
- **Planted defects:** they now calibrate severity as well as detection.
- **Kept:** frozen `fail_if`, `why_not_mech`, the sampled second judge, the human for escalation only.

### Worked examples

| Item | Row | Severity | What `bl` checks |
|---|---|---|---|
| Brief B2: "missing config → error names the path searched and how to create it" | `bl`: stderr contains the path. `judge`: FAIL if stderr has no runnable command or doc URL for creating the config | **Blocking** (cites B2) | Anchor command ran, stderr hashed, B2 exists, `fail_if` not empty, a sibling `bl` row is present |
| Brief B1: "migration is safe with v1 readers still running" | `judge`: FAIL if the migration drops or renames a column that v1 code reads (anchor: grep of v1 reads + migration file; dynamic SQL makes it judgment) | **Blocking** (cites B1) | Both anchors hashed, the migration hunk is covered, B1 exists |
| Any item: `reconcile()` grew to 140 lines | `judge`: FAIL if a function exceeds ~80 lines with mixed responsibilities | **Advisory** (no brief clause) | No `brief_ref` → forced to advisory, filed item ID exists, doesn't affect the verdict |
| New flag `--dryrun` where the repo uses `--dry-run` | `judge`: naming consistency | **Advisory** unless the brief says "follow CLI conventions", in which case blocking and convertible via a grep of existing flags | `brief_ref` decides severity; if blocking, the second judge should convert it to a `bl` row |

### What I would NOT do

- Ratio quotas for judgment share or advisory share.
- Let `bl` classify rows by reading text.
- Let verifier-2 change severity or decider.
- Accept model confidence scores as evidence.
- Let a `judge` row pass without a hashed anchor.

**Next step:** extend the `bl checks` schema to `{id, decider, severity, brief_ref, cmd, expect | fail_if + why_not_mech}`. Make `bl` refuse a blocking row with no `brief_ref` and refuse a check list recorded after `bl claims`. Make `bl round` compute verdict = AND(blocking rows).

**Won't trade:** the check list (fail conditions, severity and `brief_ref` included) is hashed into the ledger before any claim exists. Every other guard depends on that ordering.
