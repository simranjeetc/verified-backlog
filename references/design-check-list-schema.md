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
| test file | `.backlog/items/<id>/tests.py` | `bl checks --tests`, `bl rebaseline --tests` | yes — `tests_sha` (format 2, when one is frozen) |
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

**Format 1 and format 2.** A list is **format 1** or **format 2** (I89, R37). Every `checks` and
`rebaseline` event written after R37 took effect carries `format` as an integer; an event written
before it carries none and is format 1. `bl checks` without `--amend` writes format 2, and
`--amend` keeps the format of the list it amends. `bl rebaseline --tests` writes format 2;
without `--tests` it keeps the list's format. Format 1 is the list exactly as this document first
described it; format 2 gives a `decider: bl` row one of two shapes, a **test row** (a call of the
frozen test file) or a **shell row** (one simple command), and R37 defines both. A format-2 list
may also freeze a **test file**, beside the check list, whose hash the event records as
`tests_sha`.

**The test file (format 2).** `.backlog/items/<id>/tests.py` is a python3 file the verifier writes
before the code exists. It holds one named function per assertion and a fixed main block, and it
is driven by the rows: a test row's cmd is `python3 .backlog/items/<id>/tests.py <name>`. `bl`
copies the supplied file, hashes it into the list's event, parses it with `ast` and probes it
before the list is frozen; R37 states the contract, the template and the refusals.

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

**The order of work (I79).** Three events order the work; none changes an item's state, and a ledger without them
parses as before.

| event | written by | fields | `by` |
|---|---|---|---|
| `bump` | `bl bump <id> [<id> ...]` | `ids`: the ids in the order given | `human` |
| `unbump` | `bl unbump <id>` | `ids`: the id taken out of tier 1 | `human` |
| `rank` | `bl rank --file <tsv>` | `ranking`: every `{id, reason}` of the file, in rank order | `loop` |

`bl file --urgent` records `urgent: true` and `by: human` on the item's own `file` event, and counts as a `bump` of
that one id at the point it was filed — one call, one append. `bl bump` and `bl unbump` refuse an unknown, closed or
declined id and an id named twice; `bl rank` refuses those too, plus a tier-1 id and a line with an empty reason. A
refusal writes nothing.

`bl next` names the item in flight if there is one. Otherwise it names the first eligible item (open, and not waiting
on an unfinished `--after` dependency) in this order:

1. **Tier 1, the human's priority list.** Every `bump` event newest first, and within one event the order given; each
   id counts at its first appearance, so a later bump overrides an earlier one. An `unbump` newer than every bump of an
   id takes it out of tier 1; a later bump puts it back.
2. **Tier 2, the loop's ranking.** The ids of the latest `rank` event, in order. A later rank replaces the earlier
   ranking as a whole; both stay in the ledger.
3. **Tier 3, the rest.** Every other item, ordered by how many open items name it in `--after` (more first), then by
   filing order.

A parked, closed, declined or blocked item is never named, whatever its tier, and does not stop the next item from
being named. The triage pass that produces a ranking is out of scope.

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
`--amend`, which may do exactly four things: reclassify a row from `decider: judge` to `decider: bl` — which also
requires setting that row's `expect` and clearing its `fail_if` and `why_not_mech`, because R1 would otherwise refuse
the result — fill an empty `brief_ref` on an advisory row, ADD a row, and (format 2 only) replace the frozen test file
with a file that inserts text at exactly one point, immediately before the frozen file's main-block line (R37). An
amendment never changes the `id`,
`severity`, `cmd`, `expect`, `criterion`, `fail_if` or `why_not_mech` of a row that is already in the list (except
that clearing them is part of a `judge → bl` reclassification), and it never removes a row. An amendment never
reorders rows.
A row added by an amendment is a row like any other: R1, R2, R3, R8 and R34 apply to it, and its `id` must not be
one the list already holds. (In a format-2 list R37 governs a `decider: bl` row's expect and command, and R8 applies
only to a format-1 list — see R37.) It is how a defective row is corrected — the added row asserts the thing the defective
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
`stdout matches <regex>`, `stderr matches <regex>`, `file exists <path>`, `tree == <hash>`. Regex expects ignore one
trailing newline. Any other form is a
refusal at `bl checks` time, and again at `bl run` time when a frozen row carries one, because the run computes that
row's verdict from it. The regex dialect is Go's `regexp` (RE2), applied with search semantics (not a full
match) to the **full** captured output, not the 4096-byte excerpt. `<code>` is an integer between 0 and 255 — `-1` is reserved for the timeout and is not a valid expectation, so a
timeout can never be written up as a pass; `<path>` is relative to the
repository root, may not be absolute, may not contain `..`, and `file exists` is true when `stat` succeeds for it at the moment the
row runs, be it a file or a directory; `<hash>` is a 40-character lowercase hex tree value — the before-tree is the only tree a
row can know when the list is written, and `tree ==` against the verified tree can only be written in a
`second-check`.

