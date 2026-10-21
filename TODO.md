# TODO — verified-backlog

<!-- rendered from .backlog/ledger.jsonl by bl; do not edit — ask instead -->

## Queue (1)

- [ ] I1 — bl checks + bl claims: the check-list row schema and the ordering rule (design R1-R8, R20, R24). Scope: scripts/cmd/bl/main.go, scripts/cmd/bl/main_test.go

## Blocked (3)

- [ ] I2 — bl run: execute every frozen row under the fixed environment, write the run file with full output and hashes, tree built from the filesystem (R7, R21, R26, R27)  _(waiting on I1)_
- [ ] I3 — bl round: the derived verdict AND of blocking rows, the refusals, the verdicts file, the tree pin, the evidence file (R6, R9, R10, R12, R13, R14, R16, R18, R20, R23, R25, R28)  _(waiting on I2)_
- [ ] I4 — the anchor: ledger hash chain with seq, bl anchor printing the head, bl check refusing a ledger behind the anchored seq (R20, R29)  _(waiting on I1)_

## Parked (0)

_none_

## Declined (0)

_none_

## Closed (0)

_none_

