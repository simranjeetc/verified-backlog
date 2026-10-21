# Claude Opus — cutting the fresh-context cost of the verified-backlog loop

- **Date:** 2026-10-21
- **Model:** `claude-opus-5-5` (invoked as the `opus` alias), via `claude -p --model opus --tools "" --permission-mode plan` (Claude Code 2.1.302)
- **Prompt used:** [`2026-10-21-claude-opus-prompt.md`](2026-10-21-claude-opus-prompt.md) in this folder
- **Status:** one independent opinion. No measurement, no implementation, nothing verified by the loop. Treat as a proposal set, not a verdict.

## Why this file exists

The loop's cost is dominated by model contexts, not by the binary. On 2026-10-21 the author asked a second model
family for an opinion on how to cut that cost without weakening the guarantees. The answer is kept verbatim so it can
be re-read, argued with, and cited later — it is the closest thing here to a reviewed external opinion.

## The brief it was given

A self-contained description of the skill: what `bl` is, the eight-step item loop, the producer/verifier split, the
two-dispatch rule, the refusal conditions, where the tokens go, and the list of properties that must not be
sacrificed. No repository access, no tools — the model answered from the brief alone.

## The opinion, verbatim

# Cutting `verified-backlog` cost without weakening its guarantees

My main advice: stop paying a model to run checks and copy their output. Let the model write the checks, and let `bl` run them, record what it saw and work out the verdict. Most of the per-item cost goes away, and the guarantees get stronger because the binary can refuse things it currently has to trust.

## Proposed flow

```
bl open A1 --scope f1,f2 ──► brief.md + before-tree (hashed)
        │                         │
        ▼                         ▼
 [producer]  ‖  [verifier-1: intent+brief] ──► bl checks A1 (frozen hash)
        │                                         │
        ▼                                         ▼
 bl claims A1 ──► bl precheck (hygiene/lint/scope; no round)
        │
        ▼
 [verifier-2: brief+diff+claims] ──► adds falsifiers ──► bl run A1
        │                                                  │
        ▼                                                  ▼
 bl round (verdict computed by binary, tree pinned) ──► bl close (tree == verified tree)
```

## Proposals

**1. Frozen item brief: `.backlog/items/A1/brief.md`**
- **Mechanism:** written once at `bl open`. It holds:
  - the item text verbatim
  - the scope file list
  - before-state hashes
  - the repo's test command
  - a short conventions excerpt
  - `HEAD` sha

  Its hash goes into the ledger.
- **Saving:** every later session reads one file instead of re-discovering the repo. This is the biggest cut to rebuild cost.
- **Guarantee lost:** none, as long as it holds only intent and facts. It must never contain plans or hints toward a solution, or the orchestrator's framing leaks into verifier-1.
- **Enforcement (binary):** `bl round` refuses if the brief hash changed since `open`. Changing scope requires `bl rescope`, which is logged.

**2. Deterministic hygiene and scope checks in `bl`**
- **Mechanism:** `bl open --scope` records the before-tree. `bl precheck A1` runs `git status --porcelain` and the diff against the before-state. It refuses on:
  - any file changed outside scope
  - any untracked file
  - any already-dirty file whose hash changed
- **Saving:** this whole section leaves the verifier prompt. Cosmetic and hygiene failures stop using up rounds.
- **Guarantee lost:** none. A refusal is stronger than an LLM reading `git status`.
- **Enforcement (binary):** `bl round` refuses unless a `precheck` PASS exists at the current tree hash. Precheck failures are logged with cause `precheck` and don't count toward the 3-round cap.

**3. Freeze the check list with an ordering rule**
- **Mechanism:** verifier-1 output is stored with `bl checks A1 --file`. The producer's claims are stored with `bl claims A1 --file`.
- **Enforcement (binary):** `bl claims` refuses if no checks are recorded yet. The checks file is append-only; `bl round` refuses if any existing row was edited or removed.
- **Saving:**
  - Verifier-1 only needs intent, so it can run in parallel with the producer (saves wall time).
  - Fix rounds reuse the frozen checks instead of writing new ones.
- **Guarantee:** stronger. "Verifier wrote its checks blind" becomes a ledger fact that can be re-derived, not a rule written in the playbook.

**4. Run verifier-2 as a continuation of verifier-1's session**
- **Mechanism:** dispatch 2 resumes verifier-1's session (only if your harness supports resuming a session). It only resumes after `bl checks` has hashed the list.
- **Saving:** one fewer context per item (3 → 2).
- **Guarantee lost:** essentially none. The reason for two dispatches was that the rubric must be written before the claims are seen, and point 3 enforces that order. That the verifier remembers its own rubric is the point.
- **Fallback:** if sessions can't be resumed, verifier-2 starts from brief + frozen checks + diff, which is still cheap.
- **What I would not do:** merge both into one prompt. That is exactly the framing leak the design prevents.

**5. `bl run A1`: the binary executes the checks**
- **Mechanism:** each check is `{id, cmd, expect: exit/regex}`. The binary:
  - runs each one with a timeout
  - writes `observed` (exit code, stdout hash, truncated output)
  - works out PASS or FAIL itself

  `bl round --verdict` becomes a cross-check: the binary refuses if the orchestrator's verdict disagrees with the computed one.
