# Design — the check list schema, and who decides a row

**Status:** proposal, and **not yet verified**. Nothing here is implemented; `bl` does not yet have `checks`, `claims`,
`run`, or a derived verdict. Seven verification rounds are recorded in §8; the last one still found blocking defects,
which are fixed in this revision but have not been through an eighth round. One finding from round 7 is
**structural, not textual** — files outside the pinned tree can hold the fix, and the fix is R28. The other, the
trust root, is **parked** at the end of §7: the chain and the sequence number ship now, the anchor decision waits for
real session logs.
Treat this as a reviewed draft, not a finished design.
**Date:** 2026-10-21
**Inputs:** `references/opinions/2026-10-21-claude-opus-cost-cutting.md` (cost), `…-claude-opus-5-5-judgment-seam.md`
(the judgment seam), `…-claude-opus-5-5-check-classification.md` (classification). Vocabulary: `references/GLOSSARY.md`.

**The test of this document:** a third party can implement `bl checks`, `bl claims` and `bl round` from the text
alone, and two readers cannot disagree about what any of them does. Where the text is silent, it is a defect, and the
verifier's job is to find those.

---

## 1. Why this document exists

The loop today lets a verifier hand `bl` a table of rows, and `bl` records whatever table it is given. That makes the
verdict a *claim* about the code, not a fact about it: a row's "observed" column is typed by a model, and nothing
distinguishes a row that was measured from a row that was remembered.

Three rounds of external review converged on the same shape of fix, with corrections each time:

1. The model should author the checks; the binary should run them and record what it saw.
2. Freezing the check *commands* is not enough. Judgment rows need a frozen **fail condition** too, and a reason they
   cannot be mechanical.
3. The premise "a check is mechanical or not" is the wrong axis. **Every row runs a command.** What differs is who
   reads the output: the binary (`decider: bl`) or a judge (`decider: judge`).

This document turns that shape into rules. It does not decide the cost of the loop; it decides what a row is, who may
write it, and what the binary refuses.

### Non-goals

- Not implementing `bl run` (executing the rows) — that is a separate document, though R7, R21 and R26 fix the
  behaviour it needs.
- Not the repo-map, the brief's own format beyond the clause numbering R2 needs, or the planted-defect calibration
  harness. R16 names the calibration hook without specifying it.
- Not changing the round cap or the close gates, except where a rule below says so explicitly.

---

## 2. Artifacts, and the definitions every rule leans on

All paths are relative to the repository root.

| artifact | path | written by | hashed into the ledger |
|---|---|---|---|
| brief | `.backlog/items/<id>/brief.md` | `bl open`, `bl brief` | yes — `brief_sha` |
| check list | `.backlog/items/<id>/checks.tsv` | `bl checks` | yes — `checks_sha` |
| claims | `.backlog/items/<id>/claims.md` | `bl claims` | yes — `claims_sha` |
| run | `.backlog/evidence/<id>-run<N>.json` | `bl run` | yes — `run_sha` |
| verdicts | `.backlog/items/<id>/verdicts.tsv` | the verifier | no — an input to `bl round` |
| evidence | `.backlog/evidence/<id>-r<N>.txt` | `bl round` | yes — `rows_sha` |
| views | `TODO.md`, `PROGRESS.md` | `bl render` | no — artefacts (R25) |
| audit | `.backlog/audit/<date>.md`, `.backlog/audit/<date>.json` | `bl audit` | no — artefacts (R25) |
| ledger | `.backlog/ledger.jsonl` | `bl` | — |

**Brief clauses.** A clause is a line of the brief matching `^(B[0-9]+):\s+(.+)$`. Its id is the captured group, its
text is the rest of the line. Clause ids are never renumbered. A clause may be amended by rewriting the brief; the new
hash is recorded as a separate `brief` event and the previous hash stays in the ledger (R24).

**Scope.** The scope of an item is a set of repository-relative paths, recorded at `bl open`. A path ending in `/`
names every file under that directory. A path that names no file is still a scope entry — a file that does not exist yet
is in scope.

**The verified tree.** The state the round verified. It is computed from the *filesystem*, not from the index, because
the producer's changes are uncommitted and because the index can hide them. The computation is: list the working
directory's files, excluding `.git/`, the artefacts of R25, and every path ignored by `.gitignore`; hash each with
`git hash-object`; write the resulting tree with `git mktree`. The index is never consulted, so a staged-but-unstaged
path, a `skip-worktree` path and an `assume-unchanged` path cannot hide a change from the tree. The same computation
at `bl open` time produces the **before-tree**.

The tree covers the whole working tree, not only the scope, on purpose: a change outside the scope can change what
the item's commands do (a new dependency, a changed config), and a verdict must be about the tree the commands ran
against. Scope governs which files the *producer* may write (R2, R14, and the hygiene rules), not what the tree
contains.

Two trees, both 40-character lowercase hex `git write-tree` values, and never compared by prefix.

**The diff.** `git diff --no-renames --no-ext-diff --no-textconv --diff-algorithm=myers --unified=0 <before-tree>
<verified-tree>`. Every flag is mandatory: `--no-renames` so a rename is a delete plus an add, `--no-ext-diff` and
`--no-textconv` so no external diff driver or textconv filter can run, and `--diff-algorithm=myers` so the hunk
boundaries do not depend on the repository's `diff.algorithm` setting. The diff is taken over the whole tree; the
scope does not narrow it. A **hunk** is one contiguous block of changed lines in that output, identified by its file and
its new-side start line; its new-side range is the start line through the last line of the block. A deleted-only hunk
has an empty new-side range.

**The run is the unit of verification.** A round is two steps that cannot be reordered: `bl run` executes every frozen
row's command and writes a run file; the judge reads *that file* — the same bytes the round will record — and writes the
verdicts file naming the run; `bl round` consumes the run and the verdicts together. There is therefore exactly one
execution per round.

**Run file schema.** `.backlog/evidence/<id>-run<N>.json` holds, for the run: the run id (`run<N>`), the verified tree
at the start and at the end of the run, and for each row: the `id`, the `cmd`, the exit code, the timeout flag, the
**full** stdout and stderr as text (UTF-8; output that is not valid UTF-8 is stored base64 with a `binary: true` flag),
and the sha256 of each stream. A `decider: bl` row carries its `verdict` beside them — `PASS` or `FAIL` — and the
binary computes it from the row's `expect` and the observed values recorded next to it (R7); a `decider: judge` row
carries none, because its row-verdict is the judge's. The full output is stored, not an excerpt: R8's regexes and R9's output anchors are
evaluated against it, and the judge reads it. The excerpt (4096 bytes per stream) exists only in the evidence file, for
a human to read.

The run file is named by a run id of the form `run<N>` for the item.

**Check list format.** One row per line, tab-separated, with a header line naming the columns:

```
id	decider	severity	brief_ref	cmd	expect	fail_if	why_not_mech	criterion
```

Columns are read **by name**. The header may list the columns in any order, but must list exactly these nine names,
each exactly once. A row must have exactly nine fields, in the header's order.

| column | meaning |
|---|---|
| `id` | unique within the item; `[A-Za-z0-9_.-]+`; the ids `run` and `tree` are reserved, because the verdicts file uses them as metadata lines |
| `decider` | `bl` or `judge` (R1) |
| `severity` | `blocking` or `advisory` (R1) |
| `brief_ref` | a clause id from the brief, or empty (R2, R3) |
| `cmd` | a shell script, run by `/bin/sh -c` from the repository root; always run, for every row, whatever the decider (R7); its pipes are pipes — a backslash before a pipe is refused (R34); a row that covers a change names the file it covers, so R10 can see the hunk covered |
| `expect` | for `decider: bl` only; the machine-decidable expectation (R8); its pattern is Go's RE2 — two alternatives are separated by a pipe, and a backslash before a pipe means a LITERAL pipe |
| `fail_if` | for `decider: judge` only; the frozen fail condition (R3) |
| `why_not_mech` | for `decider: judge` only; why the row cannot be machine-decided (R3) |
| `criterion` | one line, human-readable, the thing being checked |

