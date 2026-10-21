#!/usr/bin/env python3
"""Check a verified-backlog's two files for drift.

Three invariants, each cheap to break and expensive to notice:

  1. The progress file has exactly one pointer, it names an item that exists in
     the backlog, and that item is still open. A pointer left on a closed item
     sends the next session into work that is already done.
  2. Every closed item has a record, and that record carries a PASS. A "closed"
     item whose record still says FAIL is a verdict nobody can use.
  3. No item id is declared twice. Two entries sharing one id collapse into one,
     which is how an open item quietly disappears from the backlog.

Run it with a path resolved against **this skill's directory**, not against the repo you are
checking: a relative `scripts/check-backlog.py` resolves inside the target repo, where it does
not exist. Installed at the default OpenCode location, that is
`~/.config/opencode/skills/verified-backlog/scripts/check-backlog.py`.

Usage:
    python3 ~/.config/opencode/skills/verified-backlog/scripts/check-backlog.py \
        --todo TODO.md --progress PROGRESS.md [--lenient] [--id-pattern REGEX]

`--lenient` downgrades the missing-record check to a warning (duplicate ids stay
fatal: a collapsed id is data loss, not a style choice). `--id-pattern` overrides
the item-id rule when a repo uses another scheme (`#12`, `gh-7`).

Exit status: 1 on any problem. Warnings alone exit 0.
"""
from __future__ import annotations

import argparse
import re
import sys
from pathlib import Path

CHECKBOX_RE = re.compile(r"^\s*(?:[-*+]|\d+[.)])\s+\[([ xX])\]\s*(.*)$")
ANY_BOX_RE = re.compile(r"^\s*(?:[-*+]|\d+[.)])\s+\[(.)\]")
HEADING_RE = re.compile(r"^(#{2,6})\s+(.*)$")
# A verdict line: optionally bulleted or bolded, with an optional bolded value.
VERDICT_RE = re.compile(r"^\s*(?:[-*>]\s*)?(?:\*\*)?Verdict(?:\*\*)?\s*:\s*(?:\*\*)?\s*(.*)$", re.I)
PASS_RE = re.compile(r"^PASS", re.I)
# After a PASS, any mention of a failure is a contradiction: "PASS (r1), FAILED (r2)".
FAIL_ANY_RE = re.compile(r"\bfail", re.I)
# In a heading, FAIL is a verdict token, so match the token rather than the word
# "fail" inside prose like "(one fail round)".
FAIL_TOKEN_RE = re.compile(r"\bFAIL\b")
# A heading-only verdict is a trailing PASS, optionally followed by a parenthetical
# reason: "— PASS", "— PASS (one fix round)".
PASS_TAIL_RE = re.compile(r"\bPASS\b(?:\s*\([^)]*\))?\W*$")
# A negated heading ("did not PASS", "no PASS yet") is not a pass.
NEGATED_RE = re.compile(r"\b(?:not|no|never|did)\b", re.I)
RECORDS_LINE_RE = re.compile(r"^\s*(?:\*\*)?Records?(?:\*\*)?\s*:\s*(.+)$", re.I)
FENCE_RE = re.compile(r"^\s*(`{3,}|~{3,})")
QUOTE_RE = re.compile(r"^\s*>")
# Default item id: a short all-caps prefix plus a number, optionally dotted, with an
# optional letter suffix. "E13", "A1", "C6a", "PROJ-12", "E3.1", "INFRA-12" pass.
# "Vue3", "OAuth2" and "Python3" do not, because their prefix is not all-caps. An
# all-caps product code ("S3", "EC2") does pass; use --id-pattern when that bites.
DEFAULT_ID_PATTERN = r"[A-Z][A-Z0-9]{0,9}-?\d+(?:\.\d+)*[A-Za-z]?"

ALL_CLOSED = {
    "all items closed",
    "all closed",
    "everything is closed",
    "all items are closed",
    "nothing open",
    "backlog closed",
    "none",
    "nothing",
    "no items",
    "all done",
    "done",
}

# Compound ids in one heading or list: `E3/E4`, `A1, A2`, `E5&E6`.
COMPOUND_SEP = re.compile(r"[,/&;]")


def strip_markup(text: str) -> str:
    # `~` strips strikethrough, the usual way a dropped item is marked.
    return text.strip().strip("*_`:,.!-—~ ").strip()


INLINE_CODE_RE = re.compile(r"`[^`]*`")


