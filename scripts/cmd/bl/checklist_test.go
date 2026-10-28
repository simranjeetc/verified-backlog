package main

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// ── P1: the precedence is over the whole list, not over one row ───────────────

// The rows that break exactly one rule each, and nothing else. A list that carries two of them
// is refused under the rule that comes first in P1 — not under the rule whose row happens to sit
// higher in the file.
const (
	rowBreaksR1 = "X1\tmodel\tblocking\tB1\tx\texit 0\t\t\tc\n"
	rowBreaksR2 = "X2\tbl\tblocking\tB9\tx\texit 0\t\t\tc\n"
	rowBreaksR3 = "X3\tjudge\tblocking\tB1\tx\t\tit fails\ttbd\tc\n"
	rowBreaksR8 = "X8\tbl\tblocking\tB1\tx\tstdout contains foo\t\t\tc\n"
	// R34: the row is good on every other rule — it is a decider bl row with a brief_ref that
	// exists and an expectation of one of the five forms — and its cmd holds a backslash
	// before a pipe.
	rowBreaksR34 = "X34\tbl\tblocking\tB1\tgrep a\\|b f\texit 0\t\t\tc\n"
)

// p1Order is the order `bl checks` evaluates its rules in: the STATE guards first — R5 (the
// claims freeze the list), then R4 (the list is written once) — and then the list's own rules, in
// the order the list's rules: R1 (the schema), R34 (the command's dialect), R2 (the brief
// clause), R3 (the judge's justification), R8 (the expectation form). The first broken rule ends
// the command.
//
// I9 changed this order, and this table with it. I7 put R1 first, so a list that broke R1 was
// refused under R1 even on a claims-frozen item. Now the state guards come first, so such a
// command is refused under R5 — it was never allowed to write a list, so the list is not read at
// all.
//
// I13 added R34 to the list's own rules, between R1 and R2: a cmd is a script run by /bin/sh -c,
// so a pipe in it is a pipe, and a backslash before one is refused before the list is frozen.
var p1Order = []string{"R5", "R4", "R1", "R34", "R2", "R3", "R8"}

// p1Row is the row that breaks one rule. R5 and R4 are the state rules: they are broken by the
// item's state, not by a row.
func p1Row(rule string) string {
	switch rule {
	case "R1":
		return rowBreaksR1
	case "R2":
		return rowBreaksR2
	case "R3":
		return rowBreaksR3
	case "R8":
		return rowBreaksR8
	case "R34":
		return rowBreaksR34
	}
	return ""
}

// The precedence: where two rules can both match, the one that comes first in the order wins —
// over the whole list, and whatever order the rows sit in. The state rules (R5, R4) are broken by
// the item's state, so they are set up here and not by a row.
func TestChecksPrecedenceNamesTheEarliestBrokenRule(t *testing.T) {
	for i, earlier := range p1Order {
		for _, later := range p1Order[i+1:] {
			t.Run(earlier+" before "+later, func(t *testing.T) {
				dir := openItem(t)
				// The state rules are broken by the item's state. A list-rule pair is judged on a
				// format-1 list (an amendment keeps format 1), because R8 and R34 still apply there
				// and no new list can be frozen as format 1 (I89, B9, B10).
				amend := earlier != "R5" && later != "R5" && earlier != "R4" && later != "R4"
				switch {
				case earlier == "R5" || later == "R5":
					// the claims are recorded after a check list, so the item has one too
					checksOK(t, dir, goodList)
					claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
					if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
						t.Fatalf("claims: %v", err)
					}
				case earlier == "R4" || later == "R4":
					checksOK(t, dir, goodList)
				default:
					legacyChecks(t, dir, checksHeader+goodRows)
				}
				// the later rule's row sits higher in the file, so the file order cannot be
				// what decides
				rows := ""
				if row := p1Row(later); row != "" {
					rows += row
				}
				if row := p1Row(earlier); row != "" {
					rows += row
				}
				if rows == "" {
					rows = goodRows
				}
				list := checksHeader + rows
				if amend {
					// an amendment only grows the frozen list, so the frozen rows stay
					list = checksHeader + goodRows + rows
				}
				argv := []string{"A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)}
				if amend {
					argv = append(argv, "--amend")
				}
				r := wantRefusal(t, call(t, cmdChecks, dir, argv...))
				if r.rule != earlier {
					t.Fatalf("want a refusal under %s, got %s: %s", earlier, r.rule, r.msg)
				}
			})
		}
	}
}