R8's text is unchanged, but R8 applies to a **format-1** list only. In a format-2 list a `decider: bl` row's `expect`
is R37's (`exit 0` for a test row, `exit <code>` for a shell row); a `decider: judge` row carries no `expect` in
either format.

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
- its `cmd` contains the hunk's file path as a literal substring; or
- (format 2) it is a test row whose function carries `@_covers("<path>", ...)` with a string-literal argument equal
  to that file's path as a whole string.

The comparison is literal: a row that exercises a file without naming it covers nothing. A check list written to
drive a binary never names a source file, so on its own it leaves every hunk uncovered and no round can be recorded
at all. The path belongs in the command, next to the thing it exercises — or, for a format-2 test row, in the
`@_covers` decorator, because its cmd names only the test file and its function (R37). A test row covers nothing
through its cmd, so a repository file whose path is a substring of the cmd is covered only by `_covers` or by another
row.

A file that is added, deleted, or mode-changed without any textual hunk (a binary file, an empty file) is covered
only by a blocking row whose `cmd` names that path, or by a test row's `@_covers` naming it.

An uncovered hunk refuses the round. Advisory rows do not count towards coverage.

A docs clause is covered by a blocking `decider: judge` row. The verifier reads the documentation
against the clause's example and places its `<path>:<line>` anchor in the changed hunk; R10 then
counts that hunk as covered. A format-2 blocking test row whose `@_covers` arguments are all
`.md` paths is refused under R37 when the list is frozen or replaced. A blocking test row must
exercise code, so use a judge row for a docs clause.

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
frozen test file, the run file and the evidence file are each hashed (sha256) and the hash is stored in the ledger event
that references the file. `bl check` re-hashes every one of them and fails if any file no longer matches its recorded
hash — except the test file, which `bl check` reports as a problem line (`FAIL <id> tests <path> is missing` or
`does not match its hash`) and keeps checking the other items.

More important: `bl run`, `bl round`, `bl claims` and `bl close` each start by re-hashing the item's brief, check list
and claims — and the test file, when the latest `checks` or `rebaseline` event carries `tests_sha` — and, for
`bl close`, the evidence file of the last round — and refuse if any of them differs from the hash
of the latest event that recorded it. A frozen artefact that was edited
after the freeze is therefore refused at the point of use, not merely detected later by `bl check`. A run file that
has no `run` event, or whose hash differs from that event's, is refused by R6.

The ledger is a chain of the same hashes. The ledger file's **lines** are its non-blank lines in order, one event
each; the bytes of a line are the bytes up to and including its terminating newline, or to the end of the file for a
last line that has none. The **chain hash through event *n*** is `chain(0)` = the sha256 of the empty byte string, and
`chain(n)` = the sha256 of the bytes of `chain(n-1)` followed by the bytes of the line holding event *n*. The **head** of
the ledger is `chain` through its last event, and the head's **seq** is that event's `seq`. Every event written after
this rule took effect carries `prev_sha`: the chain hash through the event before it — the genesis value, the sha256 of
the empty byte string, for the first event in a ledger. An event written before this rule took effect carries no
`prev_sha`; the first event that carries one commits to the whole history before it, because the chain hash through it
folds in every line before it, and every event after the first that carries one must carry one. `bl check` recomputes
the chain from the ledger file's bytes and refuses when an event's `prev_sha` is not the chain hash through the event
before it, naming the seq; it refuses when a `prev_sha` is not 64 lowercase hex characters, and when an event after the
first that carries one carries none.

