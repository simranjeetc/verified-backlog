# Diagrams — verified-backlog

Plain ASCII pictures of the loop. Code is the source of truth; the file and line named under each
diagram is where to check it when the two disagree.

## One item, step by step

```
 bl file ─► bl open ─► bl brief ─┬─► produce (luna, worktree) ─┐   in parallel;
                                 └─► checks (haiku) ───────────┤   neither waits
                                                               ▼
               bl checks (frozen) ─► bl claims ─► bl run (frozen rows)
                                                       │
   ┌───────────────────────────────────────────────────┘
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

The outer loop is a separate program, a runner with no conversation context of its own. It is not
an agent session. It only calls `bl` and starts or awaits harness sessions; each session works one
item and exits.

```
 ┌─ runner program (no context; only calls bl + spawns sessions) ─┐
 │                                                                │
 │  bl next ──► no item? ──► stop                                 │
 │     │                                                          │
 │     ▼ spawn                                                    │
 │  ┌─ harness session (fresh, one item, item-loop.md) ─┐         │
 │  │  open … round … close / defer / drop ── exits     │         │
 │  └──────────────────────────────────────────────────┘         │
 │     │ on exit: runner asks bl for the item's state             │
 │     ├─ closed / parked / declined ──► back to bl next          │
 │     └─ in flight, no round recorded ──► refuse, stop w/ reason │
 └────────────────────────────────────────────────────────────────┘
```

Check against: `TODO.md` entry I57.