// Within one rule the refusal names the first broken row in file order, and that row only.
func TestChecksNamesTheFirstBrokenRowOfOneRule(t *testing.T) {
	cases := []struct {
		rule, first, second string
	}{
		{"R1", "Z1\tbl\turgent\tB1\tx\texit 0\t\t\tc\n", "Z2\tmodel\tblocking\tB1\tx\texit 0\t\t\tc\n"},
		{"R2", "Y1\tbl\tblocking\tB8\tx\texit 0\t\t\tc\n", "Y2\tbl\tblocking\tB9\tx\texit 0\t\t\tc\n"},
		{"R3", "W1\tjudge\tblocking\tB1\tx\t\tit fails\ttbd\tc\n", "W2\tjudge\tblocking\tB1\tx\t\tit fails\tnope\tc\n"},
		{"R8", "V1\tbl\tblocking\tB1\tx\tstdout contains foo\t\t\tc\n", "V2\tbl\tblocking\tB1\tx\treturns 0\t\t\tc\n"},
		{"R34", "Z1\tbl\tblocking\tB1\tgrep a\\|b\texit 0\t\t\tc\n", "Z2\tbl\tblocking\tB1\tgrep c\\|d\texit 0\t\t\tc\n"},
	}
	for _, c := range cases {
		t.Run(c.rule, func(t *testing.T) {
			dir := openItem(t)
			list := checksHeader + c.first + c.second
			argv := []string{"A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)}
			if c.rule == "R8" || c.rule == "R34" {
				// R8 and R34 are format-1 rules (I89, B10): an amendment of a format-1 list
				// keeps format 1 and the frozen rows stay in front.
				legacyChecks(t, dir, checksHeader+goodRows)
				list = checksHeader + goodRows + c.first + c.second
				argv = []string{"A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--amend"}
			}
			r := wantRefusal(t, call(t, cmdChecks, dir, argv...))
			if r.rule != c.rule {
				t.Fatalf("want a refusal under %s, got %s: %s", c.rule, r.rule, r.msg)
			}
			first := strings.SplitN(c.first, "\t", 2)[0]
			second := strings.SplitN(c.second, "\t", 2)[0]
			if !strings.Contains(r.msg, first) {
				t.Errorf("the refusal must name the first broken row %s: %s", first, r.msg)
			}
			if strings.Contains(r.msg, second) {
				t.Errorf("the refusal must not name the later broken row %s: %s", second, r.msg)
			}
		})
	}
}

// Each rule on its own still refuses under its own name: the order only decides between two
// rules, it does not change what a rule says.
func TestChecksEachRuleStillRefusesAlone(t *testing.T) {
	for _, rule := range []string{"R1", "R34", "R2", "R3", "R8"} {
		t.Run(rule, func(t *testing.T) {
			dir := openItem(t)
			list := checksHeader + p1Row(rule)
			argv := []string{"A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)}
			if rule == "R8" || rule == "R34" {
				legacyChecks(t, dir, checksHeader+goodRows)
				list = checksHeader + goodRows + p1Row(rule)
				argv = []string{"A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--amend"}
			}
			wantRule(t, call(t, cmdChecks, dir, argv...), rule)
		})
	}
	// the state rules refuse on the item's state
	t.Run("R5", func(t *testing.T) {
		dir := openItem(t)
		checksOK(t, dir, goodList)
		claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
		if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
			t.Fatalf("claims: %v", err)
		}
		checksRefused(t, dir, goodList, "R5")
	})
	t.Run("R4", func(t *testing.T) {
		dir := openItem(t)
		checksOK(t, dir, goodList)
		checksRefused(t, dir, goodList, "R4")
	})
}

// A well-formed list is still accepted, and stored.
func TestChecksAWellFormedListIsStillAccepted(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if err != nil || string(data) != goodList {
		t.Fatalf("checks.tsv = %q, %v", data, err)
	}
}

// ── R24: the removed-clause refusal at use ───────────────────────────────────

// r24List freezes two blocking rows: S1 cites B1, S2 cites B2. Their commands pass their
// expectations, so the binary computes PASS for both (R7) and a round over the list is a PASS
// round.
const r24List = checksHeader +
	"S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\ttests pass\n" +
	"S2\tbl\tblocking\tB2\ttrue\texit 0\t\t\tother tests pass\n"

// r24BriefWithoutB2 is the brief after an amendment that removed B2.
const r24BriefWithoutB2 = "B1: add a verb. Example: adds a verb.\n"

// r24Setup is the state a round is written against: the list is frozen, an amendment removed the
// clause S2 cites, and the claims are recorded.
func r24Setup(t *testing.T) string {
	t.Helper()
	dir := openItem(t)
	checksOK(t, dir, r24List)
	if err := call(t, cmdBrief, dir, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", r24BriefWithoutB2)); err != nil {
		t.Fatalf("brief: %v", err)
	}
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return dir
}

// The use paths — bl round, bl check and bl close — refuse under R24 when an amendment removed a
// clause a frozen row cites, and the refusal names the row and the clause.
func TestR24TheUsePathsRefuseUnderR24(t *testing.T) {
	cases := []struct {
		name    string
		handler func(string, args) error
		argv    func(t *testing.T, dir string) []string
	}{
		{
			name:    "round",
			handler: cmdRound,
			argv: func(t *testing.T, dir string) []string {
				return []string{"A1", "--verdict", "PASS",
					"--rows", writeFile(t, t.TempDir(), "rows.txt", "S1\tPASS\n")}
			},
		},
		{
			name:    "check",
			handler: cmdCheck,
			argv:    func(t *testing.T, dir string) []string { return nil },
		},
		{
			name:    "close",
			handler: cmdClose,
			argv:    func(t *testing.T, dir string) []string { return []string{"A1", "--commit", "abc1234"} },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := r24Setup(t)
			r := wantRefusal(t, call(t, c.handler, dir, c.argv(t, dir)...))
			if r.rule != "R24" {
				t.Fatalf("want a refusal under R24, got %s: %s", r.rule, r.msg)
			}
			if !strings.Contains(r.msg, "S2") {
				t.Errorf("the refusal must name the row: %s", r.msg)
			}
			if !strings.Contains(r.msg, "B2") {
				t.Errorf("the refusal must name the missing clause: %s", r.msg)
			}
		})
	}
}

