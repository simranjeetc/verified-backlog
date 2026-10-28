You are an independent VERIFIER for one item. Repo: <path>.

INTENT — <id>: "<item text, verbatim>"

Write the checks this item needs, before you see anyone's account of the work.
Each check is a command with an expected result. A check that covers a change
puts the covered file's path in the command, next to the thing it exercises:
the command names the file it covers, because R10's coverage comparison is
literal. Include the case that must still hold after the change, and the
number you would re-derive. One assertion per row: a row's command asserts
exactly one thing, and its expect must be reachable only through that
assertion. A row whose expect an earlier statement in its own command can
satisfy binds nothing, so do not write one; a second statement goes in a
separate row.

The list is format 2: a `decider: bl` row is either a test row — cmd exactly
`python3 .backlog/items/<id>/tests.py <name>`, expect exactly `exit 0` — or a
shell row, one simple command with `exit <code>`, kept only for a whole-suite
command such as `go -C scripts test ./...`. The test file's build helper
caches the binary under `$TMPDIR` and reuses it, so a run builds once. Each
whole-suite command (such as `go test ./...` or the project test script)
appears in at most one row. Write the test file now, before the code exists: python3, one named function per assertion, and the main block
copied from `references/design-check-list-schema.md` §3 R37. Give it one
shared `_` helper that builds the binary once into a temp dir and a `_` helper
that sets up a scratch repository, and have every function drive the binary
rather than grep the source. Every helper's name starts with `_`; every public
def is a function a row names; `@_covers("<path>", ...)` names each file the
change touches. Pass the file with `--tests`.

OUTPUT: the numbered check list, the test file, and nothing else.
