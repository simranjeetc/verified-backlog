#!/usr/bin/env python3
"""Gate tests for bl — every gate is proven by a refused call, not by reading the code.

Runs against the compiled binary (scripts/bl), or BL=... to point elsewhere.
Run as a script:  python3 scripts/bl.test.py
Exit status: 0 when every test passes, 1 otherwise.
"""
from __future__ import annotations

import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path

HERE = Path(__file__).resolve().parent
BL = os.environ.get("BL", str(HERE / "bl"))
if not Path(BL).exists():
    raise SystemExit(f"no bl binary at {BL} — build it: go build -o scripts/bl ./scripts/bl-go")
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
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok | PASS\n")
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
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    refused(root, "round", "A1", "--verdict", "FAIL", "--rows", "-", stdin="x\n", match="needs --cause")


@test
def round_refuses_pass_with_cause():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    refused(root, "round", "A1", "--verdict", "PASS", "--cause", "code", "--rows", "-", stdin="x\n")


@test
def round_refuses_empty_rows():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    refused(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="   \n", match="after commands have run")


@test
def round_refuses_unknown_verdict():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    refused(root, "round", "A1", "--verdict", "VOID", "--rows", "-", stdin="x\n")


@test
def cap_counts_code_only():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    for _ in range(3):
        ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", "-", stdin="check | FAIL\n")
    refused(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", "-", stdin="x\n", match="cap 3")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", "-", stdin="harness\n")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="all green\n")


@test
def round_refuses_after_pass():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
    refused(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", "-", stdin="x\n", match="already passed")


# ── close ────────────────────────────────────────────────────────────────────


@test
def close_refuses_without_pass():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", "-", stdin="x\n")
    refused(root, "close", "A1", "--commit", head(root), match="no PASS")


@test
def close_refuses_dirty_tree():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
    (root / "BRIEF.md").write_text("changed\n", encoding="utf-8")
    refused(root, "close", "A1", "--commit", head(root), match="dirty")


@test
def close_refuses_unknown_commit():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
    refused(root, "close", "A1", "--commit", "deadbeef", match="does not exist")


@test
def close_ignores_its_own_artifacts():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
    ok(root, "close", "A1", "--commit", head(root))
    assert "- [x] A1" in (root / "TODO.md").read_text()


# ── drop / defer / reopen ────────────────────────────────────────────────────


@test
def close_resolves_a_symbolic_commit():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
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
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok\n")
    ok(root, "close", "A1", "--commit", head(root))
    assert ok(root, "next").stdout.startswith("A2")


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
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="ok | PASS\n")
    ev = next((root / ".backlog" / "evidence").iterdir())
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


# ── audit ────────────────────────────────────────────────────────────────────


@test
def audit_writes_report_and_json():
    root = fresh()
    ok(root, "file", "--id", "A1", "--text", "first")
    ok(root, "open", "A1")
    ok(root, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", "-", stdin="check | FAIL\n")
    ok(root, "round", "A1", "--verdict", "PASS", "--rows", "-", stdin="check | PASS\n")
    ok(root, "audit", "--since", "2d")
    files = sorted(p.name for p in (root / "audit").iterdir())
    assert any(f.endswith(".md") for f in files) and any(f.endswith(".json") for f in files)
    assert str(root / "audit") not in (root / "PROGRESS.md").read_text()


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
    assert schema["version"] == 1 and len(schema["events"]) == 8, schema
    assert schema["round_cap"] == 3 and schema["causes"] == ["code", "harness"]


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
    declared = {"init", "file", "open", "round", "close", "drop", "defer", "reopen", "note",
                "next", "show", "status", "check", "render", "schema", "audit", "import", "lint"}
    for verb in sorted(declared):
        taught = any(n in skill for n in (f"bl {verb}", f"`{verb}", f'"$BL" {verb}'))
        assert taught, f"SKILL.md never teaches bl {verb}"
    # only command-looking text counts: inline code spans and fenced blocks
    spans = re.findall(r"```[^`]*```|`[^`\n]+`", skill)
    used = {v for span in spans for v in re.findall(r"\bbl ([a-z]+)", span)}
    invented = used - declared
    assert not invented, f"SKILL.md uses verbs the binary does not have: {sorted(invented)}"


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