**Verdicts format** (the verifier's input to `bl round`). A header line naming the columns, then one line per frozen
check id:

```
id	row_verdict	anchor
```

- The header names exactly the three columns `id`, `row_verdict`, `anchor`, each once, in any order; columns are
  read by name.
- The first line after the header is `run <run-id>` and the second is `tree <40-hex>`. Neither is a row: R6 matches rows
  only, and both lines are required. The run id must be the item's latest run, and the tree must be the tree that run
  verified (R6).
- `row_verdict` is exactly `PASS` or `FAIL`.
- The `row_verdict` of a `decider: bl` row is not what decides the round: that row's row-verdict is the one the
  binary computed from the run (R7), and the judge writes what it read for the record.
- `anchor` is empty for `decider: bl` rows and must be empty for them (R6).
- For `decider: judge` rows the anchor is a **source anchor** `file:line "quoted text"` or an **output anchor**
  `"quoted text"`. The form is determined syntactically: an anchor whose text before the first `"` contains a `:`
  and a line number is a source anchor; otherwise it is an output anchor. Only the form so determined is checked (R9).
- The quoted text is the content between the first `"` and the last `"` in the field. Inside it, `\\"` means a
  literal quote and `\\\\` a literal backslash; a field with an unescaped `"` inside the quoted text is refused. The
  quoted text must not be empty.
- A line whose first character is `#` is a comment: it is not a row, it is skipped by R6, and if it begins
  `# finding:` (one space, lower case) it is read by R13. A `# finding:` line with an empty finding text is refused.
- **The tree the judge read.** The verdicts file's header is followed by a line `tree <40-hex>`, naming the verified
  tree the judge read when it wrote the file. `bl round` refuses if that tree is not the tree the round verifies
  (R6). Without it, a judge could sign off on a tree the producer has since changed.

**One assertion per row.** A row's command asserts exactly one thing, and its `expect` is reachable only through
that assertion. A command that runs several statements can satisfy its expectation from one of the others — a
refusal from an earlier statement, a line of output that happens to match — so the row then tests nothing it
claims to test. A row that needs a second statement runs it in a separate row.

**Evidence format** (what `bl round` writes). One line per frozen check id, in the order the check list was frozen:

```
id	cmd	exit	signalled	timed_out	observed	excerpt	row_verdict
S1	go test ./scripts/cmd/bl	0	false	false	stdout sha 3f2a…, stderr sha 9b1c…	ok	pkg	bl	0.004s	PASS
J3	cat scripts/cmd/bl/missing.conf	1	false	false	stdout sha 0d44…, stderr sha 71b0…; anchor "No such file or directory"	cat: scripts/cmd/bl/missing.conf: No such file or directory	FAIL
```

`signalled` is true when the process was killed by a signal, and then `exit` is `128+signal`; a command that exits
137 on its own is recorded as `exit 137, signalled false`, so the two are distinguishable. The `excerpt` column
carries the first 4096 bytes of each stream, for a human; the `observed` column carries the hashes the run computed.
The `row_verdict` column holds the row's row-verdict: the binary's computed verdict for a `decider: bl` row (R7) and
the judge's for a `decider: judge` row.

**Execution environment (R21).** Every `cmd` is run as `/bin/sh -c <cmd>` with:
- working directory = the repository root;
- environment = `PATH` inherited from the caller and **recorded in the run file**, plus `HOME` and `TMPDIR` pointing at a
  fresh scratch directory outside the repository, created for the run and removed after it, plus `LANG=C`, `TZ=UTC`,
  and nothing else — in particular no `GIT_*`, and no `HOME` pointing into the repository, so a tool that caches
  under `HOME` cannot write into the tree;
- stdin = `/dev/null`;
- rows executed **sequentially**, in the order of the frozen list;
- a 120-second timeout, after which the process **group** is killed; the row is recorded with `timed_out = true` and
  `exit = -1`, which is distinct from any command's own exit code;
- stdout and stderr captured separately, each truncated to 4096 bytes for the excerpt, each hashed in full (sha256 of
  the raw bytes) for the record;
- the exit code recorded as an integer; a signal death is recorded as `128+signal`.

Two runs of the same command against the same tree produce the same hashes whenever the command's output is
deterministic; a command whose output varies produces varying hashes, and the run file records the values it saw.

**Ignored files under the scope.** A file that `.gitignore` excludes is not in the verified tree, so a change to it
is invisible to the diff. The before-state therefore records, for every ignored file that exists under the item's
scope paths, its path and its hash; the same list is recorded with the run. R28 refuses a round in which one of those
hashes changed, unless the file was named in the item's scope as an ignored file the item may write
(`bl open A1 --scope-ignored <path>`). This is the difference between "the fix is invisible to the diff" and "the fix
is invisible to the record": the first is allowed only for a file the item declared, and the second is not allowed at
all.

**Artefacts (R25).** `.backlog/` is an artefact directory: it is expected to be untracked, and it is excluded from the
clean-tree test in R18. It is never part of a verified tree, because `git add -A` skips ignored files and `.backlog/`
is in `.gitignore`. If `.backlog/` is tracked in the repository, that is a repository error and `bl` says so.
The two rendered views, `TODO.md` and `PROGRESS.md`, are artefacts as well: every event in the ledger rewrites
them, so they are excluded from the verified tree and from the round's diff. The audit report is an artefact as
well: `bl audit` writes it under `.backlog/audit/`, inside the artefact directory, so a report of the ledger is never
a file of the work and never lands in an item's diff. Without that, a tree pin would
refuse every round after the ledger moved — and the ledger moves in the item's own flow, because R14 files the
advisory items between the run and the round.

**Init.** `bl init` creates `.backlog/`, the empty ledger, and the two views, and reports — but does not change — the
fact that `.backlog/` is not in `.gitignore`. It is the only command that creates the directory.

**Who writes the ledger.** One writer: `bl`. Every other party produces a file and hands it over — the human writes
`BRIEF.md` and the item text, the producer writes the source files and `claims.md`, verifier-1 writes `checks.tsv`,
verifier-2 writes `verdicts.tsv`, and the machine produces the run — and `bl` hashes and records each one. No verb
edits the ledger; a correction is a new event. The limit, stated plainly: the model has a shell, so it can rewrite the
file directly, and "the ledger is append-only" is a property of the binary, not of the machine. The chain and the
anchor exist for exactly that case, and for nothing else.

**Human sign-off (R22).** `bl signoff A1 --by <name> --note <text>` records a `signoff` event naming the item, a
non-empty person and a non-empty note. It is accepted only for an item that has no round yet, so it cannot override a
failed round.

---

## 3. Rules

Each rule is decidable: given the artifacts, the rule has one outcome. "R_n" is the unit an implementation is judged
against.

**R1 — schema.** `bl checks` accepts a check list only if the header names exactly the nine columns in §2, and every
row has a unique `id`, `decider ∈ {bl, judge}`, `severity ∈ {blocking, advisory}`, a non-empty `cmd`, and exactly the
fields its decider requires: `decider: bl` rows require a non-empty `expect` and must have empty `fail_if` and
`why_not_mech`; `decider: judge` rows require non-empty `fail_if` and `why_not_mech` and must have an empty `expect`.
Any violation is a refusal, not a warning.

**R2 — a blocking row cites the brief.** A row with `severity: blocking` must carry a `brief_ref` naming a clause that
exists in the item's brief. A `brief_ref` naming a clause that does not exist is a refusal. A row with
`severity: advisory` may carry an empty `brief_ref`.

**R3 — a judge row justifies itself.** A `decider: judge` row must carry a non-empty `fail_if` (the frozen fail
condition) and a non-empty `why_not_mech`. `why_not_mech` shorter than 12 characters, or equal — after trimming and lower-casing — to one of the placeholders
`tbd`, `todo`, `n/a`, `none`, `because`, `because it is judgment`, is a refusal. Whether the reason is *good* is not
mechanically decidable and is not claimed to be: the author's duty is to write a row that is genuinely undecidable by a
command, and the mechanical backstops are R16 (a second judge asked to write a deciding command) and the calibration
run. A `why_not_mech` that names a command result ("the tests failing is a command result") is a defect the author
must not ship — `bl` cannot detect it, and the record shows the text so a reader can.

**R4 — the list is written once.** `bl checks` is refused if the item already has a check list, except with
`--amend`, which may do exactly three things: reclassify a row from `decider: judge` to `decider: bl` — which also
requires setting that row's `expect` and clearing its `fail_if` and `why_not_mech`, because R1 would otherwise refuse
the result — fill an empty `brief_ref` on an advisory row, and ADD a row. An amendment never changes the `id`,
`severity`, `cmd`, `expect`, `criterion`, `fail_if` or `why_not_mech` of a row that is already in the list (except
that clearing them is part of a `judge → bl` reclassification), and it never removes a row. An amendment never
reorders rows.
A row added by an amendment is a row like any other: R1, R2, R3, R8 and R34 apply to it, and its `id` must not be
one the list already holds. It is how a defective row is corrected — the added row asserts the thing the defective
row was written for — and adding it can only tighten the round, because the derived verdict is the AND over the
blocking rows (R12): a new blocking row that FAILs makes the round FAIL, and no row that is added can turn a FAIL
into a PASS. That is why growth is allowed after the claims and a change is not (R5).
Every amendment is an event in the ledger with the old and the new list hash. An amendment that CHANGES a row — a
reclassification or a `brief_ref` — is refused if the item already has a `claims` event (R5); adding a row is not. An
amendment is refused once the item has a PASS round: the verdict already rests on the list, and a later list cannot
reopen it.

**R5 — the list is frozen before the claims.** `bl claims A1 --file <path>` reads the producer's report from that
path. A claim exists only if a `claims` event is in the ledger for that item; a hand-written `claims.md` is not a claim,
and `bl claims` overwrites whatever is at that path.
`bl claims` is refused if the item has no check list. `bl claims` is refused if the item already has a `claims`
event — the claims are frozen once. `bl checks` is refused if the item already has a `claims` event, except for an
amendment that adds a row (R4). This is the ordering rule the whole design rests on, and the one thing not to trade
away: what it freezes is the CONTENT of the list, so that a claim means the same rows when it was made and when the
round is recorded. A row of a frozen list is never changed and never removed, so a claim made against the list
still holds against it; and a list that grows can only make the round stricter (R4, R12), so a correction can never
be a way to pass a broken implementation. A defective row — one whose command satisfies its expectation from
something other than its own assertion — is corrected by adding a row that asserts the thing properly, not by
editing the row that failed to assert it.

**R6 — the verdicts file must match the frozen list, the run, and the tree.** `bl round` accepts a verdicts file only
if it contains exactly one line per frozen check id, no line with an unknown id, and no frozen id missing; and if its
`run` line names the item's latest run; and if its `tree` line equals that run's tree; and if that tree equals the
current verified tree; and if the item has a `claims` event; and if the run has not already been consumed by a round;
and if the run file matches the hash recorded in its `run` event.
Comment lines are skipped. A `decider: bl` row's line must have an empty `anchor`; a
`decider: judge` row's line must have a non-empty `anchor` (R9). The verdicts file need not be in the frozen order.

**R7 — the run executes every row, and the round records that run.** `bl run A1` executes the `cmd` of every row in the
frozen list, under the environment in §2, whatever the decider, and writes the run file with the exit code, the
timeout flag, the full output and the output hashes of each row, and the row-verdict of every `decider: bl` row. A
`decider: bl` row's row-verdict is computed by the binary from `expect` and the run's observed values, and is recorded
with them. A `decider: judge` row's row-verdict is the judge's; the judge read the run file and wrote that verdict into the
verdicts file. The run's exit code follows the rows' verdicts: it is 0 when every `decider: bl` row's computed
row-verdict is `PASS`, and 1 otherwise, so a row whose command refused is not reported as a failure when its expectation
holds. The evidence file of the round copies the run's observed values, so the claim that a command ran is never
something a model typed into a cell, and the judge and the record read the same bytes. `bl round` never executes a
command: it consumes a run.

**R8 — a binary-decided expectation is one of five forms.** `expect` is exactly one of: `exit <code>`,
`stdout matches <regex>`, `stderr matches <regex>`, `file exists <path>`, `tree == <hash>`. Any other form is a
refusal at `bl checks` time, and again at `bl run` time when a frozen row carries one, because the run computes that
row's verdict from it. The regex dialect is Go's `regexp` (RE2), applied with search semantics (not a full
match) to the **full** captured output, not the 4096-byte excerpt. `<code>` is an integer between 0 and 255 — `-1` is reserved for the timeout and is not a valid expectation, so a
timeout can never be written up as a pass; `<path>` is relative to the
repository root, may not be absolute, may not contain `..`, and `file exists` is true when `stat` succeeds for it at the moment the
row runs, be it a file or a directory; `<hash>` is a 40-character lowercase hex tree value — the before-tree is the only tree a
row can know when the list is written, and `tree ==` against the verified tree can only be written in a
`second-check`.

**R9 — a judge row is anchored, and the anchor is checked.** The anchor's form is determined syntactically (§2), and
only that form is checked. A judge row about the change and a judge row about a command's output are told apart by the
form of the anchor, not by a rule: the author picks the form, and the form must be valid for what the row reads.
- **output anchor** — the quoted text must appear as a substring of the row's captured stdout or stderr (the full
  output, not the excerpt). If the command produced no output at all, the anchor cannot match and the row's verdict is
  `FAIL`; a row is not refused for an empty output, so an item cannot deadlock on a row whose command says nothing.
