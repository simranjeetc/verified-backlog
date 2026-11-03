# Bootstrap: when there is no ledger yet

```sh
"$BL" init              # .backlog/ + empty views; refuses over a foreign TODO.md
```

Write `BRIEF.md` first with the human: intent, constraints, what "done" means, and whether this
backlog may push. Then research the repo, write the breakdown back into `BRIEF.md` as prose, and
file the items it describes — one call each:

```sh
"$BL" file --id A1 --text "wellfound remote sweep alongside the location sweep" --source human
"$BL" file --id A2 --text "pagination flags wiring test" --after A1 --source human
```

`--after` records a dependency: the item exists now, `bl next` skips it until A1 closes.

If a prose backlog already exists, do not rewrite it by hand:

```sh
"$BL" lint TODO.md PROGRESS.md      # read-only: what parses, what does not
"$BL" import TODO.md PROGRESS.md    # one pass, fresh ledger only
```

An imported close is a claim, not a proof: the ledger records that the prose said PASS and which
commit it named, and a note says so. Treat imported items as untrusted until a real round runs.

The import runs once, refuses a non-empty ledger, and refuses to overwrite a `TODO.md` it did not render. A closed item
becomes a PASS round and a close carrying `legacy:unknown`.

Completion: `bl status` counts the items and `bl next` names one.