**R21 — the execution environment is fixed.** The environment in §2 is the whole environment, with one exception:
`PATH` is inherited from the caller, and the inherited value is recorded in the run file so that the run can be
reproduced. No other variable is inherited, in particular not `GIT_*`, and `HOME` and `TMPDIR` point outside the
repository: they are one scratch directory per run, shared by every row of that run in list order and removed after
the run, so a row may reuse what an earlier row left there. The run file records each row's `ms`. A row that needs an
environment variable must set it inside its `cmd`.

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

**R29 — the anchor names a checkpoint in the chain, and the sequence number makes truncation visible.** A chain that
verifies is not enough, because an attacker who can rewrite the file can also truncate its tail and leave a shorter
chain that still verifies. The version counter is what makes truncation visible. (This is the lesson TUF/Uptane learned
with its version and expiry metadata, and the reason certificate-transparency logs publish a signed tree head rather
than just a tree.)

The **anchor** is a checkpoint of the chain: the ledger `seq` it covers, and the chain hash through that seq.
`bl anchor` writes it to `.backlog/anchor.json` — `{"seq": <n>, "chain_sha": "<64 hex>"}` — and prints the two values
as `seq <n>` and `chain_sha <hex>` on separate lines, so that a human can keep them where the agent cannot reach. The
anchor file is an artefact (R25): it is not part of any verified tree, and writing it never moves one. `bl check`
refuses when the anchor file does not parse, when the ledger's last `seq` is lower than the anchor's `seq` (the tail
was truncated), when the ledger holds no event with that `seq` (the ledger was rewritten), and when the chain hash
recomputed through the anchor's `seq` differs from the anchor's `chain_sha` (the history before the anchor was
rewritten). The anchor file lives inside the agent's write surface, so it catches accidental damage and a careless
rewrite, not a determined one; the copy a human keeps is the anchor that holds. (The trust root is parked — §7.)

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

R34 still applies to every `decider: judge` row, in either format, and to every format-1 row. It does not reach a
format-2 `decider: bl` row (R37): a test row holds no shell syntax, and a shell row is checked by R37's
simple-command scan, which reads quotes and backslashes exactly as R34's scanner does.

**R35 — only the item in flight is worked on.** A verb that works on an item — `bl run` and `bl round` — refuses
when that item is not the item in flight, and reports R35. The loop keeps one item in verification at a time, and
the lock is what keeps it that way: two items in flight at once would each hold a half-written tree, and the run
that pins a tree cannot tell which half it pinned. A refusal under R35 writes nothing.

**R36 — an attempt is a new premise, and a restart starts it.** `bl restart A1 --reason <class> --why <text>
--change <text>` ends the item's current attempt and starts the next one. `<class>` is exactly one of `bad-check`,
`extra-file`, `damaged-evidence`, `wrong-premise`; `--why` and `--change` are single-line texts. Every item starts in
attempt 1, each accepted restart adds 1, and the event records the attempt it **ends**, the record it wrote, and no
actor.

What a restart clears is everything that belongs to the attempt: the brief, the check list and its format, the frozen
test file, the claims it recorded, its attempt scope and before-state, and — for a closed item — the status (closed becomes
open), the commit and the reason. It keeps the id, text, source, `--after`, notes, priority and the whole event history. The item is open and out
of flight, so `bl next` can name it. The ended attempt's scope and ignored-scope lists are carried into the next
`bl open` unless either scope flag is given; explicit scope flags replace both carried lists. `bl open` records the
selected scope and before-state anew; `bl brief`, `bl checks`,
`bl claims`, `bl run` and `bl round` are then accepted, the last five only after `bl open`.