- **source anchor** — `file` must be inside the item's scope, the line must exist in the verified tree, the quoted
  text must appear as a substring of that line, and the line must lie inside the new-side range of a diff hunk of the
  round's diff.

An anchor of the wrong shape for its form — a source anchor whose file is not in the scope, whose line does not exist,
or whose quoted text is not at that line, or which lies in no hunk; an output anchor whose quoted text is not in the
command's output — is a refusal.

**R10 — hunk coverage.** Every hunk in the round's diff must be covered by at least one **blocking** row, of either
decider. A row covers a hunk if either:
- it is a `decider: judge` row whose source anchor lies in that hunk's new-side range; or
- its `cmd` contains the hunk's file path as a literal substring.

The comparison is literal: a row that exercises a file without naming it covers nothing. A check list written to
drive a binary never names a source file, so on its own it leaves every hunk uncovered and no round can be recorded
at all. The path belongs in the command, next to the thing it exercises.

A file that is added, deleted, or mode-changed without any textual hunk (a binary file, an empty file) is covered
only by a blocking row whose `cmd` names that path.

An uncovered hunk refuses the round. Advisory rows do not count towards coverage.

**R11 — severity is frozen with the list.** A row's `severity` may not change after the check list is frozen. A
verdict-time request to downgrade a failing blocking row to advisory is refused; the requester must file a new item
with the reason. (`--amend` in R4 cannot change severity either.)

**R12 — the verdict is derived.** The round's verdict is `FAIL` if any **blocking** row's row-verdict is `FAIL`, and
`PASS` otherwise. Advisory rows never change the verdict, but a failing advisory row is recorded and must be filed
(R14). `bl round` refuses a submitted `--verdict` that differs from the derived one; the supplied value is a
cross-check, not the decision. `--verdict` is optional; when absent, the derived verdict is recorded. R13 overrides the
derived verdict when it applies.

**R13 — a new finding is a dispute, not a pass.** If the verdicts file carries a comment line beginning `# finding:`
followed by non-empty text, or the orchestrator passes `--finding <text>`, the round is a **dispute**: its verdict is
`FAIL` and its cause is `dispute`, whatever R12 derived, and the finding text is recorded in the event. The cause is
derived, not supplied: on such a round `--cause` is not required, and a supplied `--cause code` or `--cause harness`
is refused (R23). A harness round has no rows and therefore cannot carry a finding. A dispute is never `PASS`, and is
never silently filed as advisory. Escalation: the second judge, then the human. A new finding is never silently
ignored.

**R14 — a failing advisory row must be filed, before the round.** Every advisory row whose row-verdict is `FAIL` must
already be filed as a backlog item at the time `bl round` runs, with the row id appearing in the item's `text` field
as a whitespace-delimited token, and with that item being the most recently filed item mentioning the id. Row ids are
per item, so an id from another item cannot satisfy this rule. The order is:
the verifier produces the verdicts file, the orchestrator files the advisory items, then `bl round` runs. A round that
records a failing advisory row without such an item is refused. Advisory failures never count towards the code-round
cap (R19).

**R15 — one blocking binary-decided row per item.** To close an item, its last round must contain at least one
`blocking` row with `decider: bl`, unless a human sign-off (R22) is recorded for the item. A docs-only item is not
exempt: its mechanical outskirts (links resolve, examples run) are `decider: bl` rows.

**R16 — a mis-classified row is a round failure, not a producer failure.** The orchestrator may dispatch a second judge
to write a command that decides a `decider: judge` row, and records it with
`bl second-check A1 --row <id> --cmd "<cmd>" --expect "<expect form>"`, where the expectation uses R8's forms. `bl`
runs that command under R21 and computes the row's verdict from it, and records the computed verdict in a
`second-check` event naming the run it was computed against. `bl second-check` must run **before** `bl round`;
`bl round` refuses if a `second-check` event for a row of this list, computed against this round's run, disagrees with
the row's recorded row-verdict. A `second-check` event computed against an earlier run is stale and is ignored. If it agrees, the reclassification is recorded and the round stands. The producer is not failed: the row was
not theirs. A `misclassified` event is the one kind of refusal that writes to the ledger; it is not a round and does
not consume a code round (R19).

**R17 — no confidence scores.** A judge row's verdict is supported by its anchor and its frozen `fail_if`. The
`row_verdict` field must be exactly `PASS` or `FAIL` (R6); a self-reported confidence number cannot be expressed
there, and an `anchor` field that is not one of the two anchor forms is refused by R9.

**R18 — the close is pinned to the verified tree, and to the brief the round was written against.** `bl close A1
--commit <sha>` requires: the last round is `PASS`; the brief hash recorded in that round equals the current brief
hash, so a brief amended after the round invalidates the round and the item needs a new one; the commit's tree
(`<sha>^{tree}`, as a 40-character hex value) equals the verified tree pinned by that round; the
working tree is clean apart from artefacts (§2) — clean means no tracked file differs from `<sha>^{tree}` and no
untracked file exists outside `.backlog/`; and the item satisfies R15. Trees are compared as full 40-character hex
strings, never by prefix. The close event records the commit and the pinned tree.