def mask_inline_code(text: str) -> str:
    """Drop `code spans` before hunting for comment markers.

    A backlog item about HTML legitimately contains `<!--`, and reading it as a real
    comment would swallow every later line without a word.
    """
    return INLINE_CODE_RE.sub("", text)


def read_lines(path: Path) -> list[str]:
    # utf-8-sig drops a BOM, which would otherwise hide the first item.
    return path.read_text(encoding="utf-8-sig").splitlines()


def iter_lines(path: Path):
    """Yield (lineno, line, opaque).

    `opaque` marks a line that carries no structure: inside a fence, inside an HTML
    comment, or quoted in a blockquote. A fence closes only on the same character
    with a run at least as long as the opener, so `~~~` inside a ``` block stays
    content.
    """
    fence: tuple[str, int] | None = None
    in_comment = False
    for i, line in enumerate(read_lines(path), 1):
        masked = mask_inline_code(line)
        if in_comment:
            if "-->" in masked:
                in_comment = False
            yield i, line, True
            continue
        if "<!--" in masked and "-->" not in masked:
            in_comment = True
            yield i, line, True
            continue
        if QUOTE_RE.match(line):
            yield i, line, True
            continue
        m = FENCE_RE.match(line)
        if m:
            run = m.group(1)
            if fence and run[0] == fence[0] and len(run) >= fence[1]:
                fence = None
            elif not fence:
                fence = (run[0], len(run))
            yield i, line, True
            continue
        yield i, line, fence is not None


def unclosed_fence(path: Path) -> bool:
    """A stray fence swallows the rest of the file, so an all-closed pointer would pass."""
    fence: tuple[str, int] | None = None
    for line in read_lines(path):
        m = FENCE_RE.match(line)
        if not m:
            continue
        run = m.group(1)
        if fence and run[0] == fence[0] and len(run) >= fence[1]:
            fence = None
        elif not fence:
            fence = (run[0], len(run))
    return fence is not None


def unclosed_comment(path: Path) -> bool:
    """An unclosed `<!--` hides the rest of the file, exactly like an unclosed fence."""
    in_comment = False
    for line in read_lines(path):
        masked = mask_inline_code(line)
        if in_comment:
            if "-->" in masked:
                in_comment = False
        elif "<!--" in masked and "-->" not in masked:
            in_comment = True
    return in_comment


def make_regexes(pattern: str):
    """(scan, whole): `scan` finds an id at the start of prose, `whole` matches one token.

    Whitespace and `-` sit outside the boundaries, so `ABCDEFGHIJK-2` cannot resolve
    to `BCDEFGHIJK-2` and `E1a` does not match inside `xE1ay`. An all-caps product
    code (`S3`, `HTTP2`) does match the default id rule; use --id-pattern then.
    """
    whole = re.compile(f"(?:{pattern})")
    scan = re.compile(f"(?<![\\w-])(?:{pattern})(?![\\w])")
    return scan, whole


def ids_leading(text: str, whole: re.Pattern[str]) -> list[str]:
    """The ids that lead a heading.

    The first token is an id or a `,`/`/`/`&` compound of them. A later token joined
    by a plain space is prose, so `### E4 C2 follow-up` names only E4, and
    `### E4 (E5 still open)` names only E4.
    """
    found: list[str] = []
    carry = False  # the previous token ended on a separator, so this one continues
    for raw in strip_markup(text).split():
        if raw and all(c in ",/&;" for c in raw):
            carry = True  # a separator standing alone continues the run
            continue
        parts = [p for p in (strip_markup(x) for x in COMPOUND_SEP.split(raw)) if p]
        if not parts:
            break
        compound = bool(COMPOUND_SEP.search(raw))
        if all(whole.fullmatch(p) for p in parts) and (not found or carry or compound):
            found.extend(parts)
            carry = compound
            continue
        if not found:
            return []  # the heading does not lead with an id
        break
    return found


def ids_listed(text: str, whole: re.Pattern[str]) -> list[str]:
    """The ids in a `Records:` line: a comma or slash list, stopping at the first non-id."""
    found: list[str] = []
    words = re.sub(r"\band\b", ",", text, flags=re.I)
    for tok in re.split(r"[\s,/&;]+", words):
        cand = strip_markup(tok)
        if not cand:
            continue
        if whole.fullmatch(cand):
            found.append(cand)
        else:
            break  # a parenthetical or a note ends the list
    return found


