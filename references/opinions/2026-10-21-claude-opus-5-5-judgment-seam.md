# Claude Opus 5.5 — the judgment seam: is the anchor/inference split real, and what actually stops a lazy verifier?

- **Date:** 2026-10-21
- **Model:** `claude-opus-5-5`, via `claude -p --model claude-opus-5-5 --tools "" --permission-mode plan` (Claude Code 2.1.302)
- **Prompt used:** [`2026-10-21-claude-opus-5-5-judgment-prompt.md`](2026-10-21-claude-opus-5-5-judgment-prompt.md) in this folder
- **Relationship to the first opinion:** same model, second pass. It was given the first opinion back and asked to
  revise it. It corrected itself twice. Both are kept.
- **Status:** an opinion. Not implemented, not measured.

## The question

The first opinion (see `2026-10-21-claude-opus-cost-cutting.md`) proposed that the binary execute the check list and
compute the verdict, with judgment checks marked `kind: judgment`. The author pushed back on exactly that part: a
verifier that rubber-stamps judgment rows turns the loop into a formality, and the author's proposed fix was to split
each judgment row into an **anchor** (a cited line the binary can verify) and an **inference** (the argument drawn from
it). This second pass asks whether that split is real or theater, and what actually stops a lazy verifier.

## The answer, verbatim

**Verdict:** the anchor/inference split is real but narrow. It stops made-up evidence, not wrong reasoning. What actually stops a lazy verifier is **writing each judgment row's fail condition before any claims are seen, plus requiring every diff hunk to be cited, plus planting known defects to catch a verifier that passes everything**.

### 1. Anchor/inference: real, if the binary checks three things

