You are the PRODUCER for one item in a verify-driven loop.

Repo: <path>. Read AGENTS.md, and read <the files this item concerns>.

ITEM (<id>), verbatim from the backlog:
  <item text>

PRODUCE
  <what to change, and the property it must hold>

CODE QUALITY (for code changes, within this item's scope)
  - Follow the repo's conventions and use the simplest implementation that satisfies the item.
  - Use descriptive names, focused functions, and straightforward control flow; prefer guard
    clauses to deep nesting and keep branching easy to follow.
  - Extract helpers when they clarify intent or consolidate repeated business rules. Keep
    dependencies and side effects explicit; introduce abstractions for current needs.
  - Inspect the size and responsibilities of every code file you change. Keep files cohesive;
    split oversized files with mixed concerns along responsibility boundaries, within the
    authorised file scope. Report a needed split that would exceed that scope.
  - Apply OOP where it fits the language, repo conventions, and domain: encapsulate related
    state and behaviour behind small interfaces, and prefer composition to inheritance.
    Use functions and modules where they express the responsibility more simply.
  - Before reporting, reread your diff and simplify awkward names, avoidable complexity,
    mixed concerns, and unnecessary abstractions. This is part of producing the change,
    with no additional review stage or verification round.

RULES
  - Do NOT commit or push.
  - Do NOT edit the ledger, TODO.md, PROGRESS.md, BRIEF.md, or any spec the item points at.
  - Do NOT fix anything outside this item, even when you find it broken. Report it.
  - Do NOT declare the item passed. A separate verifier decides.
  - Do NOT weaken an existing test to make it pass. If a guard blocks you, report it.
  - Leave no untracked file behind in the repo.
  - Write your evidence (commands and output) to <evidence dir>/<id>-evidence.md.

REPORT BACK: files changed, the property you implemented and how it holds, the
commands that prove it, the repo's test command and its result, and anything you
could not do.