// The write path keeps R2: a list written with a clause that does not exist is refused under R2,
// not R24, and the list is not stored.
func TestR24TheWritePathStillRefusesUnderR2(t *testing.T) {
	dir := openItem(t)
	if err := call(t, cmdBrief, dir, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", r24BriefWithoutB2)); err != nil {
		t.Fatalf("brief: %v", err)
	}
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", r24List)))
	if r.rule != "R2" {
		t.Fatalf("want a refusal under R2, got %s: %s", r.rule, r.msg)
	}
	// the list is not stored, and nothing was written
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "brief" {
		t.Fatalf("a refused list wrote %+v", last)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); !os.IsNotExist(err) {
		t.Errorf("a refused list was stored: %v", err)
	}
}

// A refused round writes nothing: no event in the ledger, no evidence file, and the item's state
// is unchanged.
func TestR24ARefusedRoundWritesNothing(t *testing.T) {
	dir := r24Setup(t)
	before, _ := readLedger(dir)
	files := evidenceFiles(t, dir)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tPASS\n")
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows), "R24")

	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round appended %d events", len(after)-len(before))
	}
	if got := evidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused round wrote an evidence file: %v", got)
	}
	_, byID, err := derive(after)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	it := byID["a1"]
	if len(it.Rounds) != 0 {
		t.Errorf("a refused round recorded %d rounds", len(it.Rounds))
	}
	if it.Status != "open" {
		t.Errorf("the item is %s, want open", it.Status)
	}
}

// A round whose rows still cite clauses that exist is unaffected by whatever else the amendment
// changed.
func TestR24ARoundIsUnaffectedByAnUnrelatedAmendment(t *testing.T) {
	cases := map[string][]string{
		// the amendment adds a clause and leaves the cited ones alone
		"an amendment that adds a clause": {"B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\nB3: and a third thing. Example: shows the third result.\n"},
		// a clause removed and then restored by a later amendment exists in the latest brief
		"a clause removed and then restored": {"B1: add a verb. Example: adds a verb.\n", "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\n"},
	}
	for name, amendments := range cases {
		t.Run(name, func(t *testing.T) {
			dir := openItem(t)
			checksOK(t, dir, r24List)
			for _, text := range amendments {
				if err := call(t, cmdBrief, dir, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", text)); err != nil {
					t.Fatalf("brief: %v", err)
				}
			}
			if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
				t.Fatalf("claims: %v", err)
			}
			rows := runAndVerdicts(t, dir, "S1\tPASS\t", "S2\tPASS\t")
			if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
				t.Fatalf("round: %v", err)
			}
			events, _ := readLedger(dir)
			if last := events[len(events)-1]; last.T != "round" || last.Verdict != "PASS" {
				t.Fatalf("the round event = %+v", last)
			}
		})
	}
}