The binary checks:
- **The quote is really there.** The cited file:line contains the quoted string *in the tree hash recorded for the round* (#11), not just the working copy.
- **It cites the change, not the old code.** The anchor sits in a diff hunk or a scope file. A line that hasn't changed since the before-state can't anchor "the change does X".
- **Every hunk is accounted for.** Each diff hunk is cited by at least one judgment row. An uncited hunk means the round is refused.

What that rules out:

| Rules out | Does not rule out |
|---|---|
| Invented quotes, wrong line numbers | Correct quote, wrong conclusion |
| Citing code from the before-state | Picking the one good line and ignoring the bad one (partly covered by hunk coverage) |
| Ignoring hunks ("looks fine") | "This hunk is needed" said without thinking |

It becomes theater when the anchor just repeats the mechanical check. The fix is that verifier-1 writes each judgment row's **fail condition** before claims exist. Verifier-2 then only fills in anchor, observation and verdict against that fixed condition.

```
row (frozen before claims): id | criterion | FAIL if <observable condition> | why not mechanical
row (filled after claims):  anchor file:line "quote" @tree | inference | verdict
bl checks: quote present @tree, anchor in diff, every hunk cited, row ids = frozen ids
```

- **Accept:** `J3: the refusal message is actionable. FAIL if the message lacks either the path or a runnable next command.` Anchor `main.go:412 "refused: path outside scope: paths.go; run: bl rescope A1 --add paths.go"`. The inference has a fixed condition to meet.
- **Reject:** `J5: the docs are accurate.` Anchor `README.md:88 "bl close checks the tree"`. That proves the docs mention it, not that it's true. Rule: a docs-truth row needs **two anchors**, the doc line and the code that does it (e.g. the tree comparison in `bl close`). A single-anchor docs row is refused.
- **Reject:** `J6: the fix addresses the cause, not the symptom.` Anchor is the changed line. That restates the diff. Most "cause vs symptom" rows are really mechanical checks verifier-1 didn't write: a variant input that hits the cause by a different path, written before claims, failing on the before-tree and passing on the after-tree. Rule: every judgment row must say **why it can't be mechanical**. If that reason is weak, it gets converted.

### 2. The verdict rule: take (c), add a cheap version of (a), keep (b) for escalation only, reject (d)

| Option | Take? | Why |
|---|---|---|
| (c) union: one failing row fails the round | **Yes, as the base** | The binary computes AND over mechanical results and judgment verdicts. Simple, and easy to audit. |
| (a) a second judge | **Yes, sampled and adversarial** | Prompt: "find why this row FAILs". Any disagreement = FAIL with cause `dispute`. A threshold over 2 voters means nothing. |
| (b) the human decides | **Only on escalation or high-impact labels** | As the normal path it becomes the bottleneck. |
| (d) the binary only enforces ordering | **No** | Then "a verdict must be backed by a command that ran" can't be enforced, because the verifier can fabricate the observed column. |

- "Never auto-PASS" is fine as a label. The stronger rule: **verifier-2 cannot add, drop or reword judgment rows.** Ids must match the frozen list exactly.
- **Accept:** J3 above, PASS, with the second judge agreeing.
- **Reject:** a row verifier-2 added after seeing claims ("J9: the producer's approach is clean"). That is the producer's framing coming back in. Refused by id mismatch.

### 3. Is `bl run` worth it? Yes, but for honesty, not cost

- **Correction:** I sold #5 as a cost cut. The real token saving is modest, because verifier-2 still exists for judgment rows. Its real value is that the observed column can't be fabricated.
- **Trust:** running agent-written commands in a worktree with a timeout is no worse than a verifier subagent with a shell, which already does the same.
- Keep it minimal: `{id, cmd, expect: exit|regex}`, `sh -c`, timeout, worktree, store output hash plus truncated text. No DSL.

Build order:

| # | Build | Folds in |
|---|---|---|
| 1 | `bl checks` freeze with fail conditions + `bl claims` refused before it + anchor validation + hunk coverage | #3 |
| 2 | `bl run` for mechanical rows, built-in hygiene rows first (stop on failure), tree pin | #2, #5, #11 |
| 3 | `bl bind` with targeted mutation: verifier-1 names an implementation line to mutate and the assertion that should catch it; the binary mutates, runs, and requires a failure | #6 |
| 4 | Planted-defect calibration (Q4) | new |

Drop:
- **#8 repo map:** drop entirely.
- **#10 same-failure escalation:** defer. The 3-round cap already covers it.
- **#2 as a separate command:** fold it into `bl run`.

### 4. Cheaper honesty: check the judge, not each verdict

- **Required citation:** necessary, cheap (roughly 50 lines in `bl`), not sufficient. Build it.
- **Cheaper second model:** only with an adversarial prompt. With the same model family and prompt, errors are correlated, so you pay for agreement and learn nothing.
- **Planted defects (the real answer to the lazy-verifier worry):** on about 1 in N items, `bl` builds a throwaway variant of the diff with a known bug. Examples: revert one hunk, or replace an error message with `"error"`. Verifier-2 runs blind on it. A PASS on the planted variant fails the calibration check: every judgment PASS from that model/prompt setup since the last good calibration gets flagged. This measures the rubber-stamp rate instead of hoping, and a third party can recompute that number from the ledger.
  - Accept as a planted defect: one in-scope hunk deleted. J-rows citing that hunk must FAIL.
  - Reject as a planted defect: a syntax error. The mechanical rows catch it, so it says nothing about judgment.
- **Disagreeing with the author:** "a second model is the only real check on an argument" is wrong. Measuring the judge against known answers is cheaper and catches laziness systematically.

Known vs unknown:
- **Known:** anchors stop fabrication; hunk coverage stops skipped hunks.
- **Unknown:** the real rubber-stamp rate. Only the planted defects measure it.

### 5. Corrections to my earlier answer

- **#5** is about honesty, not cost (see Q3).
- **#3** was incomplete. Freezing check *commands* isn't enough; judgment rows also need frozen *fail conditions* and a "why not mechanical" field.
- **#9** said "cheap model for fix rows". That still holds, but a cheap model as a *second opinion* only works with an adversarial prompt.
- **#4** (verifier-2 continues verifier-1's session) still holds. The risk is the producer's framing, not the verifier judging its own criteria, and frozen ids keep it honest.
- **Missing earlier:** hunk coverage and planted-defect calibration. Those are what actually answer "nothing prevents a lazy verifier".

---

**Next concrete step:** build `bl checks` so that judgment rows must carry `FAIL if …` and `why not mechanical`, and make `bl round` refuse unless anchors validate against the round's tree hash and every diff hunk is cited.

**Won't trade away:** the full check list, including judgment fail conditions, is hashed into the ledger before any producer claim is recorded, and the binary refuses claims until it is.
