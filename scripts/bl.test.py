#!/usr/bin/env python3
"""Gate tests for bl — every gate is proven by a refused call, not by reading the code.

When BL is unset, builds bl from the working tree into a temporary directory. When BL is set,
runs that binary.
Run as a script:  python3 scripts/bl.test.py
Exit status: 0 when every test passes, 1 otherwise.
"""
from __future__ import annotations

import atexit
import hashlib
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
# The binary is run from other working directories, so a relative BL must not follow them.
if "BL" in os.environ:
    BL = os.path.abspath(os.environ["BL"])
else:
    _BL_TEMP = tempfile.TemporaryDirectory(prefix="bl-test-bin-")
    atexit.register(_BL_TEMP.cleanup)
    BL = str(Path(_BL_TEMP.name) / "bl")
    try:
        build = subprocess.run(
            ["go", "-C", str(HERE), "build", "-o", BL, "./cmd/bl"],
            capture_output=True,
            text=True,
            check=False,
        )
    except OSError as exc:
        if isinstance(exc, FileNotFoundError) and exc.filename == "go":
            message = "go not found on PATH"
        else:
            message = f"could not start go: {exc}"
        raise SystemExit(f"failed to build bl from the working tree: {message}") from None
    if build.returncode != 0:
        detail = build.stderr or build.stdout
        raise SystemExit(f"failed to build bl from the working tree:\n{detail}")
RESULTS: list[tuple[str, bool, str]] = []


def run(root: Path, *args: str, stdin: str | None = None) -> subprocess.CompletedProcess:
    return subprocess.run(
        [BL, *args],
        cwd=str(root),
        input=stdin,
        capture_output=True,
        text=True,
        check=False,
    )


def ok(root: Path, *args: str, stdin: str | None = None) -> subprocess.CompletedProcess:
    r = run(root, *args, stdin=stdin)
    assert r.returncode == 0, f"expected ok: bl {' '.join(args)} → {r.returncode} {r.stderr}"
    return r


def refused(root: Path, *args: str, stdin: str | None = None, match: str | None = None) -> None:
    before = ledger_lines(root)
    r = run(root, *args, stdin=stdin)
    assert r.returncode == 2, f"expected refusal: bl {' '.join(args)} → {r.returncode} {r.stdout}{r.stderr}"
    if match:
        assert match.lower() in r.stderr.lower(), f"refusal message lacks {match!r}: {r.stderr}"
    assert ledger_lines(root) == before, f"a refusal wrote to the ledger: bl {' '.join(args)}"


def ledger_lines(root: Path) -> int:
    p = root / ".backlog" / "ledger.jsonl"
    return len(p.read_text(encoding="utf-8").splitlines()) if p.exists() else 0


def binary_verbs() -> set[str]:
    """The verbs this binary has, read from the usage output the binary prints itself.

    The set is never typed here. It is parsed out of the compiled binary's own
    "verbs:" list, so a verb the binary grows is a verb the gate knows about
    without the gate being edited.
    """
    with tempfile.TemporaryDirectory(prefix="bl-verbs-") as tmp:
        r = subprocess.run([BL], cwd=tmp, capture_output=True, text=True, check=False)
    lines = (r.stdout + r.stderr).splitlines()
    start = next((i for i, ln in enumerate(lines) if ln.strip() == "verbs:"), None)
    assert start is not None, "the binary printed no verb list in its usage output"
    verbs: set[str] = set()
    for line in lines[start + 1:]:
        s = line.strip()
        if not s:
            break
        if "|" not in s and verbs:
            break
        verbs.update(tok.strip() for tok in s.split("|") if tok.strip())
    assert verbs, "the binary printed an empty verb list"
    return verbs


def test(fn):
    try:
        fn()
        RESULTS.append((fn.__name__, True, ""))
    except AssertionError as exc:
        RESULTS.append((fn.__name__, False, str(exc)))
    except Exception as exc:  # noqa: BLE001 - a broken test is a failed test
        RESULTS.append((fn.__name__, False, f"{type(exc).__name__}: {exc}"))
    return fn


def repo() -> Path:
    root = Path(tempfile.mkdtemp(prefix="bl-test-"))
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    subprocess.run(["git", "config", "user.email", "a@b.c"], cwd=root, check=True)
    subprocess.run(["git", "config", "user.name", "a"], cwd=root, check=True)
    (root / "BRIEF.md").write_text("brief\n", encoding="utf-8")
    # the artefacts are never part of a verified tree, and .gitignore is what keeps them out
    (root / ".gitignore").write_text(".backlog/\n", encoding="utf-8")
    subprocess.run(["git", "add", "."], cwd=root, check=True)
    subprocess.run(["git", "commit", "-qm", "base"], cwd=root, check=True)
    return root


ROOTS: list[Path] = []


def fresh() -> Path:
    r = repo()
    ROOTS.append(r)
    ok(r, "init")
    return r


