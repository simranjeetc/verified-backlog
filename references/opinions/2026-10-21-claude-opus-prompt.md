# The brief given to Claude Opus on 2026-10-21

Kept alongside the answer (`2026-10-21-claude-opus-cost-cutting.md`) so the opinion can be re-read with the exact
question it answered. No tools were available to the model; it answered from this text alone.

---

You are giving an independent engineering opinion to the author of a skill called `verified-backlog`. Do NOT use any
tools — the context you need is below. Answer in plain prose with concrete, specific proposals. Be opinionated, and
say what you would NOT do.

## What the skill is

A Git-repo-based work loop for a coding agent, designed so that a work item's "passed" status is an artifact that the
worker cannot write itself.

- `bl` is a small Go binary (stdlib only, ~5ms/call) that owns an append-only ledger `.backlog/ledger.jsonl` and
  renders two human views, `TODO.md` and `PROGRESS.md` (hand-edits are caught by a hash check). It also stores sha256
  of every evidence file and refuses illegal moves with exit code 2.
- The agent that the human talks to is the "orchestrator". It runs the skill's playbook.

## The loop (one item at a time)

1. `bl file --id A1 --text "..."` — add item (optionally `--after B2` for a dependency).
2. `bl open A1` — locks the item; exactly one item may be in flight (a second `open` is refused). The orchestrator
   records a before-state: `git status --porcelain`, `git diff` into `.backlog/evidence/`, plus hashes of already-dirty
   files, and names the files the item may touch.
3. Dispatch a **producer** subagent in a FRESH session: the item text verbatim + the rules (don't commit, don't edit
   outside the named files, don't fix anything outside the item, don't declare it passed, don't weaken tests) + what to
   produce. It edits files and reports claims: files changed, the property implemented, the commands that prove it, the
   test command and its result.
4. Dispatch a **verifier** subagent in a FRESH session, in TWO dispatches:
   - Dispatch 1: intent only (the item text). It writes the CHECK LIST it thinks the item needs — each check is a
     command with an expected result — before it sees any claim. Rationale: one prompt carrying both would let the
     producer's framing become the verifier's rubric.
   - Dispatch 2: the producer's claims to falsify, plus extra cases the orchestrator requires, plus: test binding
     (mutate the code in a throwaway copy and confirm the new test fails), the repo's documented test command, and
     hygiene (nothing changed outside the item's file list, no untracked file left, no diff against the before-state).
     Output is a table `| check | command | expected | observed |` and a `Verdict: PASS|FAIL` line, written to
     `.backlog/evidence/<id>-verify-r<N>.md`.
5. `bl round A1 --verdict PASS|FAIL [--cause code|harness] --rows <file>` — the orchestrator sets `--cause` itself
   (code = the item failed the checks; harness = the verifier could not run). The rows are written to
   `.backlog/evidence/<id>-r<N>.txt` and hashed into the ledger. Empty rows are refused. Cap: 3 code FAIL rounds; a 4th
   is refused and the item escalates to a human. A `harness` failure does not consume a round.
6. On FAIL: dispatch a NEW producer whose brief is only the verifier's failing rows, then a FRESH verifier (never the
   previous verifier's session). A fix round that also refactors is a fix round that cannot be verified.
7. `bl close A1 --commit <sha>` — refused unless the last round is PASS, the repo is a git repo, the commit resolves,
   and the working tree is clean. Commit happens first, with explicit paths (never `git add -A`).
8. Findings outside the item's blast radius are filed as new items (`bl file --source verifier:A1/r2`), never fixed in
   place.

## Where the cost is

- Model tokens, not the binary. Each fresh session has to re-establish the repo context: the item's scope, the
  relevant files, the before-state, the conventions.
- Per item: producer (1 context) + verifier dispatch 1 (1) + verifier dispatch 2 (1) + one producer+verifier pair per
  fix round (2 each). So 3 contexts for a clean item, 5 for one fix round, 7 for two.
- A verifier dispatch that reads the whole repo is the most expensive single action; a fix round is the most expensive
  event because it re-reads everything and writes.
- One item in flight at a time for verification: parallel producers on disjoint files are allowed, but their items
  queue for verification, so the verifier is the throughput bottleneck.
- Stalls: blocked dependencies, dirty tree at close, cosmetic FAILs that burn a round, and a wrong premise that fails
  rounds 2 and 3 for the same reason.

## What must NOT be sacrificed

- The producer must not be able to record its own success.
- The verifier must not see the producer's plan before writing its own checks.
- A verdict must be backed by a command that ran; an empty table is refused.
- The evidence must be tamper-evident (hashes) and the views must be derived, not hand-edited.
- A wrong premise should be re-baselined, not dropped; out-of-scope findings get filed, not fixed in place.
- The loop must stay auditable: a third party should be able to re-derive every number.

## The question

How do we cut the cost of the expensive stages — especially the fresh-context rebuild at each stage — without weakening
the properties above? Consider at least:

- what can be made durable-on-disk once and reused by later sessions (an item brief artifact, a frozen scope file, a
  repo map, a check catalog) and what each costs to keep fresh;
- whether the two verifier dispatches can be merged, shortened, or replaced by a deterministic harness, and what
  independence guarantee is lost at each step;
- whether the verifier needs to read the whole repo at all, or only the item's file list plus a slice;
- cheaper models for the expensive-but-routine parts, and which parts must stay expensive;
- anything else that cuts contexts per item while keeping the record audit-worthy.

For each proposal: state the mechanism concretely, the saving, what guarantee it weakens or costs, and how it would be
enforced (prose in the playbook, or a change in the `bl` binary — the binary is where refusals live, so a proposal that
can be enforced by a refusal is worth noting). End with your single strongest recommendation and the one thing you
would explicitly refuse to trade away.