// The rule id in the message is R24 in the use paths and R2 in the write path.
func TestR24TheRuleIdInTheMessageIsR24AtUseAndR2AtWrite(t *testing.T) {
	dir := r24Setup(t)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tPASS\n")
	out := captureStderr(t, func() {
		if code := run([]string{"--root", dir, "round", "A1", "--verdict", "PASS", "--rows", rows}); code != 2 {
			t.Errorf("bl round: exit = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "refused (R24)") {
		t.Errorf("the use path must refuse under R24: %s", out)
	}
	if strings.Contains(out, "refused (R2)") {
		t.Errorf("the use path must not refuse under R2: %s", out)
	}

	// the write path: a list written with a clause that does not exist is still R2
	w := openItem(t)
	if err := call(t, cmdBrief, w, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", r24BriefWithoutB2)); err != nil {
		t.Fatalf("brief: %v", err)
	}
	out = captureStderr(t, func() {
		if code := run([]string{"--root", w, "checks", "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", r24List)}); code != 2 {
			t.Errorf("bl checks: exit = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "refused (R2)") {
		t.Errorf("the write path must refuse under R2: %s", out)
	}
	if strings.Contains(out, "R24") {
		t.Errorf("the write path must not refuse under R24: %s", out)
	}
}

// ── I9: the precedence the design does not cover ──────────────────────────────

// `bl checks` does its STATE checks first — R5 (the claims freeze the list), then R4 (the list is
// written once) — so a frozen item is refused without waiting on stdin, and a list that was never
// allowed to be written is not read at all.

// The claims freeze the list: the command is refused under R5, and the list is not read. The file
// does not exist, so a command that read it first would say so instead.
func TestChecksRefusesUnderR5WithoutReadingTheList(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", filepath.Join(t.TempDir(), "no-such-list.tsv")))
	if r.rule != "R5" {
		t.Fatalf("want a refusal under R5, got %s: %s", r.rule, r.msg)
	}
	// nothing was written
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "claims" {
		t.Fatalf("a refusal wrote %+v", last)
	}
}

// The refusal comes back at once: stdin is a fifo whose writer never delivers a byte, so a
// command that read the list would wait for it.
func TestChecksRefusesUnderR5WithoutWaitingOnStdin(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}

	fifo := filepath.Join(t.TempDir(), "list.fifo")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	go func() {
		w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
		if err != nil {
			return
		}
		<-release
		w.Close()
	}()
	// the reader's open waits for the writer; it must not hang the test
	opened := make(chan *os.File, 1)
	go func() {
		f, err := os.OpenFile(fifo, os.O_RDONLY, 0)
		if err != nil {
			opened <- nil
			return
		}
		opened <- f
	}()
	var stdin *os.File
	select {
	case stdin = <-opened:
		if stdin == nil {
			t.Fatal("the fifo could not be opened")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the fifo could not be opened")
	}
	defer stdin.Close()
	defer close(release)

	old := os.Stdin
	os.Stdin = stdin
	defer func() { os.Stdin = old }()

	a, err := parseArgs([]string{"A1"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmdChecks(dir, a) }()
	select {
	case err := <-done:
		r := wantRefusal(t, err)
		if r.rule != "R5" {
			t.Fatalf("want a refusal under R5, got %s: %s", r.rule, r.msg)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("bl checks waited on stdin: the state guards come before the list is read")
	}
}

// The list is written once: with a frozen list and no --amend the command is refused under R4,
// and the list is not read.
func TestChecksRefusesUnderR4WithoutReadingTheList(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", filepath.Join(t.TempDir(), "no-such-list.tsv")))
	if r.rule != "R4" {
		t.Fatalf("want a refusal under R4, got %s: %s", r.rule, r.msg)
	}
	// nothing was written
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "checks" {
		t.Fatalf("a refusal wrote %+v", last)
	}
}

// With no claims and no list there is nothing to freeze: the command is refused, and it records
// nothing.
func TestChecksWithNoListIsRefusedAndRecordsNothing(t *testing.T) {
	dir := openItem(t)
	// stdin is empty: there is no list to read
	empty, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer empty.Close()
	old := os.Stdin
	os.Stdin = empty
	defer func() { os.Stdin = old }()

	r := wantRefusal(t, call(t, cmdChecks, dir, "A1"))
	if r.rule != "R1" {
		t.Fatalf("want a refusal under R1, got %s: %s", r.rule, r.msg)
	}
	// nothing was written, and no list was stored
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "open" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); !os.IsNotExist(err) {
		t.Fatal("a refused list must not be stored")
	}
}

// A well-formed list is accepted by `bl checks`, and `bl claims` is accepted afterwards.
func TestChecksAWellFormedListIsAcceptedAndTheClaimsAfterIt(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "claims" {
		t.Fatalf("the claims event = %+v", last)
	}
}

// `bl rebaseline` evaluates the LIST's rules before the state rules: a list that breaks R1, R2 or
// R8 is refused under that rule, even when the claims freeze the item.
func TestRebaselineRefusesABadListUnderItsOwnRuleWithClaims(t *testing.T) {
	dir := openItem(t)
	// a format-1 list, so the R8 case below stays R8's (a new list is format 2, I89, B9)
	legacyChecks(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	cases := []struct {
		name, list, rule string
	}{
		{"R1", checksHeader + "S1\tmodel\tblocking\tB1\tx\texit 0\t\t\tc\n", "R1"},
		{"R2", checksHeader + "S1\tbl\tblocking\tB9\tx\texit 0\t\t\tc\n", "R2"},
		{"R8", checksHeader + "S1\tbl\tblocking\tB1\tx\texit 999\t\t\tc\n", "R8"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := wantRefusal(t, call(t, cmdRebaseline, dir, "A1",
				"--file", writeFile(t, t.TempDir(), "c.tsv", c.list), "--reason", "the premise moved"))
			if r.rule != c.rule {
				t.Fatalf("want a refusal under %s, got %s: %s", c.rule, r.rule, r.msg)
			}
			// the frozen list is untouched, and nothing was written
			data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
			if string(data) != goodList {
				t.Fatalf("a refused re-baseline changed the list: %q", data)
			}
			events, _ := readLedger(dir)
			if last := events[len(events)-1]; last.T != "claims" {
				t.Fatalf("a refusal wrote %+v", last)
			}
		})
	}
}

// A list that is good on its own reaches the state rules: with claims the re-baseline is refused
// under R5 (the claims freeze the list).
func TestRebaselineRefusesAValidListUnderR5WithClaims(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	r := wantRefusal(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "the premise moved"))
	if r.rule != "R5" {
		t.Fatalf("want a refusal under R5, got %s: %s", r.rule, r.msg)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != goodList {
		t.Fatalf("a refused re-baseline changed the list: %q", data)
	}
}

// The same list without claims is accepted, and the re-baseline is recorded.
func TestRebaselineAcceptsAValidListWithoutClaims(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	if err := call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "the premise moved"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if err != nil || string(data) != rebaselinedList {
		t.Fatalf("checks.tsv = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "rebaseline" {
		t.Fatalf("the re-baseline event = %+v", last)
	}
}

// The list's own rules keep their order: a list that breaks R1 and R8 is refused under R1, one
// that breaks R2 and R3 under R2, and within one rule the first broken row in file order is the
// one named.
func TestTheListRulesKeepTheirOrder(t *testing.T) {
	cases := []struct {
		name, list, rule, names, notNames string
	}{
		{
			"R1 before R8",
			checksHeader + rowBreaksR8 + rowBreaksR1,
			"R1", "X1", "X8",
		},
		{
			"R2 before R3",
			checksHeader + rowBreaksR3 + rowBreaksR2,
			"R2", "X2", "X3",
		},
		{
			"the first broken row of one rule",
			checksHeader + "Y1\tbl\tblocking\tB8\tx\texit 0\t\t\tc\n" + "Y2\tbl\tblocking\tB9\tx\texit 0\t\t\tc\n",
			"R2", "Y1", "Y2",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := openItem(t)
			r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", c.list)))
			if r.rule != c.rule {
				t.Fatalf("want a refusal under %s, got %s: %s", c.rule, r.rule, r.msg)
			}
			if !strings.Contains(r.msg, c.names) {
				t.Errorf("the refusal must name the row %s: %s", c.names, r.msg)
			}
			if strings.Contains(r.msg, c.notNames) {
				t.Errorf("the refusal must not name the later row %s: %s", c.notNames, r.msg)
			}
		})
	}
}

// ── R23: a harness round carries no rows ──────────────────────────────────────

// R23: a harness round carries no rows because the verifier did not run, so a rows file with
// content is refused under R23 — and the refusal writes nothing: no round event, no evidence
// file, the ledger and the item unchanged.
func TestR23AHarnessRoundWithRowsIsRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	before, _ := readLedger(dir)
	files := evidenceFiles(t, dir)

	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "no rows") || !strings.Contains(r.msg, "verifier did not run") {
		t.Errorf("the refusal must say a harness round carries no rows because the verifier did not run: %s", r.msg)
	}

	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused harness round appended %d events", len(after)-len(before))
	}
	if got := evidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused harness round wrote an evidence file: %v", got)
	}
	_, byID, err := derive(after)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	it := byID["a1"]
	if len(it.Rounds) != 0 {
		t.Errorf("a refused harness round recorded %d rounds", len(it.Rounds))
	}
	if it.Status != "open" {
		t.Errorf("the item is %s, want open", it.Status)
	}
	// the call exits 2 and the message names the rule
	out := captureStderr(t, func() {
		if code := run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows}); code != 2 {
			t.Errorf("bl round: exit = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "refused (R23)") {
		t.Errorf("the refusal must name R23: %s", out)
	}
}

// R23: a harness round with no rows file, and a harness round given an EMPTY rows file, are both
// accepted, and both are recorded with no rows and no rows_sha.
func TestR23AHarnessRoundWithNoRowsOrAnEmptyRowsFileIsAccepted(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round without rows: %v", err)
	}
	empty := writeFile(t, t.TempDir(), "empty.txt", "")
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", empty); err != nil {
		t.Fatalf("harness round with an empty rows file: %v", err)
	}
	events, _ := readLedger(dir)
	var rounds []Event
	for _, ev := range events {
		if ev.T == "round" {
			rounds = append(rounds, ev)
		}
	}
	if len(rounds) != 2 {
		t.Fatalf("want 2 rounds recorded, got %d", len(rounds))
	}
	for _, ev := range rounds {
		if ev.Verdict != "FAIL" || ev.Cause != "harness" {
			t.Fatalf("round event = %+v", ev)
		}
		if ev.Rows != "" || ev.RowsSHA != "" {
			t.Errorf("a harness round carries no rows: %+v", ev)
		}
	}
	if files := evidenceFiles(t, dir); len(files) != 0 {
		t.Errorf("a harness round wrote an evidence file: %v", files)
	}
}

// R23: absent means the flag was not given, or the path does not exist. A harness round whose
// --rows path does not exist is accepted and recorded with no rows, and the status shows it as
// one harness round.
func TestR23AHarnessRoundWithARowsPathThatDoesNotExistIsAccepted(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	missing := filepath.Join(t.TempDir(), "nope.tsv")
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", missing); err != nil {
		t.Fatalf("a harness round whose rows path does not exist: %v", err)
	}
	events, _ := readLedger(dir)
	var rounds []Event
	for _, ev := range events {
		if ev.T == "round" {
			rounds = append(rounds, ev)
		}
	}
	if len(rounds) != 1 {
		t.Fatalf("want 1 round recorded, got %d", len(rounds))
	}
	if rounds[0].Verdict != "FAIL" || rounds[0].Cause != "harness" {
		t.Fatalf("round event = %+v", rounds[0])
	}
	if rounds[0].Rows != "" || rounds[0].RowsSHA != "" {
		t.Errorf("a harness round carries no rows: %+v", rounds[0])
	}
	if files := evidenceFiles(t, dir); len(files) != 0 {
		t.Errorf("a harness round wrote an evidence file: %v", files)
	}
	// the status shows the round as one harness round
	out := capture(t, func() {
		if err := call(t, cmdStatus, dir); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(out, "harness 1") {
		t.Errorf("the status must show one harness round: %s", out)
	}
}

// R23: a harness round whose --rows file is empty is accepted and recorded with no rows.
func TestR23AHarnessRoundWithAnEmptyRowsFileIsAcceptedAndRecorded(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	empty := writeFile(t, t.TempDir(), "empty.tsv", "")
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", empty); err != nil {
		t.Fatalf("a harness round with an empty rows file: %v", err)
	}
	events, _ := readLedger(dir)
	var rounds []Event
	for _, ev := range events {
		if ev.T == "round" {
			rounds = append(rounds, ev)
		}
	}
	if len(rounds) != 1 {
		t.Fatalf("want 1 round recorded, got %d", len(rounds))
	}
	if rounds[0].Verdict != "FAIL" || rounds[0].Cause != "harness" {
		t.Fatalf("round event = %+v", rounds[0])
	}
	if rounds[0].Rows != "" || rounds[0].RowsSHA != "" {
		t.Errorf("a harness round carries no rows: %+v", rounds[0])
	}
	if files := evidenceFiles(t, dir); len(files) != 0 {
		t.Errorf("a harness round wrote an evidence file: %v", files)
	}
}

// R23: a harness round whose --rows file holds rows is refused under R23, as it always was — the
// verifier did not run, so the round carries no rows.
func TestR23AHarnessRoundWithARowsFileHoldingRowsIsRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.tsv", "S1\tFAIL\n")
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "no rows") {
		t.Errorf("the refusal must say a harness round carries no rows: %s", r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused harness round appended %d events", len(after)-len(before))
	}
}