def head(root: Path) -> str:
    return subprocess.run(
        ["git", "rev-parse", "HEAD"], cwd=root, capture_output=True, text=True, check=True
    ).stdout.strip()


# ── the round path ────────────────────────────────────────────────────────────

# The round path's list. The binary row S1 passes on its own: the binary computes its
# row-verdict from the observed values against the row's expect, so no judge can make it fail
# (R7). A round that has to fail fails through the judge row J1, whose row-verdict is the
# judge's.
ROUND_LIST = (
    "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\tcriterion\n"
    "S1\tbl\tblocking\tB1\techo two\texit 0\t\t\tprints two\n"
    "J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n"
)


def tree_of(root: Path) -> str:
    """The verified tree, computed the way `bl` computes it: the working tree, less .git/,
    less the paths .gitignore excludes. A separate index keeps the repository's own index
    untouched."""
    tmp = tempfile.mkdtemp(prefix="bl-idx-")
    env = dict(os.environ, GIT_INDEX_FILE=str(Path(tmp) / "idx"))
    subprocess.run(["git", "add", "-A", "."], cwd=root, env=env, check=True, capture_output=True)
    out = subprocess.run(
        ["git", "write-tree"], cwd=root, env=env, capture_output=True, text=True, check=True
    )
    shutil.rmtree(tmp, ignore_errors=True)
    return out.stdout.strip()


def latest_run(root: Path) -> str:
    events = [
        json.loads(line)
        for line in (root / ".backlog" / "ledger.jsonl").read_text(encoding="utf-8").splitlines()
    ]
    return [e["run"] for e in events if e["t"] == "run"][-1]


def verdicts(root: Path, *rows: str) -> Path:
    """A verdicts file (§2) for the item's latest run: the header, the run and the tree it
    names, and one line per frozen check id. It is written outside the repository: a file
    inside it would move the tree the round verifies."""
    path = Path(tempfile.mkdtemp(prefix="bl-verdicts-")) / "verdicts.tsv"
    path.write_text(
        "id\trow_verdict\tanchor\n"
        f"run {latest_run(root)}\n"
        f"tree {tree_of(root)}\n"
        + "".join(row + "\n" for row in rows),
        encoding="utf-8",
    )
    return path


def ready(root: Path) -> Path:
    """An item that is ready to be rounded: the list is frozen, the claims are recorded and the
    run has happened. It returns the verdicts file for that run — a round consumes a run and a
    verdicts file together."""
    events = [
        json.loads(line)
        for line in (root / ".backlog" / "ledger.jsonl").read_text(encoding="utf-8").splitlines()
        if line.strip()
    ]
    kinds = {e["t"] for e in events}
    if "file" not in kinds:
        ok(root, "file", "--id", "A1", "--text", "first")
    if "open" not in kinds:
        ok(root, "open", "A1")
    # the list and the claims are written outside the repository: a file inside it would be
    # part of the diff the round checks, and no blocking row would cover it
    outside = Path(tempfile.mkdtemp(prefix="bl-item-"))
    listing = outside / "checks.tsv"
    listing.write_text(ROUND_LIST, encoding="utf-8")
    ok(root, "checks", "A1", "--file", str(listing))
    claims = outside / "claims.md"
    claims.write_text("claims\n", encoding="utf-8")
    ok(root, "claims", "A1", "--file", str(claims))
    ok(root, "run", "A1")
    return verdicts(root, "S1\tPASS\t", "J1\tPASS\t\"two\"")


def item_file(name: str, text: str) -> Path:
    """A file outside the repository: a file inside it would move the tree the round verifies."""
    path = Path(tempfile.mkdtemp(prefix="bl-item-")) / name
    path.write_text(text, encoding="utf-8")
    return path


def attempt_one_files(root: Path) -> list[Path]:
    """The files attempt 1 left behind: the item's own artefacts and its run and round evidence."""
    items = sorted(p for p in (root / ".backlog" / "items" / "A1").iterdir() if p.is_file())
    return items + [
        root / ".backlog" / "evidence" / "A1-run1.json",
        root / ".backlog" / "evidence" / "A1-r1.txt",
    ]


def digest(path: Path) -> str:
    return hashlib.sha256(path.read_bytes()).hexdigest()


def fail_round(root: Path) -> None:
    """A code FAIL round: the judge row fails, so the derived verdict does."""
    rows = verdicts(root, "S1\tPASS\t", 'J1\tFAIL\t"two"')
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows))


def restart(root: Path, why: str, change: str, reason: str = "bad-check") -> subprocess.CompletedProcess:
    return ok(root, "restart", "A1", "--reason", reason, "--why", why, "--change", change)


# ── init ─────────────────────────────────────────────────────────────────────


@test
def init_refuses_twice():
    root = fresh()
    refused(root, "init", match="already exists")


