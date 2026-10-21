# PROGRESS — verified-backlog

<!-- rendered from .backlog/ledger.jsonl by bl; do not edit — ask instead -->

## I1 — bl checks + bl claims: the check-list row schema and the ordering rule (design R1-R8, R20, R24). Scope: scripts/cmd/bl/main.go, scripts/cmd/bl/main_test.go

Status: open

- r1 FAIL cause=code — .backlog/evidence/I1-r1.txt (6bf1e1a502fb)
- code failures: 1 / cap 3
- note: check list corrected twice before freezing, both mechanical: (1) the module root is scripts/ so the commands use go -C scripts; (2) rows carried one extra empty column; (3) brief_ref mapped to B1, the only clause I1's brief has - the verifier assumed B1..B8 because it wrote the list without seeing the brief, which is a real gap: brief clause granularity comes from the item text, and a blocking row can only cite a clause that exists
- note: r1 FAIL. Two producer gaps: bl brief (R24) is not implemented at all, and R20 re-hash-at-use is not implemented (bl check still exits 0 after the frozen artefacts are tampered with). Two defects in the check list itself, not the producer: (a) nine rows never file the item before opening it, so they die at 'unknown item A1' before any assertion; (b) the rows call 'bl brief', which the producer was never asked for by name. And a tooling gap this round exposes: there is no re-baseline path, so a defective frozen check list cannot be replaced - R4 lets only a narrow amend - which means a broken list burns a code round unfairly. Next round must: implement bl brief with the old hash kept, and make bl check re-hash brief/checks/claims against the latest recorded hash.