Everything the ended attempt produced is **void**: a void round, run or second check satisfies no rule of the new
attempt. `bl round` cannot cite a void run, a void PASS round cannot let `bl close` succeed, and every R19 cap and
every rule that reads the last round (R18, `bl close`, `bl check`'s closed-without-PASS test) reads the current
attempt's rounds only. Round and run numbers stay item-wide, so attempt 2's first round after attempt 1's r1 and r2 is
r3, with evidence `.backlog/evidence/A1-r3.txt`; `bl show` marks a void round ` void` and the `rounds:` summary and
`bl status` count the current attempt only.

Before it appends the restart event, `bl restart` writes the failure record of the attempt it ends to
`.backlog/items/<id>/a<n>-failure.md` and records the path and its sha256; no agent writes it. The record is exactly
these LF-terminated lines, with no timestamp:
`# Failure record: <id> attempt <n>`, `class: <class>`, `why: <why>`, `change: <change>`, `rows:`, one
`- <row id> exit <code>` per row that is blocking in the attempt's list and `FAIL` in the evidence of the attempt's
last FAIL round that carries rows, each followed by at most 20 output lines prefixed `  | ` taken from the run that
round names (stdout first, split on LF with a trailing empty segment dropped, a binary stream as the one line
`  | (binary output)`, and `  | ... (truncated)` as the 21st line when more were cut), then `artefacts:` and one
`- <path> recorded <sha256> now <sha256 or missing>` per path an event of the attempt recorded whose bytes no longer
match the hash of the latest such event. An empty section is `- (none)`.

The brief of attempt n ≥ 2 is the brief text followed by the failure record of every earlier attempt, oldest first,
byte for byte: `bl open` writes the item's brief and then the records, and `bl brief` stores the submitted file and
then the records, adding its missing final newline first. The restart itself writes no brief. Attempt n's files live
under `.backlog/items/<id>/a<n>/`; attempt 1 keeps the paths it always had, so no later attempt can rewrite an earlier
one's file.

`bl check` excuses the damage a failure record lists, and only that damage: a listed path passes while its bytes
still hash to the record's `now` value, or while it is still `missing` if the record says so, and the excusals of all
the item's records accumulate. Every other file of a void attempt is re-hashed against the latest event that named it
and a mismatch is a `FAIL <id> <kind> <path> does not match its hash` line (or `is missing`), exit 1, never a
refusal; the current attempt's artefacts are checked as today, and a failure record is verified with no excusal at
all, so an edited record is always reported. `bl restart` itself never refuses because an artefact
mismatches — the record it writes is what excuses the damage.

The refusals are checked in order, each prints `bl: refused`, and each leaves the ledger byte-identical: the id is
unknown; the item is declined; the item is parked (it needs `bl reopen` first, which does not change the attempt); the
item is closed and the class is not `damaged-evidence`; the class is missing or not one of the four; `--why` or
`--change` is missing or empty; `--change` equals an earlier restart's `--change`; the restart limit; and nothing to
learn from — the attempt has no FAIL round of any cause and no file the artefact scan would list (for a closed item,
only a mismatched or missing file counts). An item may be restarted twice, so it may have three attempts. A restart of
an open item in its third attempt **writes**: it appends exactly one `defer` event whose reason contains `restart
limit` and changes nothing else, so the item is parked for a human and leaves flight; a closed item in the third
attempt is refused with no write. A ledger with no `restart` event parses and behaves as it did before this rule.

The four worked examples:

- **E36a — the attempt ends and the next one starts.** A1 is in attempt 1 and its round r1 failed with cause `code`.
  `bl restart A1 --reason bad-check --why "row R3 expects ^ok$ and cannot match the trailing newline echo prints"
  --change "stdout patterns allow a trailing newline"` exits 0, prints `bl: A1 restarted as attempt 2`, and
  `bl show A1` prints `attempt: 2` immediately after its `status:` line, before `reason:`, `after:` and `source:`;
  `bl show --json` gives the item the integer field `Attempt`. An item never restarted prints no `attempt:` line. In
  attempt 2 the `bl open` that the claims refused under R5 is accepted, and `bl checks` then lands in `a2/checks.tsv`.
- **E36b — damage is excused once, by the record that lists it.** A1 closed after a PASS round r2, then
  `.backlog/evidence/A1-r2.txt` was overwritten: `bl check` exits 1 with `A1 round .backlog/evidence/A1-r2.txt does
  not match its hash`. `bl restart A1 --reason damaged-evidence --why w --change c` exits 0 and `bl check` then exits
  0, `bl show A1` prints `status: open` and no `commit:` line. Editing A1-r2.txt again, or editing attempt 1's run
  file, makes `bl check` exit 1 naming that file.
- **E36c — nothing to learn from is a refusal that writes nothing.** A freshly opened A1 with a brief, a check list
  and no round, nothing edited by hand: `bl restart A1 --reason bad-check --why x --change y` exits 2 and the
  ledger's sha256 is unchanged. The same holds for an unknown id, a declined or a parked item, a closed item whose
  class is not `damaged-evidence`, a class that is not one of the four, an empty or multi-line `--why`/`--change`,
  and a `--change` an earlier restart already used.
- **E36d — the limit parks the item.** A1 in attempt 3 with a FAIL round: `bl restart A1 --reason bad-check --why a
  --change b` exits 2, `bl show A1` prints `status: parked` and a reason containing `restart limit`, the ledger has
  exactly one more line, and `bl next` does not name A1. After `bl reopen A1` the same call parks it again. A closed
  item in attempt 3 is refused with no write at all.
**R37 — a format-2 row is a test call or one simple command, and the test file is frozen with the list.** Free-form
shell one-liners were the loop's biggest defect source: escaped pipes, `&&`, redirections, exact spacing, a `sed`
that matched nothing, a `$` that missed a trailing newline. A format-2 list replaces them with a black-box test file:
the blind verifier writes a python3 file, one named function per assertion, **before** the code exists; `bl` freezes
and hashes that file with the list; and a row is one call of one function. Shell rows remain only for whole-suite
commands such as `go -C scripts test ./...`.

A blocking test row must exercise code. When its function's `@_covers` arguments are all paths
ending in `.md`, `bl checks`, `bl checks --amend` and `bl rebaseline` refuse the list under R37,
naming the row and directing the writer to a docs `decider: judge` row or an advisory row. No
`@_covers` or at least one non-`.md` path is accepted, as is an advisory test row. This applies
when a list is frozen or replaced; existing frozen lists remain usable.

**The format.** Every `checks` and `rebaseline` event written from now on carries `format: 1` or `format: 2`. An event
written before this rule carries no `format` and is format 1. `bl checks` without `--amend` writes format 2;
`--amend` keeps the format of the list it amends. `bl rebaseline --tests` writes format 2; without `--tests` it keeps
the list's format. A format-1 list behaves exactly as this document described before R37: five R8 forms, R34, and the
amendment rules that do not involve a test file.

**The test file.** `bl checks <id> --file <tsv> --tests <py>` copies `<py>` to `.backlog/items/<id>/tests.py`. The
`checks` event, and a `rebaseline` event, records the path in `tests` and the sha256 in `tests_sha` **exactly when** a
test file is frozen after the command — supplied with `--tests`, or carried over by an amendment or a re-baseline
without `--tests`. An event that freezes no test file carries neither field.

**Row types.** In a format-2 list:

- **test row** — `decider: bl`, cmd **exactly** `python3 .backlog/items/<id>/tests.py <name>` (single spaces, `<name>`
  matching `[A-Za-z][A-Za-z0-9_]*`), expect **exactly** `exit 0`. It is one call of the frozen test file.
- **test-row attempt** — a `decider: bl` row whose cmd's first word is `python3` and which contains
  `.backlog/items/`. If it is not exactly the test-row form (two spaces, `./`, an underscore name, an extra
  argument) it is refused under R37 and is **never** treated as a shell row.
- **shell row** — any other `decider: bl` row. Its expect is `exit <code>`, the integer 0..255 form R8 defines, and
  its cmd is **one simple command**: refused if it holds, outside single or double quotes, any of `|`, `;`, `&`, `<`,
  `>`, `$(`, a backtick, or a newline. The scan reads quotes and backslashes exactly as R34's scanner does.

A `decider: judge` row is unchanged in either format: R1, R3 and R34 still apply, and its cmd may be a shell script.

**The test file contract.** The file is a python3 module:

- one function per assertion, and every function a row names is a top-level `def`;
- two rows never name the same function;
- every top-level `def` or `class` whose name does not start with `_` is a function named by a row — a helper's
  name starts with `_`, and a public class is refused;
- other top-level statements (imports, assignments, helper decorators, the main block) are allowed;
- coverage comes from a decorator `@_covers("<path>", ...)`: one of its string-literal arguments must equal the
  file's path as a whole string (R10). A blocking test row cannot cover only `.md` paths; use a docs judge row (R37).
  `bl` reads the decorator from the parsed file and never runs it for coverage.

`bl` parses the file with python3's `ast`, without running it, at every freeze that keeps a test file: `bl checks`,
`bl checks --amend` and `bl rebaseline`, with or without `--tests`, parse the file that will be frozen after the
command (the supplied one, or the frozen one carried over). It refuses (exit 2, `bl: refused`, `(R37)`, nothing
written) when the file does not parse, a row names no top-level def, two rows name one def, or a public top-level
name is not a row's function.

**The run contract.** `python3 <tests path> <name>` prints the line `bl-test: <name>` to stdout as its first action,
before looking up the name, calls the named function, and exits 0 when the assertion holds, 1 when it is false, 3 when
the test itself raised anything else (with a traceback on stderr), and 4 for an unknown name. bl treats 0 and 1 as
results and any other exit, a timeout, or a missing `bl-test` line as a broken test. The template, which the verifier
copies:

```python
import os
import subprocess
import sys
import tempfile

def _covers(*paths):
    def mark(fn):
        return fn
    return mark

def _bl(*args, cwd=None):
    """The binary the gate names in BL, in the repository cwd when one is given."""
    return subprocess.run([os.environ["BL"], *args], cwd=cwd, capture_output=True, text=True)

def _repo():
    """A scratch repository with an initialised ledger, outside this one."""
    root = tempfile.mkdtemp(prefix="bl-test-repo-")
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    _bl("init", cwd=root)
    return root

@_covers("SKILL.md")
def the_thing_holds():
    assert True

if __name__ == "__main__":
    name = sys.argv[1] if len(sys.argv) > 1 else ""
    print("bl-test: " + name)
    fn = globals().get(name)
    if name.startswith("_") or not callable(fn):
        sys.exit(4)
    try:
        fn()
    except AssertionError:
        sys.exit(1)
    except Exception:
        import traceback
        traceback.print_exc()
        sys.exit(3)
```

The **main-block line** is the first line of the first module-level `if` statement (as Python's `ast` reports its
`lineno`) whose source line, after removing trailing whitespace and a trailing `#` comment, is exactly
`if __name__ == "__main__":`; a line inside a string literal never counts. The template's main block starts at that
line and is the last top-level statement of the file. bl does not inject or rewrite any part of the file.

**The freeze probe.** Every format-2 freeze that holds a test row first writes the supplied test file, if one is
given, to its path, then hashes `.backlog/`, then runs the test rows of the resulting list once each on the current
tree, in list order, exactly as `bl run` runs a row (`/bin/sh -c` from the repository root, R21's environment, the row
timeout). It stops at the first row that exits with a code other than 0 or 1, times out, or lacks the line
`bl-test: <name>` on stdout. It then checks the verified tree and `.backlog/` hash against their values from before
the rows ran, in every case, and refuses (exit 2, `bl: refused`, `(R37)`) naming the first failing condition: the row
and its exit code, `timeout`, or `no bl-test line`; then the tree; then `.backlog/`. A row that exits 0 or 1 is
accepted — an exit 1 means the work is not done yet, and `bl run` records it like any other row and does not check the
`bl-test` line. On a refusal the test file's path is restored to its bytes before the command (removed if it did not
exist) and nothing else is written; on success the list and the event are written. The probe writes no run file and no
event.