@test
def init_refuses_foreign_view():
    root = repo()
    ROOTS.append(root)
    (root / "TODO.md").write_text("# a real backlog\n\n- [ ] A1 do it\n", encoding="utf-8")
    refused(root, "init", match="not rendered by bl")
    assert not (root / ".backlog").exists(), "init created state before refusing"


@test
def init_creates_views():
    root = fresh()
    assert (root / "TODO.md").exists() and (root / "PROGRESS.md").exists()
    assert "rendered from .backlog/ledger.jsonl" in (root / "TODO.md").read_text()


# ── file ─────────────────────────────────────────────────────────────────────


@test
def file_refuses_duplicate_id():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    refused(root, "file", "--id", "a1", "--text", "again", match="already exists")


@test
def file_refuses_unknown_dependency():
    root = fresh()
    refused(root, "file", "--id", "A1", "--text", "x", "--after", "NOPE", match="names no item")


@test
def file_refuses_self_dependency():
    root = fresh()
    refused(root, "file", "--id", "A1", "--text", "x", "--after", "A1", match="itself")


@test
def file_refuses_multiline_text():
    root = fresh()
    refused(root, "file", "--id", "A1", "--text", "one\ntwo", match="one line")


@test
def file_refuses_bad_id():
    root = fresh()
    refused(root, "file", "--id", "not an id", "--text", "x", match="does not match")


@test
def agent_file_requires_proof_and_bucket_and_parks_inbox_entries():
    root = fresh()
    refused(root, "file", "--id", "D1", "--text", "finding", "--source", "Verifier:A1/r1",
            match="--proof")
    refused(root, "file", "--id", "D1", "--text", "finding", "--source", "Verifier:A1/r1",
            "--proof", "", "--bucket", "later", match="--proof")
    refused(root, "file", "--id", "D1", "--text", "finding", "--source", "producer:A1",
            "--proof", "go test -> reproduced", match="fix-now, batch or later")
    refused(root, "file", "--id", "D1", "--text", "finding", "--source", "loop-run",
            "--proof", "go test -> reproduced", "--bucket", "soon", match="fix-now, batch or later")
    ok(root, "file", "--id", "D1", "--text", "finding", "--source", "verifier:A1/r1",
       "--proof", "go test -> reproduced", "--bucket", "later")
    assert ok(root, "next").stdout.strip() == "none"
    assert "items 1 · parked 1" in ok(root, "status").stdout
    shown = ok(root, "show", "D1").stdout
    assert "proof: go test -> reproduced" in shown and "bucket: later" in shown
    ok(root, "reopen", "D1")
    assert "D1" in ok(root, "next").stdout
    ok(root, "drop", "D1", "--reason", "declined after triage")


@test
def agent_fix_now_file_stays_open():
    root = fresh()
    ok(root, "file", "--id", "D1", "--text", "urgent finding", "--source", "supervisor:run",
       "--proof", "bl check -> corrupt ledger reproduced", "--bucket", "fix-now")
    assert "D1" in ok(root, "next").stdout
    assert "items 1 · open 1" in ok(root, "status").stdout


@test
def non_agent_file_keeps_optional_proof_and_bucket():
    root = fresh()
    ok(root, "file", "--id", "H1", "--text", "human item", "--source", "human")
    assert "H1" in ok(root, "next").stdout


# ── open ─────────────────────────────────────────────────────────────────────


@test
def open_refuses_blocked_item():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "file", "--id", "A2", "--text", "second", "--after", "A1")
    refused(root, "open", "A2", match="blocked by A1")


@test
def open_refuses_second_item():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "file", "--id", "A2", "--text", "second")
    ok(root, "open", "A1")
    refused(root, "open", "A2", match="already in flight")


@test
def open_refuses_closed_item():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--rows", str(rows))
    ok(root, "close", "A1", "--commit", head(root))
    refused(root, "open", "A1", match="not open")


# ── round ────────────────────────────────────────────────────────────────────


@test
def round_refuses_without_pin():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    refused(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n", match="not the item in flight")


@test
def round_refuses_fail_without_cause():
    root = fresh()
    ready(root)
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
    refused(root, "round", "A1", "--verdict", "FAIL", "--rows", str(rows), match="needs --cause")


@test
def round_refuses_pass_with_cause():
    root = fresh()
    rows = ready(root)
    refused(root, "round", "A1", "--verdict", "PASS", "--cause", "code", "--rows", str(rows))


@test
def round_refuses_empty_rows():
    root = fresh()
    ready(root)
    # a PASS round needs the design's verdicts file: an empty one is refused
    empty = Path(tempfile.mkdtemp(prefix="bl-empty-")) / "verdicts.tsv"
    empty.write_text("", encoding="utf-8")
    refused(root, "round", "A1", "--verdict", "PASS", "--rows", str(empty), match="verdicts file is empty")
    # and so is a round with no verdicts file at all
    refused(root, "round", "A1", "--verdict", "PASS", match="needs the verdicts file")
    # and a free-text table is not a verdicts file: the header names the three columns of §2
    table = Path(tempfile.mkdtemp(prefix="bl-table-")) / "rows.txt"
    table.write_text("S1\tPASS\n", encoding="utf-8")
    refused(root, "round", "A1", "--rows", str(table), match="unknown column")


@test
def round_refuses_unknown_verdict():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    refused(root, "round", "A1", "--verdict", "VOID", "--rows", "-", stdin="x\n")


@test
def cap_counts_code_only():
    root = fresh()
    ready(root)
    for _ in range(3):
        ok(root, "run", "A1")
        rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
        ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows))
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
    refused(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows), match="cap 3")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "harness")
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tPASS\t\"two\"")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))