**R19 — the code-round cap.** Three code rounds per item; a fourth code round is refused and the item escalates to a
human. A code round is a round whose cause is `code`; a `PASS` round carries no cause and is not a code round. The
causes are `code`, `harness` and `dispute`; a round with cause `harness` or `dispute` does not consume a code round. A
`misclassified` event (R16) is not a round and consumes nothing. Three harness rounds and three dispute rounds per item; a fourth of either is refused and the item escalates to a
human, so a finding cannot be used to excuse a failing item indefinitely. An item may have at most six rounds in total,
of any cause; a seventh round of any kind is refused and the item escalates, so three code rounds plus three disputes
cannot become six failing rounds. The number of harness and dispute rounds is
recorded and shown by `bl status`.

**R20 — hashes, and the frozen artefacts are re-verified at every command.** The brief, the check list, the claims, the
run file and the evidence file are each hashed (sha256) and the hash is stored in the ledger event that references the
file. `bl check` re-hashes every one of them and fails if any file no longer matches its recorded hash.

More important: `bl run`, `bl round`, `bl claims` and `bl close` each start by re-hashing the item's brief, check list
and claims — and, for `bl close`, the evidence file of the last round — and refuse if any of them differs from the hash
of the latest event that recorded it. A frozen artefact that was edited
after the freeze is therefore refused at the point of use, not merely detected later by `bl check`. A run file that
has no `run` event, or whose hash differs from that event's, is refused by R6.

**R21 — the execution environment is fixed.** The environment in §2 is the whole environment, with one exception:
`PATH` is inherited from the caller, and the inherited value is recorded in the run file so that the run can be
reproduced. No other variable is inherited, in particular not `GIT_*`, and `HOME` and `TMPDIR` point outside the
repository. A row that needs an environment variable must set it inside its `cmd`.

**R22 — sign-off.** `bl signoff A1 --by <name> --note <text>` records a `signoff` event. It names the item, a
non-empty person and a non-empty note. It is accepted only for an item that has no round yet, so it cannot override a
failed round; it exists so that an item with no `decider: bl` blocking row can still close (R15).

**R23 — the cause is constrained.** `--cause` is required for every `FAIL` round and takes `code`, `harness` or
`dispute`. When R13 applies the cause is `dispute` and a supplied `--cause code` is refused.
- `--cause harness` is accepted only when the verdicts file is absent or contains no rows, because a harness failure
  means the verifier did not run, and only when no `--finding` was given, because a run that never happened cannot
  have found anything. A harness round skips R6, R9, R10, R12 and R14, and is recorded with no rows.
  **Absent** means the flag was not given, or the path does not exist. A path that exists and cannot be read as a file
  — a directory, or a file that cannot be opened — is refused, not treated as empty: a caller who meant to hand over
  rows and handed over something else has made a mistake, and a mistake is not a run that never happened.
- `--cause dispute` is accepted only when R13 applies.
- `--cause code` is accepted only when the derived verdict is `FAIL` and at least one blocking row has row-verdict
  `FAIL`.
- `--cause` is not required when R13 applies: the cause is `dispute` and is derived.
A `PASS` round carries no cause; a supplied `--cause` on a `PASS` round is refused.

**R24 — amendments re-hash.** An amended brief (`bl brief A1 --file <path>`) or an amended check list is recorded as a
new event with its new hash; the earlier event keeps its hash. `bl check` compares each file against the hash of the
**latest** event that references it, and a file that matches an older hash but not the latest is a failure. If an
amendment removes a clause that a frozen row cites in its `brief_ref`, `bl round` refuses that round: the row's
criterion no longer exists in the brief it was written against.

**R28 — an ignored file under the scope may not change.** The before-state records the path and hash of every ignored
file under the item's scope paths (§2). A round is refused if any of those hashes changed, unless the file was named
in the item's scope as an ignored file the item may write (`--scope-ignored`). A file that the item changed and that
the scope did not declare is therefore refused, not silently dropped from the record.

**R29 — the anchor covers a sequence number, not just a hash.** An `anchor` event records the ledger `seq` it covers
and the checkpoint hash at that `seq`. `bl check` refuses when the ledger's `seq` is lower than the last anchored
`seq`: a chain that verifies is not enough, because an attacker who can rewrite the file can also truncate its tail
and leave a shorter chain that still verifies. The version counter is what makes truncation visible. (This is the
lesson TUF/Uptane learned with its version and expiry metadata, and the reason certificate-transparency logs publish
a signed tree head rather than just a tree.)

**R25 — artefacts are excluded from the tree test.** `.backlog/` is excluded from the clean-tree test in R18, and
never appears in a verified tree, because `.gitignore` keeps it untracked. If `.backlog/` is tracked in the
repository, `bl` refuses the round rather than comparing trees that include it. The two rendered views, `TODO.md`
and `PROGRESS.md`, are artefacts as well: every event in the ledger rewrites them, so they are excluded from the
verified tree and from the round's diff, and a ledger event between the run and the round does not move the tree.

**R26 — the tree must not move under the run.** `bl run` computes the verified tree before the run and again after it.
If the two differ, the run is refused: a row's command changed the tree, and the rows were verified against a state
that no longer exists. The comparison is on the tree, so a command that only touches a timestamp does not trip it, and
a command that writes into the scratch directory outside the repository does not trip it either. The current verified
tree must also equal the run's tree when `bl round` runs (R6).

**R27 — a row may not write to the ledger.** `bl run` hashes the whole of `.backlog/` before the run and again after it.
If the hash differs, the run is refused: a row's command wrote to the ledger, the evidence, or the item files, and the
run's own record would have been written by the thing it is recording. Because the whole directory is hashed, two
items in flight at once cannot both run: the second run's pre-hash differs from the first's post-hash and is refused.
The skill's one-item-in-flight rule is what keeps that from being a problem.

---

### 3.5 Rules added by running the loop

These five rules were not in the first draft. They came out of the implementation rounds on the repository's own
backlog, where each one caught a hole that the design had not thought of. They are stated here because the
implementation in `scripts/cmd/bl/` already enforces them and the design should not lag the code.

**R30 — a new premise is a new event, and the old one keeps its hash.** A check list that turns out to be broken
itself — commands that cannot run, rows that die before an assertion — cannot be edited into shape: a list is
written once. `bl rebaseline <id> --file <path> --reason <text>` replaces it, records a `rebaseline` event carrying
the new list's hash and the previous list's hash, and leaves every earlier event in the ledger exactly as it was.
The earlier rounds are not erased. A re-baseline is a new premise, and the ledger says so.

**R31 — one helper, one family, one test.** Every verb that reads or writes a frozen artefact re-hashes that
artefact and compares the hash with the one in the ledger before it does anything. The comparison is one helper,
called from every such verb, and one table-driven test enumerates the verbs in that family, so a verb added later
cannot join the family without the test noticing. Without this rule the same hole reappeared three rounds running:
one verb at a time was fixed, and the next one had the same defect.

**R32 — the frozen row is checked against the brief that exists now.** A blocking row cites a brief clause. If the
brief is later amended and the clause is gone, the row is refused at use, not just at write time: `bl check`,
`bl round` and `bl close` all re-read the brief and refuse, naming the row and the missing clause. The rule named
differs by path, because the rules differ: a list **written** with a clause that does not exist is refused under R2,
the schema rule, while a frozen row whose clause an **amendment** removed is refused under R24, the amendment rule. A
list whose rows point at clauses that no longer exist is not a passing list.

**R33 — the claims freeze the brief and the list.** Once an item has a claims event, the brief and the check list
are frozen. No verb may rewrite them: `bl brief`, `bl checks` and `bl open` all refuse with R5, and they refuse
before they write anything. An item with claims cannot be re-opened, because re-opening would replace the brief
that the claims were made against.


**R34 — a command is a shell script, and a pattern is not a shell.** A `cmd` field is a script run by `/bin/sh -c`
from the repository root, so a pipe in it is a pipe: `a | b` runs `b` on what `a` prints. A pipe is ESCAPED when an odd
number of backslashes precedes it: the shell reads `\|` as a literal pipe, so a row holding one does not do what it
says, and such a row is refused in a frozen list. An even number of backslashes before a pipe is an escaped backslash
followed by a real pipe, and the row is fine — the check counts the backslashes rather than looking for the pair.
The check reads the command the way the shell reads it: inside a single-quoted or double-quoted string a `|` is not
a pipe at all, it is text the pattern carries, so the check looks only at the pipes outside quotes. `grep -Eq
'a\|b' file` is a working command and is not refused — the backslash there belongs to the pattern, not to the
shell.
The same goes the other way: an `expect` pattern is Go's RE2, so two alternatives are separated by a pipe, and a
backslash before a pipe there means a literal pipe rather than an alternation. The refusal names the row that holds
the mistake.

**R35 — only the item in flight is worked on.** A verb that works on an item — `bl run` and `bl round` — refuses
when that item is not the item in flight, and reports R35. The loop keeps one item in verification at a time, and
the lock is what keeps it that way: two items in flight at once would each hold a half-written tree, and the run
that pins a tree cannot tell which half it pinned. A refusal under R35 writes nothing.

**P1 is a rule about the whole list, not about one row.** The precedence in §4 applies across the list: if one row
breaks R1 and another breaks R8, the refusal names R1. Evaluating rule by rule and row by row lets the rule that
happens to sit higher in the file win, which is not the same thing as the earliest broken rule. This is still an
open implementation item (I7) — the design states it, the code does not yet do it.

