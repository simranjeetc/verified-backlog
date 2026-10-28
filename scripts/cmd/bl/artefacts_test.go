package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── R20 at use in bl open, and the class-level guard ─────────────────────────

// R20 at use in `bl open`: the stored brief is re-hashed before it is overwritten, so a brief
// edited on disk leaves a trace instead of being silently replaced by a fresh event.
func TestOpenRefusesEditedStoredBrief(t *testing.T) {
	dir := openItem(t)
	stored := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	// the item goes back in flight: defer it, reopen it, then open it again
	for _, argv := range [][]string{{"defer", "A1", "--reason", "later"}, {"reopen", "A1"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	events, _ := readLedger(dir)
	n := len(events)
	recorded := artefactSHA(t, dir, "brief")

	// the stored brief is edited on disk after it was recorded
	tampered := []byte("B1: tampered. Example: prints tampered.\n")
	if err := os.WriteFile(stored, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R20" {
		t.Fatalf("want a refusal under R20, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, ".backlog/items/A1/brief.md") {
		t.Errorf("the refusal must name the artefact: %s", r.msg)
	}
	if !strings.Contains(r.msg, recorded) || !strings.Contains(r.msg, shaHex(tampered)) {
		t.Errorf("the refusal must name both hashes: %s", r.msg)
	}

	// the call refuses, exits 2, and writes nothing
	if code := run([]string{"--root", dir, "open", "A1"}); code != 2 {
		t.Fatalf("bl open: exit = %d, want 2", code)
	}
	after, _ := os.ReadFile(stored)
	if string(after) != string(tampered) {
		t.Errorf("a refused open rewrote the brief: %q", after)
	}
	events, _ = readLedger(dir)
	if len(events) != n {
		t.Fatalf("a refused open appended %d events", len(events)-n)
	}
	if last := events[len(events)-1]; last.T != "reopen" {
		t.Fatalf("a refused open wrote %+v", last)
	}
}

// R5 at use in `bl open`: the brief is frozen with the claims. `bl open` writes the brief from
// the item text, so opening an item that already has a claims event would replace the brief the
// claims were recorded against. The call refuses and writes nothing — no brief, no event.
func TestOpenRefusedAfterClaims(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	// the item goes back in the queue: defer it, reopen it, then open it again
	for _, argv := range [][]string{{"defer", "A1", "--reason", "later"}, {"reopen", "A1"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	stored := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	frozen, _ := os.ReadFile(stored)
	frozenSHA := artefactSHA(t, dir, "brief")
	before, _ := readLedger(dir)

	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R5" {
		t.Fatalf("want a refusal under R5, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "A1") || !strings.Contains(r.msg, "claims") {
		t.Errorf("the refusal must name the item and its claims: %s", r.msg)
	}
	// the exit code is 2
	if code := run([]string{"--root", dir, "open", "A1"}); code != 2 {
		t.Fatalf("bl open: exit = %d, want 2", code)
	}

	// nothing was written: the frozen brief is untouched and no event was appended
	after, _ := os.ReadFile(stored)
	if string(after) != string(frozen) {
		t.Errorf("a refused open rewrote the brief: %q", after)
	}
	if artefactSHA(t, dir, "brief") != frozenSHA {
		t.Error("a refused open changed the recorded brief hash")
	}
	events, _ := readLedger(dir)
	if len(events) != len(before) {
		t.Fatalf("a refused open appended %d events", len(events)-len(before))
	}
	if last := events[len(events)-1]; last.T != "reopen" {
		t.Fatalf("a refused open wrote %+v", last)
	}
}

// R24 at use: a frozen row whose brief_ref names a clause the current brief no longer holds is a
// refusal — the row's criterion no longer exists in the brief it was written against, because an
// amendment removed the clause. The check runs where the round path reads the frozen artefacts,
// so a round cannot pass on a list whose brief_ref has gone missing.
func TestCheckRefusesRowWhoseBriefRefIsGone(t *testing.T) {
	dir := openItem(t)
	// S1 cites B2; J1 is a judge row with an empty brief_ref, which is fine
	list := checksHeader +
		"S1\tbl\tblocking\tB2\tgo test ./...\texit 0\t\t\ttests pass\n" +
		"J1\tjudge\tadvisory\t\tgit diff\t\tthe diff renames a public verb\tnaming quality is a reading of the diff\tnames stay stable\n"
	checksOK(t, dir, list)

	// nothing is missing: the check passes
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}

	// the brief is amended so that it holds only B1 — the frozen row cites B2, which is gone
	amended := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", amended); err != nil {
		t.Fatalf("brief: %v", err)
	}

	r := wantRefusal(t, call(t, cmdCheck, dir))
	if r.rule != "R24" {
		t.Fatalf("want a refusal under R24, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "S1") {
		t.Errorf("the refusal must name the row: %s", r.msg)
	}
	if !strings.Contains(r.msg, "B2") {
		t.Errorf("the refusal must name the missing clause: %s", r.msg)
	}
	// the exit code is 2
	if code := run([]string{"--root", dir, "check"}); code != 2 {
		t.Fatalf("bl check: exit = %d, want 2", code)
	}

	// the round path refuses too: a round cannot pass on a list whose brief_ref has gone missing
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R24")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "brief" {
		t.Fatalf("a refused round wrote %+v", last)
	}
}

// The rules above refuse only a brief_ref that is present and dangling. A judge row and an
// advisory row may both carry an empty brief_ref, and the check passes.
func TestCheckAcceptsRowsWithEmptyBriefRef(t *testing.T) {
	dir := openItem(t)
	list := checksHeader +
		"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\ttests pass\n" +
		"J1\tjudge\tadvisory\t\tgit diff\t\tthe diff renames a public verb\tnaming quality is a reading of the diff\tnames stay stable\n"
	checksOK(t, dir, list)
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
	// the brief keeps its clauses: nothing dangles
	amended := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", amended); err != nil {
		t.Fatalf("brief: %v", err)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// The verbs that read or write a frozen artefact for an item, and the state each one needs
// before it would touch that artefact.
//
// THIS TABLE IS THE SINGLE PLACE THAT LISTS THOSE VERBS. A verb added later that reads or
// writes a frozen artefact must be added here, or the hole comes back a fourth time: three
// rounds in a row found a verb that touched a frozen artefact without re-hashing it, and no
// test that would have noticed.
//
// Each case names the rule its refusal must carry. R20 is the re-hash: the verb went to touch
// an artefact that no longer matches the ledger. R5 is the ordering rule: the verb tried to
// rewrite a frozen artefact after the claims exist. `bl open` appears twice for that reason —
// once with no claims (R20 — the re-hash) and once with claims (R5 — the brief is frozen with
// the claims, so it must not be rewritten at all).
var frozenVerbs = []struct {
	verb  string
	label string
	rule  string
	setup func(t *testing.T, dir string)
	argv  func(t *testing.T, dir string) []string
}{
	{
		verb:  "open",
		label: "open",
		rule:  "R20",
		setup: func(t *testing.T, dir string) {
			// the item has to be openable again: defer it, reopen it, then open it
			if err := call(t, cmdDefer, dir, "A1", "--reason", "later"); err != nil {
				t.Fatalf("defer: %v", err)
			}
			if err := call(t, cmdReopen, dir, "A1"); err != nil {
				t.Fatalf("reopen: %v", err)
			}
		},
		argv: func(t *testing.T, dir string) []string { return []string{"open", "A1"} },
	},
	{
		verb:  "open",
		label: "open after claims",
		rule:  "R5",
		setup: func(t *testing.T, dir string) {
			checksOK(t, dir, goodList)
			if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
				t.Fatalf("claims: %v", err)
			}
			// the item has to be openable again: defer it, reopen it, then open it
			if err := call(t, cmdDefer, dir, "A1", "--reason", "later"); err != nil {
				t.Fatalf("defer: %v", err)
			}
			if err := call(t, cmdReopen, dir, "A1"); err != nil {
				t.Fatalf("reopen: %v", err)
			}
		},
		argv: func(t *testing.T, dir string) []string { return []string{"open", "A1"} },
	},
	{
		verb:  "brief",
		label: "brief",
		rule:  "R20",
		setup: func(t *testing.T, dir string) {},
		argv: func(t *testing.T, dir string) []string {
			return []string{"brief", "A1", "--file", writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\n")}
		},
	},
	{
		verb:  "checks",
		label: "checks",
		rule:  "R20",
		setup: func(t *testing.T, dir string) {},
		argv: func(t *testing.T, dir string) []string {
			return []string{"checks", "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", goodList)}
		},
	},
	{
		verb:  "claims",
		label: "claims",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv: func(t *testing.T, dir string) []string {
			return []string{"claims", "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")}
		},
	},
	{
		verb:  "run",
		label: "run",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv:  func(t *testing.T, dir string) []string { return []string{"run", "A1"} },
	},
	{
		verb:  "round",
		label: "round",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv: func(t *testing.T, dir string) []string {
			return []string{"round", "A1", "--verdict", "FAIL", "--cause", "code",
				"--rows", writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")}
		},
	},
	{
		verb:  "check",
		label: "check",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv:  func(t *testing.T, dir string) []string { return []string{"check"} },
	},
	{
		verb:  "rebaseline",
		label: "rebaseline",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv: func(t *testing.T, dir string) []string {
			return []string{"rebaseline", "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList),
				"--reason", "the premise moved"}
		},
	},
	{
		verb:  "close",
		label: "close",
		rule:  "R20",
		setup: func(t *testing.T, dir string) { checksOK(t, dir, goodList) },
		argv:  func(t *testing.T, dir string) []string { return []string{"close", "A1", "--commit", "abc1234"} },
	},
}

// Every verb that reads or writes a frozen artefact refuses when it may not touch that artefact,
// and writes nothing. A verb that skips the re-hash fails here, unless it is missing from
// frozenVerbs — which is why that table is the single place that lists them.
func TestEveryVerbTouchingAFrozenArtefactRefusesR20(t *testing.T) {
	for _, v := range frozenVerbs {
		t.Run(v.label, func(t *testing.T) {
			handler, ok := handlers[v.verb]
			if !ok {
				t.Fatalf("no handler for verb %q", v.verb)
			}
			dir := openItem(t)
			v.setup(t, dir)
			stored := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
			recorded := artefactSHA(t, dir, "brief")

			// the stored brief is edited on disk after it was recorded
			tampered := []byte("B1: tampered. Example: prints tampered.\n")
			if err := os.WriteFile(stored, tampered, 0o644); err != nil {
				t.Fatal(err)
			}
			before, _ := readLedger(dir)

			argv := v.argv(t, dir)
			if len(argv) == 0 || argv[0] != v.verb {
				t.Fatalf("the table entry for %q must start with the verb name: %v", v.verb, argv)
			}
			r := wantRefusal(t, call(t, handler, dir, argv[1:]...))
			if r.rule != v.rule {
				t.Fatalf("bl %s: want a refusal under %s, got %s: %s", v.verb, v.rule, r.rule, r.msg)
			}
			if v.rule == "R20" {
				if !strings.Contains(r.msg, ".backlog/items/A1/brief.md") {
					t.Errorf("bl %s: the refusal must name the artefact: %s", v.verb, r.msg)
				}
				if !strings.Contains(r.msg, recorded) || !strings.Contains(r.msg, shaHex(tampered)) {
					t.Errorf("bl %s: the refusal must name both hashes: %s", v.verb, r.msg)
				}
			} else {
				// R5: the refusal must name the item and the claims that froze the brief
				if !strings.Contains(r.msg, "A1") || !strings.Contains(r.msg, "claims") {
					t.Errorf("bl %s: the refusal must name the item and its claims: %s", v.verb, r.msg)
				}
			}

			// the artefact and the ledger are unchanged
			after, _ := os.ReadFile(stored)
			if string(after) != string(tampered) {
				t.Errorf("bl %s: a refused call rewrote the artefact: %q", v.verb, after)
			}
			now, _ := readLedger(dir)
			if len(now) != len(before) {
				t.Fatalf("bl %s: a refused call appended %d events", v.verb, len(now)-len(before))
			}
		})
	}
}