// R23: a path that exists and cannot be read as a file is refused, not treated as empty. A
// harness round whose --rows path is a DIRECTORY is refused under R23 — a caller who meant to
// hand over rows and handed over something else has made a mistake — and the refusal writes
// nothing: the ledger is unchanged, no evidence file, no round.
func TestR23AHarnessRoundWithADirectoryAsRowsIsRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := t.TempDir() // a directory, not a file
	ledger := filepath.Join(dir, ".backlog", "ledger.jsonl")
	before, _ := readLedger(dir)
	beforeBytes, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	files := evidenceFiles(t, dir)

	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "directory") {
		t.Errorf("the refusal must say the rows path is a directory: %s", r.msg)
	}

	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused harness round appended %d events", len(after)-len(before))
	}
	afterBytes, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	if string(afterBytes) != string(beforeBytes) {
		t.Errorf("a refused harness round changed the ledger:\n%s", afterBytes)
	}
	if got := evidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused harness round wrote an evidence file: %v", got)
	}
	_, byID, err := derive(after)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	it := byID["a1"]
	if len(it.Rounds) != 0 {
		t.Errorf("a refused harness round recorded %d rounds", len(it.Rounds))
	}
	if it.Status != "open" {
		t.Errorf("the item is %s, want open", it.Status)
	}
	// the call exits 2 and the message names the rule
	out := captureStderr(t, func() {
		if code := run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows}); code != 2 {
			t.Errorf("bl round: exit = %d, want 2", code)
		}
	})
	if !strings.Contains(out, "refused (R23)") {
		t.Errorf("the refusal must name R23: %s", out)
	}
}