## 4. Precedence

Where two rules can both match, the winner is named here, not left to the implementation. P1, P2, P3, P4 and P5 are per
command: the checks below are the order in which a command evaluates its rules, and a rule that refuses stops the
command there. P6, P7, P8, P9 and P10 are general rules that apply to every command, and P11 is the sixth per-command
list, added by item I9 when the implementation found the gap.

**P1 — `bl checks` order.** R5 (the claims freeze the list) → R4 (the list is written once) → *the list is read* → R1
(schema) → R34 (the command is a shell script, not a pattern) → R4 (the amend scope, when `--amend` is given) →
R2 (brief clause) → R3 (judge justification) → R8
(expectation form). The state guards come first because a command that is not allowed to write a list has no use for
the list, and refusing before the input is read means a doomed command never waits on stdin. Within one rule, the
first broken row in file order is named, and only that one. Item I9 changed this order: before it, R1 came before R5
and R4, so item I7's brief said "R1 across all rows, then R5, R4" - that wording is superseded.

**P11 — `bl rebaseline` order.** R20 (the frozen artefacts are what they were) → R1 (schema) → R34 (the command is a shell script, not a pattern) → R2 (brief clause) → R3
(judge justification) → R8 (expectation form) → R5 (the claims freeze the list) → R4 (the list is written once, and
the amend scope). The list's rules come first here because the list IS the proposal: a re-baseline that names a
broken list is refused for the list's own fault, not for the state of the item.

The six per-command lists are `bl checks` (P1), `bl round` (P2), `bl run` (P3), `bl claims` (P4), `bl close` (P5) and
`bl rebaseline` (P11).