@test
def round_refuses_after_pass():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
    refused(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows), match="already passed")


# ── run ──────────────────────────────────────────────────────────────────────


@test
def run_writes_the_run_file_and_records_the_event():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    listing = root / "checks.tsv"
    listing.write_text(
        "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\tcriterion\n"
        "S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tone row\n",
        encoding="utf-8",
    )
    ok(root, "checks", "A1", "--file", str(listing))
    claims = root / "claims.md"
    claims.write_text("claims\n", encoding="utf-8")
    ok(root, "claims", "A1", "--file", str(claims))
    ok(root, "run", "A1")
    run_file = root / ".backlog" / "evidence" / "A1-run1.json"
    assert run_file.exists() and run_file.stat().st_size > 0, "the run wrote no run file"
    ledger = (root / ".backlog" / "ledger.jsonl").read_text(encoding="utf-8")
    assert '"run"' in ledger, "the ledger holds no run event"


# ── close ────────────────────────────────────────────────────────────────────


@test
def close_refuses_without_pass():
    root = fresh()
    ready(root)
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows))
    refused(root, "close", "A1", "--commit", head(root), match="no PASS")


@test
def close_refuses_dirty_tree():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    (root / "BRIEF.md").write_text("changed\n", encoding="utf-8")
    refused(root, "close", "A1", "--commit", head(root), match="R18")


@test
def close_refuses_unknown_commit():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    refused(root, "close", "A1", "--commit", "deadbeef", match="does not exist")


@test
def close_ignores_its_own_artifacts():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "close", "A1", "--commit", head(root))
    assert "- [x] A1" in (root / "TODO.md").read_text()


# ── drop / defer / reopen ────────────────────────────────────────────────────


@test
def close_resolves_a_symbolic_commit():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "close", "A1", "--commit", "HEAD")
    event = json.loads((root / ".backlog" / "ledger.jsonl").read_text().splitlines()[-1])
    assert len(event["commit"]) == 40, f"a moving ref must be resolved to a sha, got {event['commit']!r}"
    assert event["commit"] == head(root)


@test
def drop_requires_reason_and_is_not_a_pass():
    root = fresh()
    ok(root, "file", "--id", "D1", "--text", "cosmetic")
    refused(root, "drop", "D1", "--reason", "  ", match="reason is empty")
    ok(root, "drop", "D1", "--reason", "not worth it")
    queue = (root / "TODO.md").read_text()
    assert "## Declined (1)" in queue and "not worth it" in queue
    records = (root / "PROGRESS.md").read_text()
    assert "Decision: declined" in records and "PASS" not in records


@test
def defer_hides_from_next_and_reopens():
    root = fresh()
    ok(root, "file", "--id", "O4", "--text", "optimisation")
    ok(root, "defer", "O4", "--reason", "awaiting triage")
    assert ok(root, "next").stdout.strip() == "none"
    ok(root, "reopen", "O4")
    assert ok(root, "next").stdout.startswith("O4")


@test
def reopen_refuses_an_open_item():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    refused(root, "reopen", "A1", match="only a declined or parked")


# ── next / status ────────────────────────────────────────────────────────────


@test
def next_skips_blocked_items():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "file", "--id", "A2", "--text", "second", "--after", "A1")
    assert ok(root, "next").stdout.startswith("A1")
    ok(root, "open", "A1")
    assert ok(root, "next").stdout.startswith("A1")
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "close", "A1", "--commit", head(root))
    assert ok(root, "next").stdout.startswith("A2")