def parse_backlog(path: Path, whole: re.Pattern[str]):
    """Return (unique order, {id: closed-if-any-entry-was}, unlabelled, duplicates, odd boxes)."""
    order: list[str] = []
    closed_any: dict[str, bool] = {}
    unlabelled: list[tuple[int, str, bool]] = []
    seen_case: dict[str, str] = {}
    duplicates: list[str] = []
    odd_boxes: list[tuple[int, str]] = []
    for lineno, line, opaque in iter_lines(path):
        if opaque:
            continue
        m = CHECKBOX_RE.match(line)
        if not m:
            box = ANY_BOX_RE.match(line)
            if box:
                odd_boxes.append((lineno, line.strip()))
            continue
        closed, rest = m.group(1).lower() == "x", strip_markup(m.group(2))
        tokens = re.split(r"[\s:—–]+", rest, maxsplit=1)
        item_id = strip_markup(tokens[0]) if tokens and tokens[0] else ""
        if not item_id or not whole.fullmatch(item_id):
            unlabelled.append((lineno, line.strip(), closed))
            continue
        key = item_id.upper()
        if key in seen_case:
            duplicates.append(item_id)
        else:
            seen_case[key] = item_id
            order.append(item_id)
        # Every entry counts: a duplicate that is the closed one must not vanish.
        closed_any[item_id] = closed_any.get(item_id, False) or closed
    return order, closed_any, unlabelled, duplicates, odd_boxes


def verdict_of_heading(heading: str) -> bool:
    """A heading-only verdict: a trailing PASS, not negated, with no FAIL token."""
    if NEGATED_RE.search(heading) or FAIL_TOKEN_RE.search(heading):
        return False
    return bool(PASS_TAIL_RE.search(heading))


class _Node:
    """A heading section, with the lines that belong to it and its subsections."""

    __slots__ = ("level", "heading", "lines", "children", "ids")

    def __init__(self, level: int, heading: str):
        self.level = level
        self.heading = heading
        self.lines: list[tuple[str, bool]] = []
        self.children: list["_Node"] = []
        self.ids: list[str] = []


def parse_records(path: Path, whole: re.Pattern[str]):
    """Return ({id: carries a PASS}, {id: heading records}, [issues]).

    Two passes, because whether a section is a record cannot be decided when its
    heading goes by. Pass one builds the section tree; pass two walks it, and a
    subsection that carries its own `Verdict:` or `Records:` line becomes a record
    of its own rather than folding into the container above it. Without that, a
    container with an index line swallows every batch below it and the last batch's
    PASS closes the first batch's failures.

    A heading record always beats a `Records:` line, and the last heading record in
    file order decides. Fenced, quoted and commented lines are held for quoting but
    never read as structure.
    """
    root = _Node(0, "")
    stack = [root]
    for _, line, opaque in iter_lines(path):
        m = None if opaque else HEADING_RE.match(line)
        if not m:
            stack[-1].lines.append((line, opaque))
            continue
        level, text = len(m.group(1)), m.group(2)
        while len(stack) > 1 and stack[-1].level >= level:
            stack.pop()
        node = _Node(level, line)
        node.ids = ids_leading(text, whole)
        stack[-1].children.append(node)
        stack.append(node)

    heading_records: dict[str, bool] = {}
    counts: dict[str, int] = {}
    from_records_line: dict[str, bool] = {}
    issues: list[tuple[str, str]] = []

    def own_structure(node: _Node) -> bool:
        """Does the section carry its own verdict or index line?"""
        return any(not opaque and (VERDICT_RE.match(line) or RECORDS_LINE_RE.match(line)) for line, opaque in node.lines)

    def passed_of(node: _Node, body: list[tuple[str, bool]]) -> bool:
        pairs = [(m.group(1).strip(), opaque) for line, opaque in body if (m := VERDICT_RE.match(line))]
        # The record's own verdict line wins. Quoted or fenced verdicts are how the
        # verifier's text gets pasted in verbatim, so they decide when the record has
        # no verdict line of its own -- a quoted FAIL must not lose to a heading.
        verdicts = [v for v, opaque in pairs if not opaque] or [v for v, opaque in pairs]
        if verdicts:
            return bool(PASS_RE.match(verdicts[-1])) and not FAIL_ANY_RE.search(verdicts[-1])
        inline = re.search(r"Verdict\s*:\s*(.+)$", node.heading, re.I)
        if inline:
            return bool(PASS_RE.match(inline.group(1).strip())) and not FAIL_ANY_RE.search(inline.group(1))
        return verdict_of_heading(node.heading)

    def emit(node: _Node, body: list[tuple[str, bool]]):
        passed = passed_of(node, body)
        listed = [
            item
            for line, opaque in node.lines
            if not opaque and (m := RECORDS_LINE_RE.match(line))
            for item in ids_listed(m.group(1), whole)
        ]
        for item in listed:
            if item in from_records_line and from_records_line[item] != passed:
                issues.append(("warning", f"conflicting `Records:` lines for {item}"))
            from_records_line[item] = passed
        for item in node.ids:
            heading_records[item] = passed
            counts[item] = counts.get(item, 0) + 1

    def absorbed(node: _Node) -> list[tuple[str, bool]]:
        """The lines of every no-id descendant, so they count as this record's detail."""
        out: list[tuple[str, bool]] = []
        for child in node.children:
            if child.ids:
                continue
            out.extend(child.lines)
            out.extend(absorbed(child))
        return out

    def visit(node: _Node, inherited: list[tuple[str, bool]]):
        body = inherited + node.lines
        if node is root:
            for child in node.children:
                visit(child, body)
            return
        if node.ids or own_structure(node):
            body = inherited + node.lines
            if node.ids:
                # A record named by its heading owns the no-id subheadings below it,
                # so a `#### Evidence` block is part of this record's verdict.
                body += absorbed(node)
            emit(node, body)
            for child in node.children:
                if child.ids:
                    visit(child, [])  # a sub-item starts fresh
                elif not node.ids:
                    visit(child, body)  # a container passes its lines down to its batches
            return
        if node.children:
            for child in node.children:
                visit(child, body)  # a bare container passes its lines down
            return

    visit(root, [])
    records = dict(from_records_line)
    records.update(heading_records)  # a heading record always wins
    return records, counts, issues


