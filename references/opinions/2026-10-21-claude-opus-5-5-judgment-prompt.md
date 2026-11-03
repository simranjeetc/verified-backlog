You are giving a second, independent engineering opinion to the author of a skill called `verified-backlog`. You already
answered a related question once (cost-cutting proposals); this time you are asked about a specific weak point that the
author found while reacting to your first answer. Do NOT use any tools — the context is below. Be opinionated, concrete,
and say what you would NOT do. Where you disagree with your earlier answer, say so explicitly.

## The skill

A Git-repo-based work loop for a coding agent, so that a work item's "passed" status is an artifact the worker cannot
write itself.

- `bl` is a small Go binary (stdlib only, ~5 ms/call) owning an append-only ledger `.backlog/ledger.jsonl`, rendering
  two human views (`TODO.md`, `PROGRESS.md`), storing a sha256 of every evidence file, and refusing illegal moves with
  exit code 2.
- The agent the human talks to is the "orchestrator". It runs the skill's playbook.

## The loop (one item at a time)

1. `bl file --id A1 --text "..."` — add an item.
2. `bl open A1` — locks the item; exactly one item may be in flight. The orchestrator records a before-state:
   `git status --porcelain`, `git diff` into `.backlog/evidence/`, plus hashes of already-dirty files, and names the
   files the item may touch (the "scope").
3. Dispatch a **producer** subagent in a FRESH session: the item text verbatim + the rules (do not commit, do not edit
   outside the named files, do not fix anything outside the item, do not declare it passed, do not weaken tests) + what
   to produce. It edits files and reports **claims**: files changed, the property implemented, the commands that prove
   it, the repo's test command and its result. A claim is an assertion backed by a command — always an input to
   verification, never the output.
4. Dispatch a **verifier** subagent in a FRESH session, in TWO dispatches:
   - Dispatch 1: intent only (the item text). It writes the **check list** it thinks the item needs — each check is a
     command with an expected result — before it sees any claim. Rationale: one prompt carrying both would let the
     producer's framing become the verifier's rubric.
   - Dispatch 2: the producer's claims to falsify, plus extra cases the orchestrator requires, plus: test binding
     (mutate the code in a throwaway copy, confirm the new test fails), the repo's documented test command, and hygiene
     (nothing changed outside the item's file list, no untracked file left, no diff against the before-state). Output
     is a table `| check | command | expected | observed |` and a `Verdict: PASS|FAIL` line.
5. `bl round A1 --verdict PASS|FAIL [--cause code|harness] --rows <file>` — the orchestrator sets `--cause` itself
   (code = the item failed its checks; harness = the verifier could not run). Rows are written to
   `.backlog/evidence/<id>-r<N>.txt` and hashed into the ledger. Empty rows are refused. Cap: 3 code FAIL rounds; a
   4th is refused and the item escalates to a human. A `harness` failure does not consume a round.
6. On FAIL: dispatch a NEW producer whose brief is only the verifier's failing rows, then a FRESH verifier (never the
   previous verifier's session).
7. `bl close A1 --commit <sha>` — refused unless the last round is PASS, the repo is a git repo, the commit resolves,
   and the working tree is clean. Commit first, with explicit paths.
8. Findings outside the item's blast radius are filed as new items (`bl file --source verifier:A1/r2`), never fixed in
   place.

## Where the cost is

Model tokens, not the binary. Each fresh session re-establishes repo context: the item's scope, the relevant files, the
before-state, the conventions. Per item: producer (1 context) + verifier dispatch 1 (1) + verifier dispatch 2 (1) + one
producer/verifier pair per fix round (2 each) — 3 contexts for a clean item, 5 for one fix round. A verifier dispatch
that reads the whole repo is the most expensive single action. One item in verification at a time: parallel producers on
disjoint files are allowed, but their items queue for verification, so the verifier is the throughput bottleneck.

## What must NOT be sacrificed

- The producer must not be able to record its own success.
- The verifier must not see the producer's plan before writing its own checks.
- A verdict must be backed by a command that ran; an empty table is refused.
- Evidence must be tamper-evident (hashes); views must be derived, not hand-edited.
- A wrong premise should be re-baselined, not dropped; out-of-scope findings get filed, not fixed in place.
- The loop must stay auditable: a third party can re-derive every number.

## Your earlier answer (abridged — you may now revise it)

Thesis: stop paying a model to run checks and copy their output. Let the model write the checks, let `bl` run them,
record what it saw, and compute the verdict. Your proposals, with the enforcement you named:

1. **Frozen item brief** — `.backlog/items/A1/brief.md` written once at `bl open`: item text, scope, before-state
   hashes, test command, conventions, HEAD; hash in the ledger; `bl round` refuses if the brief hash changed; scope
   changes need a logged `bl rescope`.
2. **Deterministic hygiene in `bl`** — `bl precheck A1` runs the diff against the before-state; refuses out-of-scope
   edits, untracked files, changed already-dirty files; `bl round` refuses without a precheck PASS; precheck failures
   carry their own cause and do not consume a round.
3. **Frozen check list with an ordering rule** — `bl checks A1 --file` stores verifier-1's list; `bl claims A1 --file`
   refuses if no checks are recorded; rows are append-only; fix rounds reuse the frozen list; verifier-1 can run in
   parallel with the producer.
4. **Verifier-2 continues verifier-1's session** — one fewer context per item (3 → 2), only after the list is hashed.
   Never merge both into one prompt.
5. **`bl run A1`: the binary executes the checks** — each check is `{id, cmd, expect: exit/regex}`; the binary runs each
   with a timeout, records observed (exit code, stdout hash, truncated output), and computes PASS/FAIL; `bl round
   --verdict` becomes a cross-check that refuses a disagreement. Judgment checks are marked `kind: judgment` and carry
   the verifier's own text. Use a worktree, since the binary runs agent-written commands.
6. **Mechanical test-binding** — `bl bind A1`: a git worktree at the before-tree, add only the producer's test files, the
   test must fail; then on the full diff it must pass. `bl round PASS` refuses without a `bind` PASS when the item adds
   tests.
7. **The verifier reads a slice, not the whole repo** — brief, frozen checks, raw diff, scope files, direct callers;
   backstop is the repo's full test command as a mandatory frozen check the binary runs.
8. **Repo map: keep it small or skip it** — a rich model-written repo summary costs more than it saves and goes stale.
9. **Cheaper models by role** — strong for producer round 1 and for writing the check list; cheap for mechanical fix
   rows; no model at all for hygiene, map and check execution.
10. **Same failure twice escalates** — stable check ids; a round failing on the same ids as the previous one is refused;
    requires `bl rebaseline` (new premise, logged) or a human.
11. **Pin the verified tree** — each round records the `git write-tree` hash; `bl close` refuses unless the commit's tree
    equals the last PASS round's tree.

Your strongest recommendation was 3 + 5 + 11 together. The thing you refused to trade away: the check list must be
written and hashed into the ledger before the verifier sees any producer claim, and the binary must refuse to record
claims until it is.

## What the author then pushed back on — the question for you now

The author accepted the cost argument but is not convinced by the *judgment* part, and asked for examples rather than
reassurance. The current design of a judgment row:

- A judgment row is one no binary can decide. Examples the author worked out: "the refusal message names the file and
  says what to do next" (binary can only check that the filename appears in stderr); "the new flag matches the CLI's
  conventions" (binary can check it parses and is listed in --help); "the change does only what the item asked" (binary
  can check the diff touches only scope files, but not that those hunks are the ones the item needed); "the test would
  catch a wrong implementation" (binary can mutate and see the test fail, but not whether the test asserts the right
  thing rather than a proxy); "the docs describe the new behaviour" (binary can check the mention, not the truth of the
  sentence); "the fix addresses the cause, not the symptom" (binary: nothing).