@test
def next_works_tier_one_then_two_then_three():
    root = fresh()
    for i in range(1, 5):
        ok(root, "file", "--id", f"A{i}", "--text", f"item {i}")
    ok(root, "file", "--id", "A5", "--text", "waits", "--after", "A4")
    assert ok(root, "next").stdout.startswith("A4"), "tier 3: the waited-on item first"
    ranking = root / "rank.tsv"
    ranking.write_text("A3\tunblocks the release\n", encoding="utf-8")
    ok(root, "rank", "--file", str(ranking))
    assert ok(root, "next").stdout.startswith("A3"), "tier 2 before tier 3"
    ok(root, "bump", "A2")
    assert ok(root, "next").stdout.startswith("A2"), "tier 1 before tier 2"
    ok(root, "file", "--id", "U1", "--text", "urgent", "--urgent")
    assert ok(root, "next").stdout.startswith("U1"), "--urgent goes to the front of tier 1"
    ok(root, "unbump", "U1")
    assert ok(root, "next").stdout.startswith("A2")
    events = [json.loads(ln) for ln in (root / ".backlog" / "ledger.jsonl").read_text().splitlines()]
    by = {e["t"]: e.get("by") for e in events if e["t"] in ("bump", "unbump", "rank")}
    assert by == {"bump": "human", "unbump": "human", "rank": "loop"}, by
    ranking.write_text("A2\tbumped already\n", encoding="utf-8")
    refused(root, "rank", "--file", str(ranking), match="tier 1")
    ranking.write_text("A3\t\n", encoding="utf-8")
    refused(root, "rank", "--file", str(ranking), match="empty reason")
    refused(root, "bump", "Z9", match="unknown item")
    refused(root, "bump", "A1", "a1", match="named twice")
    ok(root, "check")


@test
def status_counts_every_state():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "one")
    ok(root, "file", "--id", "A2", "--text", "two", "--after", "A1")
    ok(root, "file", "--id", "A3", "--text", "three")
    out = ok(root, "status").stdout
    assert "items 3" in out and "open 2" in out and "blocked 1" in out


# ── views and evidence ───────────────────────────────────────────────────────


@test
def hand_edited_view_is_caught():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "check", "--views")
    with (root / "TODO.md").open("a", encoding="utf-8") as fh:
        fh.write("- [ ] ZZ — hand added\n")
    r = run(root, "check", "--views")
    assert r.returncode == 1 and "stale or hand-edited" in r.stdout
    ok(root, "render")
    ok(root, "check", "--views")


@test
def tampered_evidence_is_caught():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ev = next((root / ".backlog" / "evidence").glob("*-r*.txt"))
    with ev.open("a", encoding="utf-8") as fh:
        fh.write("tampered\n")
    r = run(root, "check")
    assert r.returncode == 1 and "does not match its hash" in r.stdout


# ── log ──────────────────────────────────────────────────────────────────────


@test
def run_log_records_every_call():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    run(root, "file", "--id", "A1", "--text", "dup")
    log = [json.loads(l) for l in (root / ".backlog" / "run.log").read_text().splitlines()]
    assert log[-1]["exit"] == 2, "a refusal must be logged"
    assert log[-2]["exit"] == 0
    assert all("argv" in r and "ms" in r and "at" in r for r in log)
    assert log[-2]["verb"] == "file" and "rule" not in log[-2]
    assert log[-1]["verb"] == "file" and log[-1]["rule"] == ""


# ── audit ────────────────────────────────────────────────────────────────────


@test
def audit_writes_report_and_json():
    root = fresh()
    ready(root)
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tFAIL\t\"two\"")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", str(rows))
    ok(root, "run", "A1")
    rows = verdicts(root, "S1\tPASS\t", "J1\tPASS\t\"two\"")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "audit", "--since", "2d")
    audit_dir = root / ".backlog" / "audit"
    files = sorted(p.name for p in audit_dir.iterdir())
    assert any(f.endswith(".md") for f in files) and any(f.endswith(".json") for f in files)
    assert str(audit_dir) not in (root / "PROGRESS.md").read_text()


@test
def audit_reports_refusal_rules_and_legacy_lines():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    refused(root, "file", "--id", "A1", "--text", "duplicate")
    refused(root, "round", "A1", "--verdict", "PASS", "--rows", "-")
    log = root / ".backlog" / "run.log"
    lines = log.read_text(encoding="utf-8").splitlines()
    old = json.loads(lines[0])
    lines[0] = json.dumps({"at": old["at"], "argv": [], "exit": 2, "ms": 0})
    log.write_text("\n".join(lines) + "\n", encoding="utf-8")
    result = ok(root, "audit", "--since", "2d", "--json")
    report = json.loads(result.stdout)
    assert report["refused_calls"] == 3, report
    assert report["refusals_by_rule"] == {"unnamed": 1, "unrecorded": 1, "R35": 1}, report
    markdown = next((root / ".backlog" / "audit").glob("*.md")).read_text(encoding="utf-8")
    section = markdown.split("## refusals by rule\n\n", 1)[1].split("\n\n", 1)[0]
    assert section.splitlines() == ["- R35: 1", "- unnamed: 1", "- unrecorded: 1"], section


@test
def audit_due_reports_absence_then_age():
    root = fresh()
    r = run(root, "audit", "--due")
    assert r.returncode == 0 and "due: no audit yet" in r.stdout, r.stdout
    ok(root, "audit", "--since", "2d")
    r = run(root, "audit", "--due")
    assert r.stdout.startswith("not due:"), r.stdout