- **Saving:** the verifier no longer runs commands or copies output, which is roughly half its tokens. Fix-round verification becomes mostly a re-run.
- **Guarantee:** stronger. Today a verifier could make up an `observed` cell; this closes that gap.
- **Cost:**
  - Some checks are judgment calls ("the error message is clear"). Mark them `kind: judgment`; they carry the verifier's own text and are labeled that way in the record.
  - The binary runs commands the agent wrote. That is the same trust level the agent already has, but use a worktree.

**6. Mechanical test-binding check**
- **Mechanism:** `bl bind A1`:
  1. creates a `git worktree` at the before-tree
  2. adds only the producer's test files
  3. runs the test command, which must fail
  4. runs it again on the full diff, which must pass
- **Saving:** no LLM-designed mutation needed for the basic case. The verifier only adds targeted mutations where reverting the implementation is too coarse.
- **Guarantee lost:** none for "this test exercises the new code". A weaker form of "this test catches every plausible bug", which the LLM-written mutations still cover.
- **Enforcement (binary):** `bl round PASS` refuses without a `bind` PASS when the item adds tests.

**7. The verifier reads a slice, not the whole repo**
- **Mechanism:** verifier-2 gets:
  - brief
  - frozen checks
  - raw `git diff` against the before-tree (not the producer's prose summary)
  - the scope files
  - their direct callers (from a `grep`/`go list`-style query the orchestrator runs once and stores in the brief)
- **Saving:** this was the most expensive single action; it becomes a bounded read.
- **Guarantee lost:** effects on distant code. The backstop is the repo's full test command as a mandatory frozen check, which the binary runs.
- **Enforcement:** the playbook sets the slice. The binary enforces the backstop: `bl checks` refuses a list without the repo test command row.

**8. Repo map: keep it small or skip it**
- **Mechanism:** `.backlog/map.md` stamped with the `HEAD` sha. It holds the package/dir list, test command and conventions only.
- **Cost:** it goes stale on every commit. `bl open` regenerates it when the sha differs (a script, not a model).
- **What I would not do:** keep a rich, model-written repo summary. Keeping it fresh costs more than the rebuild it saves, and a stale map quietly misleads the verifier.

**9. Cheaper models: cheapen only the side whose errors get caught**

| Role | Model | Why |
|---|---|---|
| Producer, round 1 | strong | a weak producer means more fix rounds, the costliest event |
| Producer, fix round on mechanical rows (lint, rename, missing case) | cheap | the verifier catches its mistakes |
| Verifier-1 (writing checks) | strong | a weak rubric is a silent failure that no later stage catches |
| Verifier-2 (falsifying claims) | strong, but short context | same reason |
| Hygiene, running checks, copying output, map | no model (`bl`) | deterministic |
| Orchestrator `--cause` triage | mid | cross-checked by the binary-computed verdict |

**10. Same failure twice escalates instead of burning a round**
- **Mechanism:** check ids are stable (point 3).
- **Enforcement (binary):** if round N fails on the same check ids as round N-1, `bl round` refuses another code round. It requires `bl rebaseline A1` (new brief, premise logged) or escalation to a human.
- **Saving:** removes the "rounds 2 and 3 fail for the same wrong premise" stall, which is a whole fix round.
- **Guarantee:** stronger. It enforces your rule that a wrong premise is re-baselined, not dropped.

**11. Pin the verified tree**
- **Mechanism:** each round records the `git write-tree` hash of the state it verified.
- **Enforcement (binary):** `bl close` refuses unless the commit's tree equals the last PASS round's tree.
- **Saving:** none directly. It is what makes all the caching above safe, because every reused artifact is tied to an exact tree.

## What I would not do

- Let the producer write checks, the brief, or anything the verifier reads besides the raw diff and its claims.
- Give the verifier the producer's prose summary instead of the raw diff.
- Carry a verifier session over into the next round.
- Reuse a verdict after the tree changes.
- Batch several items into one verifier to save contexts. Failures get tangled and rows can no longer be traced to an item.
- Drop the full test suite to save time.
- Relax the one-item-in-flight rule before tree pinning exists. After pinning, verifying disjoint items in parallel worktrees becomes safe to try.

## Strongest recommendation

Points 3 + 5 + 11 together: frozen, hashed checks, executed by `bl`, with the verdict computed by the binary and tied to a tree hash. The verifier shrinks to a short check-writing job on a slice, verifier-2 can continue the same session, and fix rounds become mostly re-runs. Every number in the record comes from a command the binary ran itself.

## What I would refuse to trade away

The check list must be written and hashed into the ledger before the verifier sees any producer claim, and the binary must refuse to record claims until it is. Every other cost cut above can be re-checked afterwards; if this one breaks, the producer's framing quietly becomes the verifier's rubric, and nothing in the record would show it.

---

## Where this is used

- `references/second-harness.md` — how a verdict gets out of another model family.
- `SKILL.md` — the non-negotiables; the “refuse to trade away” line above is the same invariant, stated by a second family.
