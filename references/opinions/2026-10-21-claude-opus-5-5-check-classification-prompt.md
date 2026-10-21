You are giving an independent engineering opinion to the author of a skill called `verified-backlog` (a verify-driven
work loop for a coding agent). This is the third question in that series; you answered the first two (cost-cutting
proposals, and the judgment seam). Do NOT use any tools — the context is below. Be opinionated, concrete, and say what
you would NOT do. If you disagree with your own earlier answers, say so.

## The skill, in brief

A Git-repo-based work loop where a work item's "passed" status is an artifact the worker cannot write itself.

- `bl` is a small Go binary (stdlib only, ~5 ms/call) that owns an append-only ledger `.backlog/ledger.jsonl`, renders
  human views (`TODO.md`, `PROGRESS.md`), stores a sha256 of every evidence file, and refuses illegal moves with exit 2.
- The human talks to an "orchestrator" agent that runs the skill's playbook. The orchestrator dispatches subagents in
  fresh sessions: a **producer** (edits files, returns **claims** — assertions backed by a command it ran) and a
  **verifier** in two dispatches: (1) intent only, where it writes the **check list** — each check is a command with an
  expected result — before seeing any claim; (2) the claims to falsify, producing a table `| check | command | expected |
  observed |` and a verdict.
- `bl round A1 --verdict PASS|FAIL --rows <file>` records the round: the rows are hashed into the ledger, empty rows
  are refused, 3 code failures cap the item. `bl close A1 --commit <sha>` requires a PASS round, a real commit, and a
  clean tree.
- Non-negotiables: the producer cannot record its own success; the verifier must not see the producer's plan before
  writing its own checks; a verdict must be backed by a command that ran; evidence is tamper-evident; the loop stays
  auditable by a third party.

## What your two earlier answers said (abridged — you may revise)

**First answer (cost):** let the model write the checks and let `bl` run them, record what it saw, and compute the
verdict. Eleven proposals: frozen item brief; deterministic hygiene in `bl`; a frozen check list with an ordering rule
(`bl checks` before `bl claims`); verifier-2 continuing verifier-1's session; `bl run` executing the checks; mechanical
test binding in a worktree; the verifier reading a slice not the repo; a small repo map (later dropped); cheaper models
by role; same-failure-twice escalating; pinning the verified tree.

**Second answer (the judgment seam):** the anchor/inference split is real but narrow — it stops made-up evidence, not
wrong reasoning. What actually stops a lazy verifier: (1) each judgment row's **fail condition** frozen before any
claim exists, plus a required "why not mechanical" field; (2) **hunk coverage** — every diff hunk must be cited by at
least one judgment row; (3) **planted defects** — ~1 item in N is verified blind against a variant diff with a known bug,
and a PASS on the planted variant flags every judgment PASS since the last good calibration. Verdict rule: take (c)
union — one failing row fails the round — add a sampled adversarial second judge, keep the human for escalation only,
reject (d) binary-enforces-ordering-only. It corrected itself: `bl run` is about honesty, not cost; freezing check
*commands* is not enough, judgment rows need frozen fail conditions too.

## The author's question now — check classification

The author has noticed that most of this design rests on an unstated premise: **that a check can be mechanical at all**.
The author puts it this way:

> This is based on the premise that every check can be mechanical? Right? If the checks are given by model and model is
> instructed to keep them mechanical then it is fine. But when model is given the data for manufacturing checks, model
> should also be given an option to categorize checks into mechanical ones and the ones that cannot be converted to
> mechanical ones. In that case what should be the strategy to handle non-mech ones?

The author's current working answer, which they want stress-tested:

1. **Classify at authoring time, not at verdict time.** The check-list author (verifier-1) is the only actor that sees the
   intent before the claims, so classification happens there, and the classification is frozen with the list. Every check
   carries `kind: mechanical | judgment`; a judgment check additionally carries `FAIL if <observable condition>` and
   `why not mechanical`. The binary refuses a check with no kind.
2. **Conversion pressure.** The author prompt asks "can a command decide this? If yes, write the command." `kind:
   judgment` is the exception that must be justified, quoted and counted.
3. **Non-mechanical checks split into two kinds:**
   - *convertible* — a mechanical form exists but the author did not find it. Caught by calibration: if a planted
     defect should have been caught by a mechanical check and was not, the classification is wrong and gets audited.
   - *irreducible* — genuinely semantic. Handled by: the frozen fail condition, the mechanically validated anchor, hunk
     coverage, the union rule (one failing judgment row fails the round), a sampled adversarial second judge, and
     escalation on dispute.
4. **Blocking vs advisory.** A proposed refinement: every judgment row carries a severity. `advisory` findings do not
   fail a round — they are filed as their own items, exactly like the skill already files out-of-scope findings. Only
   `blocking` judgment rows can fail a round, and a row may be blocking only if its fail condition is observable by a
   third party reading the anchor. Most judgment findings would be advisory by default, which keeps the loop's teeth on
   the mechanical rows and stops cosmetic arguments from burning rounds.
5. **A portfolio-level signal.** If an item's judgment share is high, that is evidence the *brief* is vague, not that the
   verdict needs more argument: re-baseline the item (write a better brief) instead of arguing harder.
6. **Auditable counting.** The ledger can report the mechanical/judgment split, the blocking/advisory split, and the
   string of every "why not mechanical" — a human can read them and see whether the classification is honest.

## What to answer

1. Is classifying at authoring time, in a frozen list, the right place? What would go wrong if classification happened
   later (at verdict time, or by the binary)?
2. Is the blocking/advisory split the right strategy for irreducible judgment rows — or does it hollow out the loop
   (advisory becomes a place to bury real problems)? If it is right, what stops advisory from becoming a dumping ground,
   and what is the right default ratio? If it is wrong, what is the alternative?
3. Should a mis-classified row (something marked judgment that was really mechanical) be a hard failure, and who
   detects it — the second judge, the planted defects, the human, or the binary?
4. Does the "high judgment share ⇒ vague brief" signal survive scrutiny? What is the threshold, and what else could
   produce a high judgment share?
5. Where does this change your earlier answers, if at all? In particular: does the blocking/advisory split change what
   "one failing row fails the round" should mean?

Concretely: give at least two worked examples of an item where the right answer is a *blocking* judgment row, and two
where the right answer is *advisory*, and say what the binary can check in each case. End with your single strongest
recommendation for the next concrete implementation step, and the one thing you would refuse to trade away.