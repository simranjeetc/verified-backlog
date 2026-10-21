# PROGRESS — verified-backlog

<!-- rendered from .backlog/ledger.jsonl by bl; do not edit — ask instead -->

## I1 — bl checks + bl claims: the check-list row schema and the ordering rule (design R1-R8, R20, R24). Scope: scripts/cmd/bl/main.go, scripts/cmd/bl/main_test.go

Verdict: PASS — closed at a571e713bb510018cec6f5a5a5c782d0d5bd2d56

- r1 FAIL cause=code — .backlog/evidence/I1-r1.txt (6bf1e1a502fb)
- r2 FAIL cause=code — .backlog/evidence/I1-r2.txt (0019667c1197)
- r3 FAIL cause=code — .backlog/evidence/I1-r3.txt (5822965021a5)
- r4 FAIL cause=code — .backlog/evidence/I1-r4.txt (3b826069cce4)
- r5 PASS — .backlog/evidence/I1-r5.txt (1f853f3cde35)
- code failures: 4 / cap 3
- note: check list corrected twice before freezing, both mechanical: (1) the module root is scripts/ so the commands use go -C scripts; (2) rows carried one extra empty column; (3) brief_ref mapped to B1, the only clause I1's brief has - the verifier assumed B1..B8 because it wrote the list without seeing the brief, which is a real gap: brief clause granularity comes from the item text, and a blocking row can only cite a clause that exists
- note: r1 FAIL. Two producer gaps: bl brief (R24) is not implemented at all, and R20 re-hash-at-use is not implemented (bl check still exits 0 after the frozen artefacts are tampered with). Two defects in the check list itself, not the producer: (a) nine rows never file the item before opening it, so they die at 'unknown item A1' before any assertion; (b) the rows call 'bl brief', which the producer was never asked for by name. And a tooling gap this round exposes: there is no re-baseline path, so a defective frozen check list cannot be replaced - R4 lets only a narrow amend - which means a broken list burns a code round unfairly. Next round must: implement bl brief with the old hash kept, and make bl check re-hash brief/checks/claims against the latest recorded hash.
- note: r2 FAIL, code. Every blocking row passed and the producer's hand-run claims reproduced exactly, but the claim 'every verb that uses a frozen artefact re-hashes it' is false: bl brief overwrites a tampered stored brief without an R20 refusal, and the appended brief event carries the pre-tamper hash, so the edit leaves no trace. The frozen list had no row for R20 at use in bl brief - the premise was incomplete, which is why the hole survived a row-complete round. Fix round: (a) bl brief re-hashes before overwriting, (b) bl schema lists brief and rebaseline, test count 10 to 12, (c) the list gains a row for (a). Also filed: I5 = R19 total-round budget (6 rounds of any cause) not implemented, only the 3-code-round cap which the re-baseline resets; I6 = R24 removed-clause refusal in bl round. Supervisor note: rebaseline resets the code cap, so the total budget is the only guard against an endless rebaseline - holding I1 to two more rounds, then it goes to the human. Also: the built binary scripts/bl was gone after the verifier's run; it is a gitignored artefact, rebuilt here - the verifier's throwaway cleanup may have removed it, so the verifier should clean up only inside its own directory.
- note: r3 FAIL, code. Three blocking findings: (1) a judge row with an empty fail_if is refused under R3 but the requirement belongs to R1 (the schema rule); R3 owns the placeholder and the too-short why_not_mech. (2) bl schema lists the event kinds but never reports how many, so 12 and 11 look the same to a reader and to a grep. (3) bl open overwrites a tampered stored brief with no R20 refusal, so the tamper leaves no trace - the same hole class as bl brief had in r2, which is why the fix round must add a table-driven test enumerating every verb that touches a frozen artefact, so a new verb cannot join that family unnoticed. Advisory: the hygiene row must exclude the loop's own rendered views TODO.md and PROGRESS.md. r4 is the last code round the supervisor authorises for this item.
- note: r4 FAIL, code, on one row: X2. All 15 frozen rows pass, and so do T1, TB, H1, N1 and X1. X2 is the gap the producer itself admitted: after a claims event, bl defer + bl reopen + bl open rewrites the R5-frozen brief, appends a new open event with a new brief_sha, leaves a frozen check row pointing at a clause that no longer exists, and bl check still reports PASS - while bl brief refuses the same rewrite under R5, so the implementation contradicts itself. Also filed: I7 = P1 global ordering (a list whose row 1 breaks R8 and row 2 breaks R1 must be refused under R1, not R8). And the judge's advisory: the sample judge row J3 carries no output anchor, so under R9 it can only ever be recorded FAIL. The 3-code-round cap is now spent (r2, r3, r4), so r5 needs a re-baseline or the human's word.
- note: I1 closed on round 5 (PASS): 15 frozen rows, T1, TB, H1, N1, X1, X2, X3 and the judge row all passed with no repair. Two advisories filed rather than fixed: the R5 guard sits before the R20 re-hash in cmdOpen, so an item with claims reports R5 for a tampered brief (bl check still reports R20); and the frozen list has no row pinning the 12-kind schema count. Both are candidates for the next re-baseline.

