# TODO — verified-backlog

<!-- rendered from .backlog/ledger.jsonl by bl; do not edit — ask instead -->

## Queue (6)

- [ ] I2 — bl run: execute every frozen row under the fixed environment, write the run file with full output and hashes, tree built from the filesystem (R7, R21, R26, R27)
- [ ] I4 — the anchor: ledger hash chain with seq, bl anchor printing the head, bl check refusing a ledger behind the anchored seq (R20, R29)
- [ ] I10 — the gate's verb set is a hard-coded list of 18 and omits the four verbs the binary grew in I1 (brief, checks, claims, rebaseline), so SKILL.md cannot document them and the check named the_documented_verbs_are_the_real_verbs is weaker than its name; the set must come from the binary  _(verifier:I8/r1)_
- [ ] I11 — the binary accepts --cause harness with a rows file, but R23 says a harness round is accepted only when the verdicts file is absent or has no rows, and is recorded with no rows  _(verifier:I8/r1)_
- [ ] I12 — compaction on every 250k crossing: whenever a session's token consumption crosses a multiple of 250k, the compaction must be triggered once the logical step that crossed it is complete - every crossing, not only the first - so a long session is compacted repeatedly instead of never  _(human)_
- [ ] I13 — the check-list format never says which dialect each field is written in, so the verifier keeps over-escaping: it writes a backslash-pipe where the shell wants a pipe and where Go's RE2 wants an alternation, and the row cannot run - it cost two harness rounds in I8 and one row in I9. The format must state the two dialects plainly (the cmd is a shell script run by /bin/sh -c, so its pipes are pipes; the expect pattern is Go's RE2, so an alternation is a bare pipe and a backslash-pipe is a literal pipe), and the binary must refuse a frozen list whose cmd contains an escaped pipe, so the mistake cannot reach a round. This must be closed before I2 begins  _(human)_

## Blocked (1)

- [ ] I3 — bl round: the derived verdict AND of blocking rows, the refusals, the verdicts file, the tree pin, the evidence file (R6, R9, R10, R12, R13, R14, R16, R18, R20, R23, R25, R28)  _(waiting on I2)_

## Parked (0)

_none_

## Declined (0)

_none_

## Closed (6)

- [x] I1 — bl checks + bl claims: the check-list row schema and the ordering rule (design R1-R8, R20, R24). Scope: scripts/cmd/bl/main.go, scripts/cmd/bl/main_test.go  _(commit a571e71)_
- [x] I5 — the total-round budget: R19 - six rounds of any cause for an item, refused beyond that, counted across re-baselines so a re-baseline cannot reset the only cap  _(commit 102cf3f)_
- [x] I6 — R24 removed-clause refusal in bl round: a round whose row cites a brief clause that a later brief amendment removed must be refused, the way bl checks refuses it under R2  _(commit 39622a7)_
- [x] I7 — P1 precedence ordering: the rules are evaluated in a fixed global order across the whole list (R1 across all rows, then R5, R4, R2, R3, R8), so the refusal names the earliest broken rule and not the first broken row  _(commit 5475ae5)_
- [x] I8 — the python gate's stale expectations: scripts/bl.test.py hard-codes 8 event kinds and the causes [code, harness], but the schema now has 12 kinds and three causes, so the gate has been failing since I1  _(commit 0d31559)_
- [x] I9 — the precedence for the commands the design does not cover: bl rebaseline needs its own order (P1 names bl checks only, and rebaseline's validateChecks runs R1-R2-R3-R8 with no R5/R4), and bl checks must do its state checks (R5, R4) before it reads the list, so a frozen item is refused without waiting on stdin  _(commit 828d211)_