- The proposed rule: judgment rows are marked `kind: judgment`, they never auto-PASS, the round cannot close on them
  alone, and the record labels them so a reader can see how much of a verdict rested on argument.
- The author's proposal for making judgment rows less slippery: split each judgment row into an **anchor** and an
  **inference**. The anchor is a cited line — "main.go:412 — 'refused: path outside scope: paths.go'" — and the binary
  can mechanically check that the file exists, the line exists, and the quoted string is really there. The inference is
  the argument drawn from that anchor ("the refusal is actionable"). The anchor is mechanical and tamper-evident; the
  inference is recorded as an argument. The author's rule of thumb: ask of every judgment row "what does it cite?" — a
  row with no citation is an opinion with a verdict attached.
- The author's stated worry, in their words: a verifier that rubber-stamps judgment rows turns the loop into a formality
  with extra steps, and nothing in the design prevents a lazy verifier. The current containment is: the check list is
  frozen before the claims (the criterion is set in advance), judgment rows are labelled, the stopping bar is stated up
  front, and a second model or harness is the only real check on an argument.

## The questions

1. Is the anchor/inference split a genuine improvement, or is it theater — a way to make an opinion look evidenced?
   If it is real, what exactly does the binary check, and what does that check actually rule out? If it is theater, what
   would you do instead?
2. Is "judgment rows never auto-PASS" the right rule? Consider the alternatives you would actually accept: (a) judgment
   rows are decided by a second model or harness and the binary computes PASS/FAIL from both; (b) judgment rows are
   decided by the human for high-blast-radius items only; (c) the binary computes the mechanical rows and the *verdict
   is the union* — one failing judgment row fails the round, same as now; (d) drop the idea of binary-computed verdicts
   and keep the binary's only job as enforcing the ordering (checks frozen before claims) and the record.
3. Given the goal is cutting cost, is the "binary runs the checks" proposal (5) worth the complexity and the trust it
   asks of the binary (it executes agent-written commands)? What should be built first, second, third — and what should
   be dropped?
4. Is there a cheaper way to make the judgment honest than adding binary machinery — for instance a cheaper model as a
   second opinion, a disagreement threshold, or a rule that a judgment row must cite a specific file and line?
5. Where does your earlier answer need correcting, given the author's pushback?

For each answer: be concrete, give at least one worked example of a judgment row you would accept and one you would
reject and why. End with your single strongest recommendation for the next concrete step, and the one thing you would
refuse to trade away.