// R23: a file that cannot be opened is refused like a directory is: the path exists, so it is not
// absent, and a file that cannot be read as a file cannot be empty either.
func TestR23AHarnessRoundWithAFileThatCannotBeOpenedIsRefused(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root opens every file")
	}
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.tsv", "")
	if err := os.Chmod(rows, 0o000); err != nil {
		t.Fatal(err)
	}
	before, _ := readLedger(dir)

	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}

	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused harness round appended %d events", len(after)-len(before))
	}
}

// R23: a code round is a verdict about rows, so a rows file with content is still accepted, and
// the round is recorded with the rows and their hash.
func TestR23ACodeRoundWithRowsIsStillAccepted(t *testing.T) {
	dir := roundItem(t, roundFailList)
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("code round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "code" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Rows == "" || ev.RowsSHA == "" {
		t.Fatalf("a code round carries its rows: %+v", ev)
	}
	if files := roundEvidenceFiles(t, dir); len(files) != 1 {
		t.Fatalf("want one evidence file, got %v", files)
	}
}

// P2: R20 comes before R23, so a harness round with a rows file on an item whose frozen artefacts
// were tampered with is refused under R20, not R23 — and it writes nothing either.
func TestR20OutranksR23OnTheHarnessPath(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	checks := filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")
	if err := os.WriteFile(checks, []byte(goodList+"# tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	before, _ := readLedger(dir)
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness", "--rows", rows), "R20")
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round appended %d events", len(after)-len(before))
	}
}

// ── R34: a command is a shell script, and a pattern is not a shell ────────────

// r34BadRow is a row whose cmd holds a backslash before a pipe: the shell reads that as a
// literal pipe, so the row does not do what it says. It is a judge row because R34 reaches
// every judge row in either format, and a format-2 `decider: bl` row is governed by R37's
// simple-command rule instead (I89, B10).
const r34BadRow = "X34\tjudge\tblocking\tB1\tgrep a\\|b f\t\tit fails\ta shell script is read as a shell script\tc\n"

// r34GoodRow is a row whose cmd holds a plain pipe: the shell reads that as a pipe, which is
// what the row means.
const r34GoodRow = "S1\tjudge\tblocking\tB1\techo ab | grep a\t\tit fails\ta shell script is read as a shell script\tc\n"

// `bl checks` refuses a list whose row holds a backslash before a pipe: the refusal names R34
// and the offending row, and nothing is written.
func TestChecksR34RefusesABackslashBeforeAPipe(t *testing.T) {
	dir := openItem(t)
	list := checksHeader + r34GoodRow + r34BadRow
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
	if r.rule != "R34" {
		t.Fatalf("want a refusal under R34, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "X34") {
		t.Errorf("the refusal must name the offending row: %s", r.msg)
	}
	if strings.Contains(r.msg, "S1") {
		t.Errorf("the refusal must not name the good row: %s", r.msg)
	}
	// nothing is written: no list event and no list file
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "open" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); err == nil {
		t.Fatal("a refused list must not be stored")
	}
	// and a good list can still be frozen for the item
	checksOK(t, dir, checksHeader+r34GoodRow)
}

// `bl rebaseline` refuses the same way: a new list is a frozen list too.
func TestRebaselineR34RefusesABackslashBeforeAPipe(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	r := wantRefusal(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", checksHeader+r34BadRow), "--reason", "the premise moved"))
	if r.rule != "R34" {
		t.Fatalf("want a refusal under R34, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "X34") {
		t.Errorf("the refusal must name the offending row: %s", r.msg)
	}
	// the frozen list is untouched and nothing was written
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != goodList {
		t.Fatalf("a refused re-baseline changed the list: %q", data)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "checks" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	// and a good list can still be re-baselined
	if err := call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c2.tsv", checksHeader+r34GoodRow), "--reason", "the premise moved"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
}

// A list whose commands use plain pipes is still accepted: the shell reads a plain pipe as a
// pipe, and that is what the row means.
func TestChecksR34AcceptsPlainPipes(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, checksHeader+
		"S1\tjudge\tblocking\tB1\techo ab | grep a\t\tit fails\ta shell script is read as a shell script\tc\n"+
		"S2\tjudge\tblocking\tB1\techo ab | grep -E 'a|b' | wc -l\t\tit fails\ta shell script is read as a shell script\tc\n"+
		"S3\tjudge\tblocking\tB1\ttrue || false\t\tit fails\ta shell script is read as a shell script\tc\n")
}

// A list with no pipe at all is still accepted.
func TestChecksR34AcceptsAListWithNoPipe(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, checksHeader+"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\tc\n")
}

// R34 is about the cmd field only: an expect pattern is Go's RE2, where a backslash before a
// pipe means a literal pipe, and that is what the pattern says. Only a format-1 list carries
// an expect like that (I89, B10), so the list is re-baselined onto a format-1 item.
func TestChecksR34DoesNotRefuseAnEscapedPipeInAnExpect(t *testing.T) {
	dir := openItem(t)
	legacyChecks(t, dir, rebaselinedList)
	list := checksHeader + "S1\tbl\tblocking\tB1\techo \"a|b\"\tstdout matches a\\|b\t\t\tc\n"
	if err := call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "the premise moved"); err != nil {
		t.Fatalf("R34 must not read the expect field: %v", err)
	}
}

// Within R34 the refusal names the first broken row in file order, and that row only.
func TestR34NamesTheFirstBrokenRowOnly(t *testing.T) {
	dir := openItem(t)
	list := checksHeader +
		"X1\tjudge\tblocking\tB1\tgrep a\\|b f\t\tit fails\ta shell script is read as a shell script\tc\n" +
		"X2\tjudge\tblocking\tB1\tgrep c\\|d f\t\tit fails\ta shell script is read as a shell script\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
	if r.rule != "R34" {
		t.Fatalf("want a refusal under R34, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "X1") {
		t.Errorf("the refusal must name the first broken row: %s", r.msg)
	}
	if strings.Contains(r.msg, "X2") {
		t.Errorf("the refusal must not name the later broken row: %s", r.msg)
	}
}

// The count of the backslashes decides, not the pair. A pipe is ESCAPED when an ODD number of
// backslashes precedes it: the shell reads it as a literal pipe, so the row does not do what it
// says. An EVEN number is an escaped backslash followed by a real pipe, which the shell runs as
// a pipe, and that row is fine.
func TestChecksR34CountsTheBackslashesBeforeAPipe(t *testing.T) {
	cases := []struct {
		name    string
		row     string
		refused bool
	}{
		{"no backslash", "PIPE0\tjudge\tblocking\tB1\techo a | cat\t\tit fails\ta shell script is read as a shell script\tc\n", false},
		{"one backslash", "PIPE1\tjudge\tblocking\tB1\techo a \\| cat\t\tit fails\ta shell script is read as a shell script\tc\n", true},
		{"two backslashes", "PIPE2\tjudge\tblocking\tB1\techo a \\\\| cat\t\tit fails\ta shell script is read as a shell script\tc\n", false},
		{"three backslashes", "PIPE3\tjudge\tblocking\tB1\techo a \\\\\\| cat\t\tit fails\ta shell script is read as a shell script\tc\n", true},
		{"four backslashes", "PIPE4\tjudge\tblocking\tB1\techo a \\\\\\\\| cat\t\tit fails\ta shell script is read as a shell script\tc\n", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := openItem(t)
			list := checksHeader + c.row
			if !c.refused {
				// the row is fine: the list is frozen
				checksOK(t, dir, list)
				return
			}
			r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
			if r.rule != "R34" {
				t.Fatalf("want a refusal under R34, got %s: %s", r.rule, r.msg)
			}
			id := strings.SplitN(c.row, "\t", 2)[0]
			if !strings.Contains(r.msg, id) {
				t.Errorf("the refusal must name the offending row %s: %s", id, r.msg)
			}
			// nothing is written
			events, _ := readLedger(dir)
			if last := events[len(events)-1]; last.T != "open" {
				t.Fatalf("a refusal wrote %+v", last)
			}
			if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); err == nil {
				t.Fatal("a refused list must not be stored")
			}
		})
	}
}