# ── surfaces not covered by a refusal ────────────────────────────────────────


@test
def note_is_rendered_on_the_record():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "note", "A1", "--text", "worth filing later")
    assert "note: worth filing later" in (root / "PROGRESS.md").read_text()


@test
def show_and_schema_emit_valid_json():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    shown = json.loads(ok(root, "show", "A1", "--json").stdout)
    assert shown["ID"] == "A1" and shown["Status"] == "open", shown
    schema = json.loads(ok(root, "schema", "--json").stdout)
    assert schema["version"] == 1, schema
    assert schema["round_cap"] == 3, schema
    assert schema["causes"] == ["code", "harness", "dispute"], schema
    assert sorted(schema["events"]) == [
        "baseline", "brief", "bump", "checks", "claims", "close", "defer", "drop", "file",
        "misclassified", "note", "open", "packet", "rank", "rebaseline", "reopen", "restart", "round",
        "run", "second-check", "signoff", "unbump",
    ], schema
    assert schema["events"]["restart"]["required"] == [
        "id", "reason", "why", "change", "attempt", "record", "record_sha",
    ], schema
    assert schema["events"]["restart"]["optional"] == [], schema
    assert schema["events"]["bump"]["required"] == ["ids", "by"], schema
    assert schema["events"]["rank"]["required"] == ["ranking", "by"], schema


@test
def root_flag_works_from_another_directory():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    r = subprocess.run([BL, "--root", str(root), "status"], cwd="/tmp", capture_output=True, text=True)
    assert r.returncode == 0 and "items 1" in r.stdout, r.stdout + r.stderr


@test
def next_says_none_on_an_empty_ledger():
    root = fresh()
    assert ok(root, "next").stdout.strip() == "none"


@test
def a_corrupt_line_exits_3():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    with (root / ".backlog" / "ledger.jsonl").open("a", encoding="utf-8") as fh:
        fh.write("{broken\n")
    r = run(root, "status")
    assert r.returncode == 3 and "corrupt ledger" in r.stderr, (r.returncode, r.stderr)


@test
def a_repeated_seq_exits_3():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ledger = root / ".backlog" / "ledger.jsonl"
    first = ledger.read_text().splitlines()[0]
    with ledger.open("a", encoding="utf-8") as fh:
        fh.write(first + "\n")
    r = run(root, "status")
    assert r.returncode == 3 and "strictly increasing" in r.stderr, (r.returncode, r.stderr)


@test
def the_documented_verbs_are_the_real_verbs():
    skill = (HERE.parent / "SKILL.md").read_text()
    declared = binary_verbs()
    for verb in sorted(declared):
        taught = any(n in skill for n in (f"bl {verb}", f"`{verb}", f'"$BL" {verb}'))
        assert taught, f"SKILL.md never teaches bl {verb}"
    # only command-looking text counts: inline code spans and fenced blocks
    spans = re.findall(r"```[^`]*```|`[^`\n]+`", skill)
    used = {v for span in spans for v in re.findall(r"\bbl ([a-z][a-z-]*)", span)}
    invented = used - declared
    assert not invented, f"SKILL.md uses verbs the binary does not have: {sorted(invented)}"


# ── restart ──────────────────────────────────────────────────────────────────


@test
def restart_ends_the_attempt_and_starts_the_next():
    root = fresh()
    ready(root)
    fail_round(root)
    r = restart(root, "row J1 reads the wrong file", "the row names the file it reads")
    assert r.stdout == "bl: A1 restarted as attempt 2\n", r.stdout
    shown = ok(root, "show", "A1").stdout.splitlines()
    assert shown[1].startswith("status: open") and shown[2] == "attempt: 2", shown
    assert json.loads(ok(root, "show", "A1", "--json").stdout)["Attempt"] == 2
    event = json.loads((root / ".backlog" / "ledger.jsonl").read_text().splitlines()[-1])
    assert event["t"] == "restart" and event["attempt"] == 1, event
    assert event["reason"] == "bad-check" and event["why"] and event["change"], event
    assert "by" not in event, "the restart records no actor"
    record = root / event["record"]
    assert record.exists() and digest(record) == event["record_sha"], event
    assert ok(root, "next").stdout.startswith("A1"), "a restarted item is eligible again"
    ok(root, "open", "A1")
    ok(root, "checks", "A1", "--file", str(item_file("checks.tsv", ROUND_LIST)))
    assert (root / ".backlog" / "items" / "A1" / "a2" / "checks.tsv").exists()