**Order.** Every existing rule keeps its place (P1, P11). R37's block runs after all of them, in this order: the row
checks (the test-row form, the shell-row form, and B9's presence rules), then the test file's parse and amendment
checks, then the probe, immediately before the write. The test file's own re-hash is the exception: it is a state
guard, and it runs at the top of each verb, before the submitted list is read.

**Amending the test file.** R4's fourth amendment action: `bl checks <id> --amend --file <tsv> --tests <py>` accepts
a new test file only when one is already frozen and the new file is the frozen file with new text inserted at exactly
one point, immediately before the frozen file's main-block line — the frozen bytes before that line are a prefix of
the new file, and the frozen bytes from that line to the end are its suffix. Otherwise the amendment is refused under
R37. With no frozen test file, `--amend --tests` is refused and only `bl rebaseline` can introduce one. The amendment
must still add or change a row as R4 requires, and the amended event keeps the list's format.

**Format 1, and which files a list needs.** A format-2 list with a test row needs a test file; a freeze with a test
row and no frozen file is refused. A format-2 list of judge and shell rows only needs none, and `--tests` given with
such a list is refused. Every format-2 freeze in which a test file stays frozen — supplied, or carried over by an
amendment or a re-baseline — must hold at least one test row, so a carried-over file is always parsed and probed. A
format-1 list is untouched: it runs, rounds, closes and checks as before; `--amend` without `--tests` keeps format 1,
and `--amend --tests` on it is refused, because only `bl rebaseline` can move a list to format 2.

**Worked examples.**

- `bl checks A1 --file c.tsv --tests t.py`, where c.tsv holds one test row: exits 0; `.backlog/items/A1/tests.py` is
  byte-identical to t.py; the last ledger line holds `"format":2`, `"tests":".backlog/items/A1/tests.py"` and a
  `"tests_sha"` equal to the sha256 of t.py.
- row `R1` with cmd `python3 .backlog/items/A1/tests.py restart_prints_attempt` and expect `exit 0` is accepted; the
  same row with `stdout matches ok` is refused at `bl checks` with exit 2, a stderr line starting `bl: refused`, the
  row id `R1` and the tag `(R37)`. `python3  .backlog/items/A1/tests.py a` (two spaces) and
  `python3 ./.backlog/items/A1/tests.py a` are both refused `(R37)`.
- t.py defines `_repo()`, `a()` and `b()`: rows naming `a` and `b` are accepted; rows naming only `a` are refused
  naming `b`; rows naming `a`, `b` and `c` are refused naming `c`; adding `class Helper: pass` makes the first list
  refused naming `Helper`.
- a test whose function raises `NameError` exits 3: the list is refused naming that row and `exit 3`, and
  `.backlog/items/A1/tests.py` does not exist afterwards; the same function fixed to `assert False` exits 1 and the
  list is accepted; a file with no main block exits 0 and is refused naming that row and `no bl-test line`; a file
  whose first line is `import does_not_exist` exits 1 with no such line and is refused the same way; a test function
  that writes `.backlog/items/A1/junk.txt` is refused on the `.backlog/` check.
- `go -C scripts test ./...` with `exit 0` is accepted; `cd scripts && go test ./...`, `go -C scripts test ./... |
  tail -1`, and `echo "it's" | wc -l` are refused `(R37)`; `echo \$(date)` is accepted; `grep -c x f` with `stdout
  matches ^3$` is refused for its expect; `ls` with `exit 300` is refused.
- a docs hunk is covered by a blocking judge row whose anchor lies in that hunk; a stale anchor is refused under R10;
  a blocking test row with only `@_covers("SKILL.md")` is refused under R37, while an advisory row or one also
  covering code is accepted; a diff adding an empty `e.txt`
  is covered by `@_covers("e.txt")`.
- after the freeze, appending `def c(): assert True` to tests.py on disk makes `bl run A1` exit 2 naming tests.py;
  `bl checks A1 --amend --file c2.tsv --tests t2.py`, where t2.py inserts `def c(): assert True` before its main block
  and c2.tsv adds a row naming `c`, exits 0; a module docstring that contains the text
  `if __name__ == "__main__":` does not move the split; a t2.py that changes one byte of t.py is refused `(R37)`.
- `bl rebaseline A1 --file t.tsv` with only judge rows, on a format-2 list with a frozen tests.py, is refused
  `(R37)`; the repository's own ledger, whose items are format 1, checks exactly as before.

**P1 is a rule about the whole list, not about one row.** The precedence in §4 applies across the list: if one row
breaks R1 and another breaks R8, the refusal names R1. Evaluating rule by rule and row by row lets the rule that
happens to sit higher in the file win, which is not the same thing as the earliest broken rule. This is still an
open implementation item (I7) — the design states it, the code does not yet do it.

## 4. Precedence

Where two rules can both match, the winner is named here, not left to the implementation. P1, P2, P3, P4 and P5 are per
command: the checks below are the order in which a command evaluates its rules, and a rule that refuses stops the
command there. P6, P7, P8, P9 and P10 are general rules that apply to every command, and P11 is the sixth per-command
list, added by item I9 when the implementation found the gap.

**P1 — `bl checks` order.** R5 (the claims freeze the list's content) → R4 (the list is written once) →
*the list is read* → R1 (schema) → R34 (the command is a shell script, not a pattern) → R4 (the amend scope, when
`--amend` is given: the amendment may only add a row, and an amendment is refused once the item has a PASS round) →
R5 (an amendment that CHANGES a row is refused once the claims exist) → R2 (brief clause) → R3 (judge
justification) → R8 (expectation form, format 1 only) → **R37** (the test file and the format-2 rows: the row rules,
then the file's parse and amendment checks, then the freeze probe immediately before the write). The state guards that do not depend on the rows come first, because a
command that is not allowed to write a list has no use for the list, and refusing before the input is read means a
doomed command never waits on stdin. The guards that do depend on the rows come after the rows are read: an
amendment is judged on what it does to them, so an amendment that changes a row is refused under R5 whatever else
is wrong with it. Within one rule, the first broken row in file order is named, and only that one. Item I9 changed
this order: before it, R1 came before R5 and R4, so item I7's brief said "R1 across all rows, then R5, R4" - that
wording is superseded. Item I23 changed it again: R5 moved to where the amendment's scope is judged, because the
claims now freeze the list's content and not its corrections.

**P11 — `bl rebaseline` order.** R20 (the frozen artefacts are what they were, including the test file) → R1 (schema) → R34 (the command is a shell script, not a pattern) → R2 (brief clause) → R3
(judge justification) → R8 (expectation form, format 1 only) → R5 (the claims freeze the list) → R4 (the list is written once, and
the amend scope) → **R37** (the test file and the format-2 rows, with the probe immediately before the write). The list's rules come first here because the list IS the proposal: a re-baseline that names a
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
  on R11. P1 evaluates the amendment's scope (R4) before the claims' guard (R5), so the refusal names R4.
- An amendment that fills the empty `brief_ref` with `B2` **is accepted** when the item has no claims event, and is
  **refused on R5** when it has one: filling a `brief_ref` CHANGES a row, and the claims freeze the list's content.
- An amendment that reclassifies the `bl` row `S1` to `decider: judge` is **refused on R1** — P1 runs R1 before R4, and
  a judge row may not carry `expect`, which `S1` has. P7's "stricter wins" reaches the same outcome from the other
  direction: the change is looser, and looser is refused.
- An amendment that reclassifies `J3` to `decider: bl`, setting `expect: exit 0` and clearing `fail_if` and
  `why_not_mech`, **is accepted**.
- An amendment that ADDS a row `S2` to the list is **accepted**, and it is accepted even once the item has a claims
  event (R4, R5): the claims freeze the list's content, and an added row can only tighten the round, because the
  derived verdict is the AND over the blocking rows (R12). An amendment that only adds a row is not refused under
  R5; an amendment that changes a row is.
- An amendment is **refused on R4** once the item has a PASS round: the verdict already rests on the list, and a later
  list cannot reopen it.

**Must not hold:** the stored list is unchanged after a refused amendment; a refused amendment is not recorded.

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
| checks `bl` validates before storing a list | 7 | R1, R2, R3, R4, R5, R8, R37 |
| refusals `bl checks` can raise | 7 | R1, R2, R3, R4, R5, R8, R37 |
| refusals `bl round` can raise | 12 | R6, R9, R10, R12, R14, R16, R20, R23, R25, R28, R35, R37 — R13 derives rather than refuses, R27 moved to the run, and R19's cap refusal and R24's removed-clause refusal are counted separately because they are caps and premise faults rather than checks |
| refusals `bl run` can raise | 6 | R1, R8 (format 1), R20, R26, R27, R35 |
| refusals `bl close` can raise | 4 | R15, R18, R20, R25 |
| frozen artefacts re-verified before a command | 4 | the brief, the check list and the claims, and the frozen test file when there is one, each against its latest recorded hash — R20 |
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