def parse_pointers(path: Path, prefix: str) -> list[tuple[int, str]]:
    """Every pointer line. The prefix's colon and its markup may move around it."""
    label = re.escape(prefix.rstrip(": "))
    pattern = re.compile(r"^\s*(?:[-*+]\s+)?[\*_]{0,2}" + label + r"[\*_]{0,2}\s*:\s*", re.I)
    found = []
    for lineno, line, opaque in iter_lines(path):
        if opaque:
            continue
        m = pattern.match(line)
        if m:
            found.append((lineno, line[m.end():].strip()))
    return found


def pointer_ids(text: str, whole: re.Pattern[str], scan: re.Pattern[str]) -> list[str]:
    """The item the pointer names, plus its `/`-joined companions.

    The id must lead the pointer's text. `Next item: none until E4 is reviewed`
    names nothing, and a pointer that mentions other ids in prose keeps them as
    prose, so only the leading run counts.
    """
    cleaned = text.strip("*_ ")
    m = scan.match(cleaned)
    if not m:
        return []
    ids = [m.group(0)]
    rest = cleaned[m.end():]
    while True:
        sep = re.match(r"\s*[\*_]{0,2}\s*/\s*[\*_]{0,2}", rest)
        if not sep:
            return ids
        rest = rest[sep.end():]
        nxt = whole.match(rest)
        if not nxt or (nxt.end() < len(rest) and re.match(r"[\w]", rest[nxt.end():])):
            return ids
        ids.append(nxt.group(0))
        rest = rest[nxt.end():]


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--todo", required=True, type=Path)
    ap.add_argument("--progress", required=True, type=Path)
    ap.add_argument("--pointer-prefix", default="Next item:")
    ap.add_argument("--id-pattern", default=DEFAULT_ID_PATTERN, help="regex for one item id (default: short all-caps prefix + number)")
    ap.add_argument("--lenient", action="store_true", help="downgrade the missing-record check to a warning")
    args = ap.parse_args()

    for path in (args.todo, args.progress):
        if not path.exists():
            sys.exit(f"check-backlog: no such file: {path}")

    scan, whole = make_regexes(args.id_pattern)
    hazards = []
    for path in (args.todo, args.progress):
        if unclosed_fence(path):
            hazards.append(f"{path} ends with an unclosed code fence; everything after it is ignored")
        if unclosed_comment(path):
            hazards.append(f"{path} ends with an unclosed HTML comment; everything after it is ignored")

    order, items, unlabelled, duplicates, odd_boxes = parse_backlog(args.todo, whole)
    if not order:
        if not unlabelled:
            for hazard in hazards:
                print(f"PROBLEM   {hazard}")
            sys.exit(f"check-backlog: no checkbox items found in {args.todo}")
        print(f"backlog   {args.todo}: 0 item(s) with an id, {len(unlabelled)} without")
        for hazard in hazards:
            print(f"PROBLEM   {hazard}")
        for lineno, line, _ in unlabelled:
            print(f"PROBLEM   checkbox at {args.todo}:{lineno} carries no id: {line}")
        print(f"\nFAIL {len(unlabelled)} item(s) carry no id; the checker needs `- [ ] <id> <text>`")
        return 1

    records, heading_counts, record_issues = parse_records(args.progress, whole)
    closed = [i for i in order if items[i]]
    open_items = [i for i in order if not items[i]]
    items_upper = {i.upper(): i for i in order}
    unlabelled_open = [line for _, line, was_closed in unlabelled if not was_closed]

    problems: list[str] = []
    warnings: list[str] = []

    # 1. the pointer
    pointers = parse_pointers(args.progress, args.pointer_prefix)
    named: list[str] = []
    if not pointers:
        problems.append(f"no `{args.pointer_prefix}` line in {args.progress}")
    else:
        if len(pointers) > 1:
            where = ", ".join(f"line {n}" for n, _ in pointers)
            problems.append(f"{len(pointers)} `{args.pointer_prefix}` lines ({where}); keep exactly one")
        text = pointers[0][1]
        normalised = re.sub(r"\s+", " ", re.sub(r"[^a-z ]", "", text.lower())).strip()
        if normalised in ALL_CLOSED:
            if open_items or unlabelled_open or odd_boxes:
                still = ", ".join(open_items[:8]) or f"{len(unlabelled_open) + len(odd_boxes)} without a normal id"
                problems.append(f"pointer says everything is closed, but items are open: {still}")
        else:
            named = pointer_ids(text, whole, scan)
            if not named:
                problems.append(f"pointer names no item id: {text!r}")
            for item_id in named:
                real = items_upper.get(item_id.upper())
                if real is None:
                    problems.append(f"pointer names {item_id}, which is not in {args.todo}")
                elif items[real]:
                    problems.append(f"pointer names {item_id}, which is already closed")
            if named and open_items and named[0].upper() != open_items[0].upper():
                warnings.append(f"pointer names {named[0]} but {open_items[0]} is first in the backlog")

    # 2. records
    for item_id in order:
        found = records.get(item_id, records.get(item_id.upper()))
        if found is None:
            if items[item_id]:
                message = f"closed item {item_id} has no record in {args.progress}"
                (warnings if args.lenient else problems).append(message)
            continue
        if not items[item_id]:
            # An open item with a PASS record means a close was half-done.
            (problems if found else warnings).append(f"open item {item_id} already has a record")
        elif not found:
            problems.append(f"record for closed item {item_id} carries no PASS verdict")
        if heading_counts.get(item_id, 0) > 1:
            warnings.append(f"{heading_counts[item_id]} heading records for {item_id}; the last one decides")
    for item_id in sorted(records):
        if item_id.upper() not in items_upper:
            warnings.append(f"record for {item_id} has no matching item in {args.todo}")

    # 3. ids
    for item_id in sorted(set(duplicates)):
        problems.append(f"id {item_id} is reused in {args.todo} (ids are case-insensitive)")
    for lineno, line, was_closed in unlabelled:
        message = f"checkbox at {args.todo}:{lineno} carries no id: {line}"
        (problems if was_closed else warnings).append(message)
    for lineno, line in odd_boxes:
        warnings.append(f"checkbox at {args.todo}:{lineno} uses an unrecognised mark: {line}")

    # 4. parse hazards
    problems.extend(hazards)
    for severity, message in record_issues:
        (problems if severity == "problem" else warnings).append(message)

    print(f"backlog   {args.todo}: {len(order)} item(s), {len(closed)} closed, {len(open_items)} open")
    print(f"records   {args.progress}: {len(records)} id(s) with a record")
    print(f"pointer   {pointers[0][1] if pointers else None!r} -> names {named or 'nothing'}")
    for w in warnings:
        print(f"warn      {w}")
    for p in problems:
        print(f"PROBLEM   {p}")
    if problems:
        print(f"\nFAIL {len(problems)} problem(s)")
        return 1
    print("\nok" + (f" ({len(warnings)} warning(s))" if warnings else ""))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