@test
def restart_keeps_the_earlier_attempts_files_and_carries_its_record():
    root = fresh()
    ready(root)
    fail_round(root)
    before = {p: digest(p) for p in attempt_one_files(root)}
    restart(root, "the list is the wrong premise", "a new list")
    ok(root, "open", "A1")
    ok(root, "checks", "A1", "--file", str(item_file("checks.tsv", ROUND_LIST)))
    ok(root, "claims", "A1", "--file", str(item_file("claims.md", "claims\n")))
    ok(root, "run", "A1")
    after = {p: digest(p) for p in before}
    assert before == after, "attempt 2 rewrote a file of attempt 1"
    brief = (root / ".backlog" / "items" / "A1" / "a2" / "brief.md").read_bytes()
    record = (root / ".backlog" / "items" / "A1" / "a1-failure.md").read_bytes()
    assert brief.endswith(record), "attempt 2's brief must end with attempt 1's record"


@test
def restart_excuses_the_damage_its_failure_record_lists():
    root = fresh()
    rows = ready(root)
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", str(rows))
    ok(root, "close", "A1", "--commit", head(root))
    evidence = root / ".backlog" / "evidence" / "A1-r1.txt"
    evidence.write_text(evidence.read_text() + "tampered\n", encoding="utf-8")
    r = run(root, "check")
    assert r.returncode == 1 and "A1 round .backlog/evidence/A1-r1.txt does not match its hash" in r.stdout, r.stdout
    restart(root, "the round evidence was overwritten", "bl check reports it", reason="damaged-evidence")
    r = run(root, "check")
    assert r.returncode == 0, f"the record must excuse the damage it lists: {r.stdout}"
    shown = ok(root, "show", "A1").stdout
    assert "status: open" in shown and "commit:" not in shown, shown
    evidence.write_text(evidence.read_text() + "again\n", encoding="utf-8")
    assert run(root, "check").returncode == 1, "a second edit is not the damage the record lists"


@test
def restart_limit_parks_the_item_for_a_human():
    root = fresh()
    ready(root)
    for n in (1, 2):
        fail_round(root)
        restart(root, f"attempt {n} why", f"attempt {n} change")
        ok(root, "open", "A1")
        ok(root, "checks", "A1", "--file", str(item_file("checks.tsv", ROUND_LIST)))
        ok(root, "claims", "A1", "--file", str(item_file("claims.md", "claims\n")))
        ok(root, "run", "A1")
    fail_round(root)
    before = ledger_lines(root)
    r = run(root, "restart", "A1", "--reason", "bad-check", "--why", "the third", "--change", "the third change")
    assert r.returncode == 2 and r.stderr.startswith("bl: refused"), r.stderr
    assert ledger_lines(root) == before + 1, "the limit refusal appends exactly one event"
    assert "restart limit" in (root / "PROGRESS.md").read_text(), "the defer reason names the limit"
    assert ok(root, "next").stdout.strip() == "none", "a parked item is not named"
    ok(root, "reopen", "A1")
    r = run(root, "restart", "A1", "--reason", "bad-check", "--why", "the third", "--change", "the third change")
    assert r.returncode == 2 and "restart limit" in (root / "PROGRESS.md").read_text()


@test
def restart_refusals_leave_the_ledger_alone():
    root = fresh()
    ready(root)
    refused(root, "restart", "Z9", "--reason", "bad-check", "--why", "x", "--change", "y", match="unknown item")
    refused(root, "restart", "A1", "--reason", "Bad-Check", "--why", "x", "--change", "y", match="--reason must be one of")
    refused(root, "restart", "A1", "--reason", "bad-check", "--why", " ", "--change", "y", match="why is empty")
    refused(root, "restart", "A1", "--reason", "bad-check", "--why", "x", "--change", "y", match="no FAIL round")
    ok(root, "defer", "A1", "--reason", "later")
    refused(root, "restart", "A1", "--reason", "bad-check", "--why", "x", "--change", "y", match="parked")


# ── legacy import ────────────────────────────────────────────────────────────


@test
def import_round_trips_counts_and_refuses_second_run():
    root = Path(tempfile.mkdtemp(prefix="bl-import-"))
    ROOTS.append(root)
    (root / "TODO.md").write_text(
        "# todos\n\n"
        "- [x] A1 first — **PASS**: did it (commit abcdef1)\n"
        "- [ ] A2 second\n"
        "- [ ] A3 third\n",
        encoding="utf-8",
    )
    (root / "PROGRESS.md").write_text(
        "# records\n\n### A1 — first — PASS\n\nVerdict: PASS\n\nclosed at abcdef1\n",
        encoding="utf-8",
    )
    subprocess.run(["git", "init", "-q"], cwd=root, check=True)
    ok(root, "import", "TODO.md", "PROGRESS.md")
    out = ok(root, "status").stdout
    assert "items 3" in out and "closed 1" in out and "open 2" in out
    assert "- [x] A1" in (root / "TODO.md").read_text()
    refused(root, "import", "TODO.md", "PROGRESS.md", match="not empty")


@test
def lint_reports_unparseable_lines():
    root = Path(tempfile.mkdtemp(prefix="bl-lint-"))
    ROOTS.append(root)
    (root / "TODO.md").write_text("- [ ] no id here\n- [ ] B2 has one\n", encoding="utf-8")
    (root / "PROGRESS.md").write_text("", encoding="utf-8")
    r = run(root, "lint", "TODO.md", "PROGRESS.md")
    assert r.returncode == 1 and "checkbox without a readable id" in r.stdout
    assert "parsed 1 items" in r.stdout