// `bl rebaseline` freezes a list too, so it counts the backslashes the same way: an even number
// of backslashes before a pipe is a real pipe, and the re-baseline is accepted.
func TestRebaselineR34AcceptsAnEvenNumberOfBackslashesBeforeAPipe(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	list := checksHeader + "PIPE2\tjudge\tblocking\tB1\techo a \\\\| cat\t\tit fails\ta shell script is read as a shell script\tc\n"
	if err := call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "the premise moved"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != list {
		t.Fatalf("the re-baseline stored %q, want %q", data, list)
	}
}

// The check reads the command the way the shell reads it: a pipe inside quotes is text the
// pattern carries, so a backslash before it is the pattern's, not the shell's.
func TestChecksR34SkipsPipesInsideQuotes(t *testing.T) {
	cases := []struct {
		name    string
		cmd     string
		refused bool
	}{
		{"single quotes", "grep -Eq 'a\\|b' f", false},
		{"double quotes", "grep -Eq \"a\\|b\" f", false},
		{"escaped pipe outside quotes", "grep -Eq a\\|b f", true},
		{"two backslashes outside quotes", "echo a\\\\|cat", false},
		{"escaped pipe after a closed quote", "grep -Eq 'a' \\| b", true},
		{"backslash inside quotes then a real pipe", "echo 'a\\'|cat", false},
		{"escaped quote does not open a string", "echo \\'a\\|b", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := openItem(t)
			list := checksHeader + "Q1\tjudge\tblocking\tB1\t" + c.cmd + "\t\tit fails\ta shell script is read as a shell script\tc\n"
			if !c.refused {
				checksOK(t, dir, list)
				return
			}
			r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
			if r.rule != "R34" {
				t.Fatalf("want a refusal under R34, got %s: %s", r.rule, r.msg)
			}
			if !strings.Contains(r.msg, "Q1") {
				t.Errorf("the refusal must name the offending row: %s", r.msg)
			}
		})
	}
}
