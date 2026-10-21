# TODO — verified-backlog

<!-- rendered from .backlog/ledger.jsonl by bl; do not edit — ask instead -->

## Queue (11)

- [ ] I4 — the anchor: ledger hash chain with seq, bl anchor printing the head, bl check refusing a ledger behind the anchored seq (R20, R29)
- [ ] I12 — compaction on every 250k crossing: whenever a session's token consumption crosses a multiple of 250k, the compaction must be triggered once the logical step that crossed it is complete - every crossing, not only the first - so a long session is compacted repeatedly instead of never  _(human)_
- [ ] I14 — the design's word 'absent' in R23 is ambiguous: a harness round whose --rows path cannot be read (missing, or a directory) is accepted and recorded with no rows, on the reading that a file that cannot be read holds none. Decide whether 'absent' means the flag was not given, or the file cannot be read, and make the binary and the design say the same thing  _(verifier:I11/r2)_
- [ ] I16 — R25 says bl says so when .backlog/ is tracked by git, and the implementation does not: bl run excludes .backlog/ from the tree unconditionally and never says anything about it being tracked  _(verifier:I2/r1)_
- [ ] I17 — bl run does not require the item to be in flight (cmdRun omits requireInFlight), so a run can happen outside the item's turn and the one-item-at-a-time discipline is not enforced on that path; decide whether bl run must hold the lock like bl round does, and enforce it  _(verifier:I2/r1)_
- [ ] I18 — R7 says the binary computes a decider: bl row's row-verdict, and the implementation does not: the round takes the judge's word for a bl row, so the derivation in R12 rests on the judge's word and a judge who writes PASS for a failing command gets a PASS round; the binary must compute a bl row's row-verdict from the run's observed values against that row's expect  _(producer:I3/r1)_
- [ ] I19 — the verified tree includes the loop's own rendered views (TODO.md and PROGRESS.md), which change with every ledger event, so R6's tree pin refuses any round after the ledger moved - and the ledger moves in the item's own flow, because R14 files the advisory items between the run and the round. Decide whether the rendered views are artefacts and are excluded from the tree, or the pin compares something narrower than the whole tree, and make the design and the code say the same thing  _(producer:I3/r1)_
- [ ] I20 — the check-list format did not say that a row covering a change must name the file it covers: R10's coverage is a literal comparison, so a list written to drive a binary left every hunk uncovered and no round could be recorded at all. The design now says the path belongs in the command - verify that the design says it, and make the loop's own check lists carry the path  _(supervisor:I3/r1)_
- [ ] I21 — a row's command can satisfy its expectation from a statement other than its own assertion - the row added to item I3 passed while the defect it was written for was still in the code. The design now says one assertion per row - verify that it says it, and that the loop's own rows do it  _(supervisor:I3/r1)_
- [ ] I22 — the nested-git test's directory case creates empty directories, so it passes even with the fix reverted and binds nothing; it must build a nested repository with content - a git init inside it and a commit - so the case fails when the tree computation stops skipping a nested .git directory  _(verifier:I3/r2)_
- [ ] I23 — a defective row cannot be corrected once the claims are recorded: item I3's row D1 is satisfied by an earlier statement in its own command and does not bind the refusal it was written for, and R5 refuses the re-baseline that would fix it. The design must say how a defective row is corrected after the claims - a second judge, a re-recorded claim, or a rule that the freeze binds the list's content and not its corrections  _(verifier:I3/r2)_

## Blocked (0)

_none_

## Parked (0)

_none_

## Declined (0)

_none_

## Closed (12)

- [x] I1 — bl checks + bl claims: the check-list row schema and the ordering rule (design R1-R8, R20, R24). Scope: scripts/cmd/bl/main.go, scripts/cmd/bl/main_test.go  _(commit a571e71)_
- [x] I2 — bl run: execute every frozen row under the fixed environment, write the run file with full output and hashes, tree built from the filesystem (R7, R21, R26, R27)  _(commit 9e8f95a)_
- [x] I3 — bl round: the derived verdict AND of blocking rows, the refusals, the verdicts file, the tree pin, the evidence file (R6, R9, R10, R12, R13, R14, R16, R18, R20, R23, R25, R28)  _(commit 42c7563)_
- [x] I5 — the total-round budget: R19 - six rounds of any cause for an item, refused beyond that, counted across re-baselines so a re-baseline cannot reset the only cap  _(commit 102cf3f)_
- [x] I6 — R24 removed-clause refusal in bl round: a round whose row cites a brief clause that a later brief amendment removed must be refused, the way bl checks refuses it under R2  _(commit 39622a7)_
- [x] I7 — P1 precedence ordering: the rules are evaluated in a fixed global order across the whole list (R1 across all rows, then R5, R4, R2, R3, R8), so the refusal names the earliest broken rule and not the first broken row  _(commit 5475ae5)_
- [x] I8 — the python gate's stale expectations: scripts/bl.test.py hard-codes 8 event kinds and the causes [code, harness], but the schema now has 12 kinds and three causes, so the gate has been failing since I1  _(commit 0d31559)_
- [x] I9 — the precedence for the commands the design does not cover: bl rebaseline needs its own order (P1 names bl checks only, and rebaseline's validateChecks runs R1-R2-R3-R8 with no R5/R4), and bl checks must do its state checks (R5, R4) before it reads the list, so a frozen item is refused without waiting on stdin  _(commit 828d211)_
- [x] I10 — the gate's verb set is a hard-coded list of 18 and omits the four verbs the binary grew in I1 (brief, checks, claims, rebaseline), so SKILL.md cannot document them and the check named the_documented_verbs_are_the_real_verbs is weaker than its name; the set must come from the binary  _(commit 7a6978b; verifier:I8/r1)_
- [x] I11 — the binary accepts --cause harness with a rows file, but R23 says a harness round is accepted only when the verdicts file is absent or has no rows, and is recorded with no rows  _(commit 0cf8e86; verifier:I8/r1)_
- [x] I13 — the check-list format never says which dialect each field is written in, so the verifier keeps over-escaping: it writes a backslash-pipe where the shell wants a pipe and where Go's RE2 wants an alternation, and the row cannot run - it cost two harness rounds in I8 and one row in I9. The format must state the two dialects plainly (the cmd is a shell script run by /bin/sh -c, so its pipes are pipes; the expect pattern is Go's RE2, so an alternation is a bare pipe and a backslash-pipe is a literal pipe), and the binary must refuse a frozen list whose cmd contains an escaped pipe, so the mistake cannot reach a round. This must be closed before I2 begins  _(commit be4d18f; human)_
- [x] I15 — R34 fires on the two characters backslash and pipe, so a command holding an escaped backslash followed by a real pipe is refused even though its pipe is not escaped; the shell reads the doubled backslash as one backslash and the pipe as a pipe. The refusal must count the backslashes and fire only when the pipe is actually escaped  _(commit 69aa5dd; verifier:I13/r1)_

