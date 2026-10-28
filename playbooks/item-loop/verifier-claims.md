The dispatcher includes `bl packet <id> --role verifier` output — the current brief, the
cited rules and the claims below — rather than naming `references/design-check-list-schema.md`.

The producer claims:
  - <claim 1, with the number it reported>
  - <claim 2>

Falsify each claim with your own checks, plus:
  - <the property, on a case you construct yourself>
  - a row that covers a change puts the covered file's path in the command,
    next to the thing it exercises
  - one assertion per row: a row's command asserts exactly one thing, and its
    expect must be reachable only through that assertion. A row that an earlier
    statement in its own command can satisfy binds nothing, so do not ship one
  - test binding: when the item touches a test, mutate the code in a copy and
    confirm that test fails. Otherwise mark this row N/A with your reason
  - the repo's documented test command; if there is none, or it is slow, name the
    subset you ran and why
  - hygiene: nothing changed outside <the item's file list>, no untracked file
    left behind, and no difference against <before-state file> — add the file list
    of any parallel item, whose edits are expected to show

Work on copies under the evidence dir; leave the real data read-only. Do NOT edit
tracked files.

OUTPUT: | check | command | expected | observed |, then a Verdict line,
     Verdict: PASS
     Verdict: FAIL - <the rows that failed>
then the strongest counterexample you found or "none found, tried X, Y, Z".
Write this to <evidence dir>/<id>-verify-r<N>.md verbatim.
