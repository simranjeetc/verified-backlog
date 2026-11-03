# Recovery: restarts, later attempts, refused rounds

Reach for this when a fix round cannot help, or `bl round` refuses.

## When to restart instead of fixing

A FAIL round is a defect in the work. A restart is a defect in the attempt's own premise or in
its record, and only the four classes are reasons for one:

| class | when |
|---|---|
| `bad-check` | a frozen row cannot pass on correct code — it asserts the wrong thing, or its command cannot reach its assertion |
| `extra-file` | the producer wrote a file the item's scope did not name, so the record is of the wrong change |
| `damaged-evidence` | a run file, a round's evidence or another artefact no longer matches its hash, so the record no longer describes what ran |
| `wrong-premise` | the item's own premise is false, so no fix round can converge |

```sh
"$BL" restart A1 --reason bad-check --why "row R3 expects ^ok$ and cannot match the newline echo prints" \
                --change "stdout patterns allow a trailing newline"
```

The restart writes the failure record of the attempt it ends and leaves the item open and out of
flight. Its scope and ignored-scope lists carry into the next `bl open` unless either scope flag is
given; explicit scope flags replace both. `bl open A1` starts attempt 2 with its own brief (which carries that record), its own list
and its own claims, under `.backlog/items/A1/a2/`. The rounds of the attempt it ended are void: the
code-round cap starts again, `bl show` marks them ` void`, and its runs can no longer be cited. Two
restarts, three attempts; a third is refused and parks the item for the human.

A row that cannot pass is a `bad-check`. A fix round is for code that can pass. Do not restart to
escape a row you could fix, and do not run a fourth fix round after two rounds failed on the same
premise: that premise is wrong, and a restart is the recorded way to say so.

## The verifier of a later attempt

The fresh verifier of attempt *n* receives the attempt's brief, which carries the failure records
of every earlier attempt, and never an earlier attempt's claims: those describe a tree that no
longer exists. Pass the attempt's own `checks.tsv`, `claims.md` and run file, and name the failure
records as the record of why the attempt exists.

## A refused or void round

If an R39 refusal says the verifier window is void, correct the invalidating state first,
then issue a fresh verifier packet
and dispatch a new verifier. Exception: filing a follow-up for a filed disposition is allowed inside the window and does not void it.
An amendment is blocked while a usable verifier window is open. An already void window does not
block amendment, but the amended checks need a new run, packet and independent verifier.

An R10 refusal before any PASS round can be recovered without a FAIL round or restart. If the
verifier window is open, run `bl run <ID>` first to void it, then `bl checks <ID> --amend` adding
a blocking row that covers the named path (when the format-2 list has a frozen test file, use
`--tests` with a test function carrying `@_covers("<path>")` inserted before the main block;
otherwise add a shell row whose command names the path. For a `.md` path, add a judge row.)
Then run `bl run <ID>`, issue a fresh `bl packet <ID> --role verifier`, dispatch a new verifier,
and record the round. With no open window, amend first, then run, issue a fresh packet and dispatch a new verifier before recording the round.

Examples: for `main.go`, a list with frozen Python tests adds a function decorated with
`@_covers("main.go")` before the frozen main block and passes the extended file via `--tests`.
A list without frozen tests instead adds a blocking simple-command row that exercises
`main.go` and names that path; omit `--tests`. For a changed `README.md` hunk, add a
blocking judge row with an anchor in the hunk; for a path-only change its command must
name `README.md`. The new verifier must evaluate the amended list, not reuse the old verdict.