**P2 — `bl round` order.** R35 (the item is the one in flight) → R20 (the frozen artefacts are what they were) → R23-harness (a harness round has no rows and stops here) → R6 (the verdicts file matches the
list, names the latest run, and names the run's tree, which is the current tree) → R28 (no ignored file under the scope
changed) → R25 (artefacts are excluded) → R7 (the round consumes the run, and the row-verdicts of the `decider: bl` rows
are the ones the binary computed from the run's observed values) → R9
(anchors) → R10 (hunk coverage) → R16 (misclassification) → R13 (dispute) → R14 (advisory filed) → R12 (derived verdict)
→ R23-cause (code or dispute) → R19 (cap). R7 sits with the rules that read the run — R9's anchors and R16's second check —
because the row-verdicts it yields are read out of the run file, and R14 and R12 consume them: the derived verdict is the
AND of the binary's computed row-verdicts for the `decider: bl` rows and of the judge's row-verdicts for the
`decider: judge` rows, so it does not rest on the judge's word for a binary row.

**P3 — `bl run` order.** R35 (the item is the one in flight) → R20 (the frozen artefacts are what they were) → R1 (the list still parses) → R7 (every row
ran and its row-verdict was computed) → R8 (the expectation's form, which is refused inside R7) → R26 (the tree did not move) → R27 (the ledger did not move) → the run file and the `run` event. A run is refused
on the first of these that fails, and no run file is written. R26 and R27 are comparisons ACROSS the run — the tree and
the ledger are hashed before it and again after it — so they can only be made once the rows have run. The list
therefore has to parse, and the rows have to run, before they are checked. This order was written before anything ran;
item I2 found the order that can be implemented, and item I2's verifier confirmed the implemented order is the only one
that can be.

**P4 — `bl claims` order.** R20 (the frozen artefacts are what they were) → R5 (the list exists, and the claims are
not already recorded).

**P5 — `bl close` order.** R20 (the frozen artefacts are what they were) → R18 (a PASS round, the pinned tree, a clean
tree) → R15 (a blocking `decider: bl` row, or a
sign-off) → R25 (artefacts are excluded).

**P6 — the frozen list wins.** If a stored check list and a submitted list disagree about an `id`, the stored one wins
for that id, and the disagreement is a refusal (R4), not a merge. P6 governs submissions that are not amendments; an
amendment is governed by R4 alone.

**P7 — stricter wins.** Where an amendment would make a row easier to pass, it is refused (R4): `judge → bl` is
allowed, `bl → judge` is not.

**P8 — derived beats supplied.** Where a derived value and a supplied value disagree, the derived value is the record
and the supplied one is refused (R12, R23), except that R13 overrides the derived verdict when it applies.

**P9 — the earlier rule in this document wins** for any pair not covered by P1–P8.

**P10 — one rule per command, one refusal per call.** A command that reaches a refusing rule stops there and reports
that rule; it does not continue to later rules. A refusal writes nothing to the ledger, to the evidence and to the
item's artefacts, except the `misclassified` event in R16, which is not a round. The run.log is the log of every
call, refusals included, so it grows; that is not state.
call, refusals included, so it grows; that is not state.

---

## 5. Worked examples

Real values from this repository. Unless an example says otherwise, a round in these examples has a run
first: `bl run` produces the run file, the judge reads that file and writes the verdicts, and `bl round` consumes the
two together. The brief used by E1–E16 has these clauses:

- `B1: the repository's test command go test ./scripts/cmd/bl exits 0.`
- `B2: a missing config produces an error naming the path searched and how to create the config.`

The item's scope is `scripts/cmd/bl/`.

### E1 — a well-formed list is accepted

```
id	decider	severity	brief_ref	cmd	expect	fail_if	why_not_mech	criterion
S1	bl	blocking	B1	go test ./scripts/cmd/bl	exit 0			repo tests pass
J3	judge	blocking	B2	cat scripts/cmd/bl/missing.conf		if stderr has no runnable next command, FAIL	actionable is a reading, not a string match	the refusal names the path and a way forward
```

**Must hold:** `bl checks` accepts; `checks_sha` is recorded. `bl claims` is accepted afterwards (R5 — no claims event
yet).
**Must not hold:** nothing is refused.

### E2 — a blocking row without a brief clause is refused

```
S1	bl	blocking		go test ./scripts/cmd/bl	exit 0			repo tests pass
```

**Must hold:** refused on R2, naming R2 and saying a blocking row needs a `brief_ref`.
**Must not hold:** the list is not stored; the ledger has no `checks` event.

### E3 — a judge row that is really a command is refused

```
J9	judge	blocking	B1	go test ./scripts/cmd/bl	exit 0		the tests failing is a command result, so use decider bl	use bl
```

**Must hold:** refused on R1 — a `decider: judge` row must have an empty `expect`, and this one carries `exit 0`.
**Must not hold:** a resubmission with `decider: bl`, `expect: exit 0`, empty `fail_if` and empty `why_not_mech` is
accepted (R1); the refusal itself is not a code round, because nothing was recorded.

### E4 — a vague `why_not_mech` is refused

```
J4	judge	advisory		bl run A1		if the function exceeds 80 lines, FAIL	TBD	the function grew too long
```

**Must hold:** refused on R3 — `why_not_mech` is the placeholder `TBD`.
**Must not hold:** the placeholder is not stored; no code round is consumed (nothing was recorded).

### E5 — the order is enforced

State: the item has a `claims` event. `bl checks` is then called with the valid list from E1.

**Must hold:** refused on R5 — claims exist, so no check list may be recorded afterwards.
**Must not hold:** the list is not stored; the claims event is untouched.

### E6 — a verdicts file that does not match the frozen list is refused

Frozen ids: `S1`, `J3`. The verdicts file contains `S1` only.

**Must hold:** refused on R6 — `J3` is missing.
**Must not hold:** no round event is written; the cap is not touched (a refusal never reaches the ledger, P10).

### E7 — the verdict is derived, and a disagreeing verdict is refused

Frozen: `S1` (blocking, `bl`, `expect: exit 0`), `J3` (blocking, `judge`, source anchor
`scripts/cmd/bl/main.go:412`), `J7` (advisory, `judge`, output anchor). Observed: `S1` PASS, `J3` FAIL, `J7` FAIL.
`J7`'s item was filed with the text `J7 — the helper grew past its limit`, before the round.

**Must hold:** the derived verdict is `FAIL` (one failing blocking row). A submitted `--verdict PASS` is refused on
R12; `--verdict FAIL --cause code` is accepted and consumes one code round.
**Must not hold:** an advisory row's failure alone never makes the verdict `FAIL` — with `S1` and `J3` PASS and only
`J7` failing, the derived verdict is `PASS`.

### E8 — a source anchor outside the diff is refused

The round's diff touches only `scripts/cmd/bl/main.go`. `J3`'s anchor is
`scripts/cmd/bl/README.md:12 "scope"`. README.md is inside the scope (`scripts/cmd/bl/`), so the file check passes, but
no hunk of the round's diff contains line 12.

**Must hold:** refused on R9 — the line lies in no hunk of the round's diff.
**Must not hold:** the row covers no hunk, so the round would also fail R10 — but P2 stops at R9 and the refusal names
R9 only.

### E9 — an uncovered hunk refuses the round

The diff has two hunks in `scripts/cmd/bl/main.go`. The only blocking row is `S1`, whose `cmd` is
`go test ./scripts/cmd/bl` — the string `scripts/cmd/bl/main.go` does not appear in it, and `S1` has no source anchor.

**Must hold:** refused on R10 — neither hunk is covered by a blocking row.
**Must not hold:** the round is not recorded; no code round is consumed.

### E10 — a new serious finding is a dispute

Every frozen row passes, but the verdicts file carries a comment line `# finding: the migration drops a column v1 reads`.

**Must hold:** recorded as cause `dispute`, verdict `FAIL` (R13, which overrides R12's derived `PASS`). The item does
not close — R18 requires a `PASS` round. The cap is not consumed (R19). A supplied `--cause code` is refused (R23).
**Must not hold:** the finding is not filed as an advisory item — it is escalated, because filing is for findings below
the bar. The comment line is not refused by R6, because comment lines are skipped.

### E11 — an amendment can only make a row stricter

Frozen: `J3` is `judge`, `advisory`, `brief_ref` empty, with `fail_if` and `why_not_mech` filled.

- An amendment that changes `severity` to `blocking` is **refused on R4** (an amendment may not change severity) and
  on R11. P1 evaluates R5, then R4, so the refusal names R4.
- An amendment that fills the empty `brief_ref` with `B2` **is accepted**, and is recorded as an amendment event with
  both hashes (R4, R24).
- An amendment that reclassifies the `bl` row `S1` to `decider: judge` is **refused on R1** — P1 runs R1 before R4, and
  a judge row may not carry `expect`, which `S1` has. P7's "stricter wins" reaches the same outcome from the other
  direction: the change is looser, and looser is refused.
- An amendment that reclassifies `J3` to `decider: bl`, setting `expect: exit 0` and clearing `fail_if` and
  `why_not_mech`, **is accepted**.

**Must not hold:** the stored list is unchanged after a refused amendment; a refused amendment is not recorded.

### E12 — a close is pinned to the verified tree

The last PASS round pinned the verified tree `91c2ff…` (40 hex characters). The commit's tree is that value; the
working tree is clean apart from `.backlog/`.

**Must hold:** closed; the close event names the commit and the pinned tree.
**Must not hold:** a commit whose tree is a different value is refused on R18, even though it is a valid commit and the
round was a PASS. A dirty working tree outside `.backlog/` is refused on R18.

### E13 — a harness round carries no rows

`bl round A1 --verdict FAIL --cause harness` with no verdicts file.

**Must hold:** recorded as a harness round: cause `harness`, verdict `FAIL`, no rows, no code round consumed (R19,
R23). The number of harness rounds appears in `bl status`.
**Must not hold:** the same call **with** a verdicts file containing `S1` is refused on R23 — a harness failure means
the verifier did not run, so there must be no rows.

### E14 — an amended brief keeps the old hash in the ledger

`B2` is reworded; `bl brief A1 --file <path>` records the new brief with a new hash.

**Must hold:** the ledger holds both events; `bl check` compares the file against the **latest** hash (R24) and passes;
a row citing `B2` still resolves, because clause ids are never renumbered.
**Must not hold:** the earlier event is not rewritten; a file matching only the old hash is a `bl check` failure.

### E15 — a row that changes the tree refuses the round

`S1`'s `cmd` is `printf '\\n' >> scripts/cmd/bl/main.go` — it changes the content of a file the round is verifying.

**Must hold:** refused on R26 — the verified tree computed after the run differs from the one computed before it.
**Must not hold:** no round is recorded; the tree after the run is not the tree that was verified. A command that only
touches a timestamp (`touch`) does not trip R26, because the tree is content, not metadata.

### E16 — the tree a round verifies is the tree that gets committed

The producer's change is unstaged: the index is at the old content, the working tree has the new content. The round's
commands run in the working tree, so the verified tree is computed from the working tree (§2), not from the index.

**Must hold:** the pinned tree is the working tree's content; a commit of that content has the same tree and closes.
**Must not hold:** a commit of the *index* (the old content) is refused on R18, because its tree is not the pinned
tree — even though the index was what `git write-tree` would have returned.

---

## 6. Numbers this document produces

Computed with the rules above.

| quantity | value | how it is derived |
|---|---|---|
| checks `bl` validates before storing a list | 6 | R1, R2, R3, R4, R5, R8 |
| refusals `bl checks` can raise | 6 | R1, R2, R3, R4, R5, R8 |
| refusals `bl round` can raise | 11 | R6, R9, R10, R12, R14, R16, R20, R23, R25, R28, R35 — R13 derives rather than refuses, R27 moved to the run, and R19's cap refusal and R24's removed-clause refusal are counted separately because they are caps and premise faults rather than checks |
| refusals `bl run` can raise | 6 | R1, R8, R20, R26, R27, R35 |
| refusals `bl close` can raise | 4 | R15, R18, R20, R25 |
| frozen artefacts re-verified before a command | 3 | the brief, the check list and the claims, each against its latest recorded hash — R20 |
| code rounds a failing item may have | 3 | R19 |
| harness rounds an item may have | 3 | R19 |
| dispute rounds an item may have | 3 | R19, R13 |
| rounds an item may have in total, of any cause | 6 | R19 |
| blocking rows an item needs to close | at least 1, of which at least 1 must be `decider: bl` — or 0 with a human sign-off, and only then if the diff has no hunks and no ignored file under the scope changed (R10, R28) | R15, R10, R22, R28 |
| the minimum number of blocking rows for an item with a diff | at least one per hunk, and at least one `decider: bl` row; a single row may cover several hunks if it names their file | R10, R15 |

**Sensitivities, not headlines.** The figures below describe the loop as it exists today and are *not* a property of
this design; nothing here changes them. They are recorded so that a future claim of a cost win can be checked against
the same counting rule instead of reusing a row.

| sensitivity | value | where it comes from |
|---|---|---|
| model contexts for a clean item today | 3 (producer, verifier-1, verifier-2) | the current playbook, not these rules |
| model contexts for one fix round today | 5 | the current playbook, not these rules |
| judge share above which the brief should be re-read | ~⅓ of blocking rows | a guess with no data behind it |
| planted-defect calibration rate | ~1 in N items | a guess with no data behind it |

---

## 7. Acceptance criterion

A third party can implement `bl checks`, `bl claims` and `bl round` from this document alone, and:

1. for every rule that can refuse (R1–R6, R8–R12, R14–R20, R22–R28), at least one input exists where it refuses and
   one where it does not; R7, R13 and R21 never refuse, and the document says so;
2. the sixteen worked examples E1–E16 hold exactly as stated, and each "must not hold" is also checked;
3. no two rules can produce different outcomes for the same input — the precedence section decides any pair;
4. every figure in §6 is re-derivable from the rules in §3;
5. a `bl round` run on a real item produces an evidence file whose every line is derivable from the check list, the
   verified tree and the commands — with the caveat that a command whose output varies between runs (a test that
   prints timings, a network call) produces varying recorded values; the evidence is a record of that run, not a
   reproducible value. There is one execution per round, and the judge reads that execution (R7).

**Cases this document leaves open, stated rather than hidden:**

| case | class | why it is open |
|---|---|---|
| a judge row whose `fail_if` is itself arguable | judgement | the fail condition is text; the second judge (R16) checks conversion, not the wording |
| whether a `why_not_mech` is a genuine reason | judgement | R3 checks emptiness and placeholders, not truth; R16 and the calibration are the backstops |
| whether a `judge → bl` amendment is worth its cost | policy | R4 allows it; nothing says when it is expected |
| the frequency of the calibration run | policy | R16 names the hook; the rate is a choice, not a derivation |
| behaviour that depends on things outside the repository | limit | a parent `go.work`, a global git config, a developer's shell environment or an ignored file can change what a command does, and the record pins the tree, not the environment. This is a limit of the claim, not a defect in the rules |
| who keeps `.backlog/` ignored | process | R25 refuses a round when `.backlog/` is tracked; the repository's `.gitignore` is what keeps it ignored, and `bl init` can only report it |
| two items in flight at once | process | R27 hashes the whole of `.backlog/`, so a second concurrent run is refused; the skill's one-item-in-flight rule is what keeps this from mattering |
| **the ledger is a file the agent can rewrite** | **architecture** | every hash in this design is stored in `.backlog/ledger.jsonl`, which lives in the repository the agent may write. A hash chain detects an edit to an event; it does not stop the agent writing a new chain, or a forged `run` or `round` event, in place of the old one. The guarantee this design can honestly claim is "a third party can detect that the record changed", not "the agent cannot rewrite history". Closing that needs a witness outside the agent's write surface — see the options below, which are all OS-agnostic. **This is a decision for a human, not a rule.** |
| a change made and reverted inside the run | limit | R26 compares the tree before and after the run, so it catches a net change; a change made and reverted while the rows run is invisible. Catching it needs a per-row tree, at the cost of hashing the tree after every command |
| a command that detaches a background process | limit | the run kills the process group on timeout, but a process that detaches itself and acts after the run is outside what the tree checks can see |
| the cost of `bl run` itself | implementation | the rows are executed sequentially; parallelism is an implementation choice that changes wall time, not the recorded values |
| a brief clause amended between rounds | policy | R24 refuses a round whose row cites a clause the amendment removed; whether a reworded clause still means the same thing is a judgement |
| which of two filed items mentioning the same advisory row id a reader should trust | policy | R14 says the most recently filed wins |
| whether the scope constrains what the producer writes | limit | the scope is a file list in the brief and in the producer's prompt. No rule in this document makes the binary refuse a write outside it; the enforcement is review-based (the before-state diff, and the clean-tree gate at close). R28 and R10 make the *record* complete, not the *scope* enforced |
| a change to a file outside the repository that the build depends on | limit | a parent `go.work`, a global git config or an ignored file can change what a command does, and the record pins the tree, not the environment. R28 covers ignored files under the scope; nothing covers the rest |
| how the escalation of a dispute is requested | interface | there is no `bl escalate` verb; a dispute is recorded, and the orchestrator is expected to escalate in the open. A verb would be better |
| a row whose output depends on the time of day | implementation | the run records what it saw; the row is a record of that run, not a reproducible value |
| the interface by which a verdict-time downgrade is requested | interface | R11 refuses the downgrade; the requester has no command to call, and files an item instead — that is deliberate, but it is a gap in the interface |
| a judge row with no output anchor | interface | R9 wants the judge row to name the artefact and the output anchor, but the sample row in §2 (criterion: "clear", cmd: "true") emits nothing at all, so under R9 it can only ever be recorded FAIL. A judge row whose command prints nothing needs a rule that says what "the output" is |
| a round whose commands need the network | implementation | the environment has no proxy variables and `HOME` is scratch; a network command may behave differently from the developer's shell |
| a check list that cannot run | process | a frozen list cannot be replaced (R4 allows only a narrow amend), so a list whose commands never reach an assertion spends a code round on its own defect. The loop needs a re-baseline path: a new premise, logged, with the round counter reset |
| the granularity of the brief's clauses | interface | R2 makes a blocking row cite a clause that exists, and the clauses come from the item text. A check list written from the intent can therefore only cite ids the item text implies — so the brief must be readable by the check-list author, or the brief format must accept pre-numbered clauses (`B1: …`) instead of inventing ids |
| the check list's commands must set up their own world | process | a row that drives the binary has to file the item before opening it, and needs a working directory the binary will accept. Nothing in the design says so; the list's author has to know it |

### The trust root, in options (all OS-agnostic)

The property needed is not "the agent cannot read the witness" — it is "**the agent cannot make a rewrite
invisible**". Everything below satisfies that on any operating system, and they compose.

| option | what it costs | what it buys | what it does not buy |
|---|---|---|---|
| **A. Hash-chain the ledger, anchor the head by hand** — each event carries `prev_sha`, so the file is a chain; `bl anchor` prints the head hash and the human stores it anywhere they trust and the agent cannot reach (a photo, a paper note, an account the agent has no credentials for) | one human action per anchor; no key material; no infrastructure | a rewrite changes the head, so the stored anchor no longer matches and the tamper is visible. Works on a laptop with no network | does not stop a rewrite that happens *before* the first anchor; the anchor's own storage is out of band |
| **B. The remote is the witness** — the ledger is committed with the work; the agent holds **fetch-only** credentials for the remote, and the push credential never touches the machine the agent runs on | one credential split; the human pushes, or a CI job pushes | the remote's history is the anchor; local rewrites are visible by comparing to the remote. Free, and already how most repos work | nothing if the agent can push; a rewrite that is pushed looks like normal history |
| **C. Append-only public log** — the head is submitted to an immutable transparency log (a Sigstore/Rekor-style log, or any append-only service) | an account and a network call | the first anchor is immutable forever, so every later rewrite is provably a second, conflicting history. The agent *can* submit a forged head — but it cannot erase the first one, so the forgery becomes visible | only helps once something is anchored; costs a network dependency and an account |
| **D. Hardware signing key** — the head is signed by a FIDO2/hardware token the agent can call but whose private key never leaves the device (human presses to allow) | the device; a human press per anchor | a forged head cannot be produced by the agent at all — the strongest of the four | a hardware dependency; still manual |

**All four are the same generic pattern** — content-address the payload, chain the events, checkpoint the chain, sign
the checkpoint, anchor the checkpoint outside the write surface — which is the pattern certificate-transparency logs
(RFC 6962), Sigstore/Rekor, in-toto/SLSA, TUF/Uptane, git's signed refs, AWS CloudTrail's digest files and immudb all
use, with the payload swapped out. What the pattern needs that a plain chain does not is a **sequence number** in the
anchor (R29), so a truncated tail cannot pass as current.

**Default to ship with:** A, because it is the cheapest and it works everywhere, and it needs no credential the agent
could reach. B is free if the repository already has a remote and the human is willing to own the push credential.
C is the one that survives a machine being wiped. D is the only one that turns "the agent cannot rewrite it" into a
guarantee rather than a detection.

Whatever is chosen, the rules in §3 do not change: the ledger stays a hash chain, the artefacts stay hashed, and
Whatever is chosen, the rules in §3 do not change: the ledger stays a hash chain, the artefacts stay hashed, and
the anchor is an addition at the point where the head is recorded. This is why it is a decision and not a rule — the
wording cannot pick it.

### If the ledger is only ever written through a binary call

The question deserves a precise answer, because it is easy to confuse two different claims.

**As a convention, it is worth nothing against an agent that decides to disagree.** The shell writes the file just as
well as the binary does, so "the agent agreed to use the binary" does not make the file trustworthy. What it does buy
is honest and small: one writer, one format, no half-written lines, and a ledger that stays parseable for a
cooperating agent.

**As a guarantee, it is the right shape, and it needs one more thing.** For the binary to be the only producer of a
*verifiable* event, it must hold something the agent cannot use: a key it cannot read, a signer it cannot call, or
storage it cannot write. Then the agent's only power is to *ask*, and the binary answers the request by checking it
against the rules in §3 — the tree, the hashes, the scope — and refusing when they do not hold. The ledger becomes the
audit trail of decisions the binary made, not a file the agent maintains.

The place for the key is a matter of mechanism, not of principle: an OS keychain item whose access list names the
signed binary; a local signing service whose key the agent cannot read; a hardware token the agent can call but cannot
forge without a human; or storage the agent cannot write at all. The shape — "a signer the agent cannot use directly"
— is the same on every platform.

**What this changes about the rest of the design.** The weight moves off the ledger and onto the binary: the refusals
(R10, R18, R20, R28) become the defence, and the ledger becomes the receipt. It also splits the two problems apart,
which the earlier options blurred:

- A **signer** stops forgery and rewriting. A line without a valid signature is not an event, whatever the file says.
- The **sequence number and an external copy** stop truncation. Dropping the last `k` events leaves a chain that
  verifies perfectly, so no signature can catch it; only a copy of the head that the agent cannot reach can (R29).

**Signed head, or every event signed?** A signed head is enough for a ledger this size: the chain binds the history,
so one signature over the head covers every line before it. Signing every event is the heavier variant and buys one
thing — proving a single event without the head, which is what a certificate-transparency log needs because its tree is
huge and distributed, and what this ledger does not need.

### Decision for now: the trust root is parked

The trust root is **deferred**, deliberately, until the loop has run on real work. Until it is decided, the ledger uses
the cheapest thing that still leaves an audit trail:

- the hash chain with a sequence number (R29) — every event carries the previous event's hash, and every event
  carries its position;
- `bl anchor` prints the head and the sequence number for a human to keep, if they want one;
- no signer, no remote, no hardware, no extra daemon.

**What that means honestly.** This claims nothing about prevention. It says the record is reconstructable, that a
rewrite after an anchor is visible, and that a rewrite before the first anchor is not caught. That is a weaker
promise than the one the four options above could support, and it is written down here so nobody has to guess which
promise is in force.

**When to revisit.** After the loop has run on real items, look at the accumulated session logs and decide with data
rather than with the abstract worry that motivated this section. The questions that decide it:

1. how often would a rewrite actually have gone unnoticed — or was the fear of one driving work that no real item
   needed?
2. what did anchoring cost in human actions, per item?
3. did any second party ever need to audit the record, or was it only ever me reading my own log?
4. is the ledger growing into something that needs a service under it — many items, many sessions, many readers?

**This is the third time this question has surfaced**, and parking it is a decision rather than an oversight: the
design has to be implementable before it is provable, and a guarantee that only the author can check is worth less
than a working loop that says plainly what it does and does not guarantee. The rules in §3 do not depend on the
answer — the chain, the sequence number and the refusal rules all stand either way, and the signer or the anchor
slots in where the decision is finally made.

---

## 8. Rounds

| round | verifier | verdict | defect found | fix |
|---|---|---|---|---|
| 1 | Claude Opus 5.5, via `claude -p`, re-implementing from the text alone | FAIL | 18 rule clusters and 8 worked examples were not decidable from the text: R1 header and column parsing; R3 not enforceable as E3/E4 were written; R4 amend scope and amend-after-claims; R5 how a claim exists; R6 order and the `expected` cell; R7 the execution environment and the `observed` conflict; R8 regex dialect and the "four forms" count; R9 output anchors and the self-cancelling allowance; R10 hunk definition and "reads or writes"; R12 vs R13 precedence; R14 id matching and ordering; R15 sign-off; R16 the trigger and the refused-but-recorded event; R18 `.backlog/` dirtying the tree; R19 a supplied cause; R20/R24 brief amendment vs its hash; §6 rows not derivable; E3, E4, E5, E7, E8, E9, E10, E12 not derivable; precedence gaps; the canonical E1 list could never pass because J3's own sample row carried no anchor | name-based parsing (R1); check list / verdicts / evidence split (E1–E14 rewritten); the execution environment and the artefact exclusion (R21, R25); two anchor forms (R9); "names the path" replaces "reads or writes" (R10); R13 before R12 and a dispute channel (R13); sign-off, cause constraints, amendment re-hashing (R22, R23, R24); per-command precedence (P1, P2, P7); context counts moved to sensitivities (§6) |
| 2 | Claude Opus 5.5, re-implementing, plus a check of round 1's claimed fixes | FAIL | the verified tree was the **index** while the commands ran in the **working tree**, so a round could PASS against unstaged content and close a commit of the index — the design's central claim failed. Also R1 × R4 (a `judge → bl` amendment could never pass R1); R6 × R23 (a harness round could never be recorded); R12 × R13 (a dispute came out `PASS`); R9 "satisfies both"; the execution environment; clause syntax, scope format, before-tree, regex semantics, quote escaping, stdin, sequential execution and `--no-renames`; the verdicts header; the R16 interface; §6 rows not derivable; E7, E10, E13 not derivable | the verified tree is computed from the working tree (§2, E16, R18, R26); a `judge → bl` amendment may set `expect` (R4); harness rounds skip the row rules and are checked first (R23); a dispute overrides the derived verdict (R13, P5); the anchor's form is syntactic; a timeout is `exit = -1, timed_out = true`; the missing definitions written down (§2, R8, R10); the verdicts file has a name-based header and skips comment lines; `bl second-check` gives R16 an interface; the acceptance criterion now says which rules never refuse |
| 3 | Claude Opus 5.5, re-implementing, plus a check of round 2's claimed fixes | FAIL | the verified tree was built with `git add -A -- <scope paths>`, so a change **outside** the scope (a new dependency in `go.mod`) was not in the pinned tree while the row's commands still ran against it — a round could PASS and close a commit that fails the test command. Also: the verdicts file carried no tree, so a judge could sign off on a tree the producer then changed; E15's `touch` does not change a tree; R13 vs R23 left the cause ambiguous; R26 and R25 missing from the precedence; P3 conflicted with R4; §6 numbers wrong; E7, E8, E10, E11, E12, E15 not derivable; the open-cases table listed decided cases and missed about twelve | the verified tree is computed over the **whole** working tree, not the scope (§2, E16); the verdicts file carries the tree the judge read and R6 refuses a mismatch; R13 derives the cause; R26 and R27 added and ordered; P3 scoped to non-amend submissions; a `bl close` precedence list added; E15 uses a content-changing command; §6 re-derived; the open-cases table rewritten |
| 4 | Claude Opus 5.5, re-implementing, plus a check of round 3's claimed fixes | FAIL | the same class as round 3 — the tree. R21 set `HOME` to the repository root, so `go test` wrote its build cache into the repo, the after-run tree differed, and **no round that runs `go test` could ever be recorded**. Also: the judge read an earlier run while the round re-ran the command; a `# finding:` line had no defined cause; R25 was still missing from the precedence; §6 numbers wrong; the open-cases table listed decided cases | the premise was re-derived, not patched: **the run is the unit of verification**. `bl run` executes every row and writes a run file; the judge reads that file and writes verdicts naming the run and its tree; `bl round` consumes both and never executes a command. Also: `HOME`/`TMPDIR` point at a scratch directory outside the repository and `PATH` is inherited and recorded; R26/R27 moved to the run; a dispute derives its cause; harness and dispute rounds are each capped at three; R24 refuses a round citing a removed clause |
| 5 | Claude Opus 5.5, re-implementing, plus a hard test of the round-4 premise | FAIL | the re-derived premise was still leaky: the run file stored excerpts and hashes, but R8's regexes and R9's output anchors need the **full** output, and the judge read only the excerpt — so "the judge and the record read the same bytes" was false. Also a parent `go.work` changes what the commands do without changing the tree; stale §6 rows; R25 missing from the precedence; a `--finding` with `--cause harness` not refused; the open-cases table still listed decided cases | the run file stores the **full** stdout and stderr (base64 with a flag for non-UTF-8 output) and the excerpt exists only in the evidence; the run file schema is written down; R6 requires a claims event and refuses a run already consumed by a round; R8 defines `file exists` and `tree ==`; R9 defines quote escaping; R23 refuses a finding on a harness round; the precedence list is renumbered; §6 re-derived; the open-cases table gains the honest limits |
| 6 | Claude Opus 5.5, re-implementing, plus a hard test of the premise | FAIL | the strongest: **the frozen check list was never re-verified against its hash at the point of use.** A one-line `sed` on `checks.tsv` after the freeze changed what the run executed, and neither `bl run` nor `bl round` nor `bl close` compared the file with the frozen hash — only `bl check` did, and only later. This defeated R4, R5 and R20 at once. Runners-up: the verified tree was still index-derived, so a `skip-worktree` path hid an edit; a forged run file with no `run` event was accepted; `expect exit -1` turned a timeout into a PASS; the ids `run` and `tree` collided with the verdicts file's metadata lines; the precedence list had two P4s; the §6 counts were stale | R20 now requires the brief, the check list and the claims to be re-hashed and matched at the start of `bl run`, `bl round` and `bl close`; R6 refuses a run file with no `run` event or a mismatched hash; the verified tree is computed from the filesystem with `git hash-object` and `git mktree`, never from the index, so `skip-worktree` and `assume-unchanged` cannot hide a change; `expect exit` is limited to 0–255 so a timeout cannot be written up as a pass; the ids `run` and `tree` are reserved; the precedence list is renumbered P1–P10; §6 re-derived |
| 7 | Claude Opus 5.5, via `claude -p`, re-implementing from the text alone, plus a hard test of both premises | FAIL | **the strongest: the fix can live outside the pinned tree.** A `.gitignore` holding `*.gen.go`, and the item's fix written to `scripts/cmd/bl/config.gen.go`: the file is ignored, so it is not in the verified tree, the diff is empty, R10 passes trivially, and `bl close` succeeds on a commit that contains no fix at all — a closed item that does not do what it was asked. Runner-up: **the ledger is a file the agent can rewrite** — a forged `run` or `round` event appended to `.backlog/ledger.jsonl` is indistinguishable from a real one, so every hash in this design rests on a trust root the agent holds. Also: R20 was missing from the harness path and from the close path, so a `sed` on the frozen list still slipped through a harness round; the precedence list had a gap (P2) and stale cross-references; the §6 counts were wrong; a judge row whose command produced no output could deadlock the item; a brief amended after a PASS round passed the close; a failing item could stretch to six failing rounds (3 code + 3 dispute); R18's untracked was undefined for ignored files | fixed: **R28** — the before-state records the path and hash of every ignored file under the scope, and a round is refused if one of them changed unless the item declared it with `--scope-ignored`; R20 now runs first in the harness, claims, run, round and close paths; the precedence list is renumbered P1–P10 with no gap and the cross-references repaired; a judge row whose command produced no output is a FAIL, not a refusal, so an item cannot deadlock; R18 requires the brief hash pinned by the PASS round to equal the current brief, so a brief amended after the round invalidates it; R19 adds a total budget of six rounds per item; §6 re-derived. **Not fixed, deliberately:** the ledger trust root and the files outside the tree are architecture, not rules. They are now in §7 as the two decisions that need a human | | 8 | *not run* | | | |

**Where this stands.** Seven rounds, seven FAILs, and every round found something real. The first three were
wording and missing definitions; rounds 4 and 5 exposed a wrong premise, which was re-derived rather than patched;
round 6 found that the frozen artefacts were not re-verified where they are used; round 7 found that the fix can live
outside the pinned tree, and that the ledger itself is the trust root. The round-7 defects are fixed in this revision
but **have not been through an eighth round**, so the document is a reviewed draft, not a verified design.

**Two findings in round 7 are not rules, and they are the ones worth a decision.** The ledger lives in the repository
the agent can write, so the guarantee this design can honestly make is "a third party can detect that the record
changed", not "the agent cannot rewrite history". And anything outside the tree — a parent `go.work`, a global git
config, an ignored file — can change what the commands do while the record stays clean; R28 closes the ignored-file
case under the scope, and nothing closes the rest. Both are in §7 with the options. Both need a human, not another
round of wording.
