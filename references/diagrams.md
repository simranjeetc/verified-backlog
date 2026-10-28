# Diagrams — verified-backlog

Plain ASCII pictures of the loop. Code is the source of truth; the file and line named under each
diagram is where to check it when the two disagree.

## One item, step by step

```
 bl file ─► bl open ─► bl brief ─► checks (haiku) ─► bl checks (frozen)
                                                          │
   ┌──────────────────────────────────────────────────────┘
   ▼
 produce (luna, worktree) ─► bl claims ─► bl run (frozen rows)
                                              │
   ┌──────────────────────────────────────────┘
   ▼
 bl packet --role verifier ─► verify (deepseek) ─► bl round
                                                     │
                     PASS ─► commit ─► bl close ◄────┤
                     FAIL ─► fix round (≤3 code rounds, R19) ─► produce
```

Check against: `SKILL.md` (command rows), `scripts/cmd/bl/round.go:678` (round cap), `scripts/cmd/bl/ledger.go:26`.

## Item status

```
          bl restart (next attempt; at attempt 3 it parks instead, R36)
              ┌───┐
              ▼   │
         ┌──────────┐  bl close   ┌──────────┐
         │   open   │────────────►│  closed  │
         └──────────┘             └──────────┘
          │   ▲    │
  bl drop │   │    │ bl defer
          ▼   │    ▼
   ┌──────────┐│┌──────────┐
   │ declined │││  parked  │
   └──────────┘│└──────────┘
         └─────┴─────┘  bl reopen (declined or parked only)
```

Check against: `scripts/cmd/bl/ledger.go:503-511`, `scripts/cmd/bl/lifecycle.go:81`, `scripts/cmd/bl/restart.go:15,97`.

## Outer loop (I57 — proposed, not built)

```
 bl next ─► no item? ─► stop
    │
    ▼
 fresh harness session works one item (playbooks/item-loop.md)
    │
    ▼
 session exits ─► item closed / parked / declined? ─► bl next
                └► still in flight, no round recorded ─► refuse, stop with reason
```

Check against: `TODO.md` entry I57.