# ── the frozen test file and the format-2 row (I89, R37) ─────────────────────

TEST_TEMPLATE = """import sys


def _covers(*paths):
    def mark(fn):
        return fn

    return mark


@_covers("scripts/cmd/bl/main.go")
def a():
    assert True


if __name__ == "__main__":
    name = sys.argv[1] if len(sys.argv) > 1 else ""
    print("bl-test: " + name)
    fn = globals().get(name)
    if name.startswith("_") or not callable(fn):
        sys.exit(4)
    try:
        fn()
    except AssertionError:
        sys.exit(1)
    except Exception:
        import traceback

        traceback.print_exc()
        sys.exit(3)
"""


def r37_item() -> tuple[Path, Path]:
    """An open item A1, and a directory outside the repository holding a checks list with one
    test row and the test file it names."""
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    outside = Path(tempfile.mkdtemp(prefix="bl-r37-"))
    listing = outside / "c.tsv"
    listing.write_text(
        "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\tcriterion\n"
        "R1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tthe test a\n",
        encoding="utf-8",
    )
    return root, listing


@test
def r37_checks_copies_the_test_file_and_records_its_hash():
    root, listing = r37_item()
    tests = Path(tempfile.mkdtemp(prefix="bl-r37-tests-")) / "t.py"
    tests.write_text(TEST_TEMPLATE, encoding="utf-8")
    ok(root, "checks", "A1", "--file", str(listing), "--tests", str(tests))
    copied = root / ".backlog" / "items" / "A1" / "tests.py"
    assert copied.read_bytes() == tests.read_bytes(), "the test file is not copied byte for byte"
    ev = json.loads((root / ".backlog" / "ledger.jsonl").read_text(encoding="utf-8").splitlines()[-1])
    assert ev["format"] == 2, ev
    assert ev["tests"] == ".backlog/items/A1/tests.py", ev
    assert ev["tests_sha"] == hashlib.sha256(tests.read_bytes()).hexdigest(), ev


@test
def r37_a_test_row_expect_is_exit_zero():
    root, listing = r37_item()
    bad = listing.parent / "bad.tsv"
    bad.write_text(listing.read_text(encoding="utf-8").replace("\texit 0\t", "\tstdout matches ok\t"), encoding="utf-8")
    tests = Path(tempfile.mkdtemp(prefix="bl-r37-tests-")) / "t.py"
    tests.write_text(TEST_TEMPLATE, encoding="utf-8")
    r = run(root, "checks", "A1", "--file", str(bad), "--tests", str(tests))
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert r.stderr.startswith("bl: refused"), r.stderr
    assert "R1" in r.stderr and "(R37)" in r.stderr, r.stderr
    assert not (root / ".backlog" / "items" / "A1" / "tests.py").exists()


@test
def r37_the_probe_refuses_a_test_that_exits_three():
    root, listing = r37_item()
    tests = Path(tempfile.mkdtemp(prefix="bl-r37-tests-")) / "t.py"
    tests.write_text(TEST_TEMPLATE.replace('assert True', 'raise NameError("boom")'), encoding="utf-8")
    r = run(root, "checks", "A1", "--file", str(listing), "--tests", str(tests))
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "exit 3" in r.stderr and "R1" in r.stderr and "(R37)" in r.stderr, r.stderr
    assert not (root / ".backlog" / "items" / "A1" / "tests.py").exists(), "a refused probe left the test file"


@test
def r37_the_frozen_test_file_is_rehashed_before_a_run():
    root, listing = r37_item()
    tests = Path(tempfile.mkdtemp(prefix="bl-r37-tests-")) / "t.py"
    tests.write_text(TEST_TEMPLATE, encoding="utf-8")
    ok(root, "checks", "A1", "--file", str(listing), "--tests", str(tests))
    frozen = root / ".backlog" / "items" / "A1" / "tests.py"
    with frozen.open("a", encoding="utf-8") as fh:
        fh.write("# edited\n")
    r = run(root, "run", "A1")
    assert r.returncode == 2, (r.returncode, r.stderr)
    assert "tests.py" in r.stderr and "(R20)" in r.stderr, r.stderr


def main() -> int:
    failed = [r for r in RESULTS if not r[1]]
    for name, passed, why in RESULTS:
        print(f"{'ok  ' if passed else 'FAIL'} {name}" + (f" — {why}" if why else ""))
    print(f"\n{len(RESULTS) - len(failed)}/{len(RESULTS)} passed")
    for root in ROOTS:
        shutil.rmtree(root, ignore_errors=True)
    return 0 if not failed else 1


if __name__ == "__main__":
    sys.exit(main())