## I5 — the total-round budget: R19 - six rounds of any cause for an item, refused beyond that, counted across re-baselines so a re-baseline cannot reset the only cap

Verdict: PASS — closed at 102cf3fd1f2aba2818c45d159118dfaaed8764b2

- r1 PASS — .backlog/evidence/I5-r1.txt (454fab4a9c0f)
- code failures: 0 / cap 3
- note: the round-1 check list was written blind and three of its rows encoded assumptions the design contradicts: a re-baseline is refused once the claims exist (R5, R33), and the regex dialect is Go's RE2 where an escaped pipe is a literal pipe. The re-issued list fixes both, and the supervisor made one mechanical correction (the escaped pipe). Also learned: the budget's counts are per item over its whole life, so a re-baseline buys no fresh rounds, and a premise fault must be recorded as a harness round, not a code round - a code round spends the item's budget.
- note: round 2: the producer relaxed the 'already passed' guard to satisfy an impossible row, and the legacy test scripts/bl.test.py::round_refuses_after_pass caught it. The guard goes back to its original meaning, named R18, checked before the budget; bl status must also count passes so the six-round total is visible. The verifier wrote the regex alternation as an escaped pipe twice, in both re-issues - a habit worth watching, and a supervisor correction each time.

## I6 — R24 removed-clause refusal in bl round: a round whose row cites a brief clause that a later brief amendment removed must be refused, the way bl checks refuses it under R2

Verdict: PASS — closed at 39622a7d4c186a9500fce0dce5bdce6486351a74

- r1 PASS — .backlog/evidence/I6-r1.txt (5655a27bf924)
- code failures: 0 / cap 3

## I7 — P1 precedence ordering: the rules are evaluated in a fixed global order across the whole list (R1 across all rows, then R5, R4, R2, R3, R8), so the refusal names the earliest broken rule and not the first broken row

Verdict: PASS — closed at 5475ae5cf20fc4ee69fb0a902b02259641f31e1d

- r1 PASS — .backlog/evidence/I7-r1.txt (196747c23f9f)
- code failures: 0 / cap 3

## I8 — the python gate's stale expectations: scripts/bl.test.py hard-codes 8 event kinds and the causes [code, harness], but the schema now has 12 kinds and three causes, so the gate has been failing since I1

Verdict: PASS — closed at 0d31559f0867d7e7c50de3975de7ccbd7b0764eb

- r1 FAIL cause=harness — no rows
- r2 FAIL cause=harness — no rows
- r3 PASS — .backlog/evidence/I8-r3.txt (4a2fdfb2eb5c)
- code failures: 0 / cap 3
- note: round 1: the premise was broken, not the code. The producer's run was 9/10 and the one failure was G9, whose mutation target is a word SKILL.md does not contain. Recorded as a harness round with no rows, because a run that never happened cannot carry a finding (R23). The implementation was not changed between rounds; round 2 runs against the re-based list.
- note: round 2: the premise was broken again, the code was not. The verifier's verdict was FAIL with the finding that the frozen cmd column escaped the shell's pipes. The implementation was left alone; the list was re-based a second time. Two harness rounds spent on one item's premise - the check list, not the code, is where this loop's rounds go.

