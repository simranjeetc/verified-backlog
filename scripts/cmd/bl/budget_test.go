package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── R19: the round budget ────────────────────────────────────────────────────

// evidenceFiles lists the evidence files a ledger holds, sorted by name.
func evidenceFiles(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dir, ".backlog", "evidence"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// roundEvidenceFiles lists the evidence files the rounds wrote — the `-r<N>.txt` ones, not the
// run files, which live in the same directory.
func roundEvidenceFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	for _, name := range evidenceFiles(t, dir) {
		if strings.HasPrefix(name, "A1-r") && strings.HasSuffix(name, ".txt") {
			out = append(out, name)
		}
	}
	return out
}

// wantBudgetRefusal fails unless err is a refusal under R19 that says the item escalates to a
// human, and returns it.
func wantBudgetRefusal(t *testing.T, err error) Refusal {
	t.Helper()
	r := wantRefusal(t, err)
	if r.rule != "R19" {
		t.Fatalf("want a refusal under R19, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "escalate") || !strings.Contains(r.msg, "human") {
		t.Errorf("the budget refusal must say the item escalates to a human: %s", r.msg)
	}
	return r
}

// R19: three code rounds per item, and a fourth is refused under R19. The refusal names the
// cap, says the item escalates to a human, and writes nothing at all.
func TestR19TheThirdCodeRoundIsAcceptedAndTheFourthIsRefused(t *testing.T) {
	dir := roundItem(t, roundFailList)
	for i := 0; i < roundCap; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	before, _ := readLedger(dir)
	files := evidenceFiles(t, dir)
	r := wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
	if !strings.Contains(r.msg, "cap 3") {
		t.Errorf("the refusal must name the cap: %s", r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
	if got := evidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused round wrote an evidence file: %v", got)
	}
}

// R19: three harness rounds per item, and a fourth is refused under R19 — even though the
// item has spent only four rounds in total.
func TestR19TheFourthHarnessRoundIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	// a dispute round is a round too, and the budget still has room
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"))
}

// R19: three dispute rounds per item, and a fourth is refused under R19.
func TestR19TheFourthDisputeRoundIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	for i := 0; i < roundCap; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
			t.Fatalf("dispute round %d: %v", i+1, err)
		}
	}
	// a harness round is a round too
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows))
}

// R19: six rounds in total, of any cause. The seventh is refused under R19 although no cause
// has spent three rounds.
func TestR19TheSeventhRoundIsRefused(t *testing.T) {
	dir := roundItem(t, roundFailList)
	rounds := []struct{ cause string }{
		{"code"}, {"code"}, {"harness"}, {"harness"}, {"dispute"}, {"dispute"},
	}
	for i, r := range rounds {
		argv := []string{"A1", "--verdict", "FAIL", "--cause", r.cause}
		if r.cause != "harness" {
			argv = append(argv, "--rows", runAndVerdicts(t, dir, roundFail...))
			if r.cause == "dispute" {
				argv = append(argv, "--finding", "a new finding")
			}
		}
		if err := call(t, cmdRound, dir, argv...); err != nil {
			t.Fatalf("round %d (%s): %v", i+1, r.cause, err)
		}
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: a PASS round is a round too — it counts towards the six, and the seventh is refused.
// The refusal is the state guard's (R18): the item's last round is a PASS, so the only
// legal next step is the close.
func TestR19ACountsAPassRoundTowardsTheTotal(t *testing.T) {
	dir, marker := switchItem(t)
	// The binary row fails while the marker is there, so the code rounds are FAIL rounds.
	markFailing(t, marker)
	for i := 0; i < 2; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	// the sixth round is a PASS and it is accepted: the marker is gone, so the binary row
	// passes and the derived verdict is PASS
	markPassing(t, marker)
	rows = runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	// the PASS counted: the item has spent its six
	events, _ := readLedger(dir)
	_, byID, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if n := len(byID["a1"].Rounds); n != totalCap {
		t.Fatalf("rounds = %d, want %d — the PASS round counts towards the six", n, totalCap)
	}
	// and the seventh round is refused: the item already passed, so the only legal next
	// step is the close
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"))
	if r.rule != "R18" {
		t.Fatalf("want a refusal under R18, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "already passed") {
		t.Errorf("the refusal must say the item already passed: %s", r.msg)
	}
}

// R19: the counts are over the item's whole life, so a re-baseline resets none of them — the
// six-round budget survives two re-baselines.
func TestR19TheBudgetSurvivesTwoRebaselines(t *testing.T) {
	dir := gitItem(t)
	legacyChecks(t, dir, roundList)
	// Two re-baselines: each one replaces the frozen list with a new premise. The claims
	// freeze the list (R5), so they come before the claims — and before every round, because
	// a round needs a claims event (R6).
	for i := 0; i < 2; i++ {
		list := writeFile(t, t.TempDir(), "c.tsv", rebaselinedList)
		if err := call(t, cmdRebaseline, dir, "A1", "--file", list, "--reason", "the list's own command was broken"); err != nil {
			t.Fatalf("rebaseline %d: %v", i+1, err)
		}
	}
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	// The budget is spent by the rounds that follow the re-baselines: two code rounds, two
	// harness rounds and two dispute rounds.
	for i := 0; i < 2; i++ {
		rows := runAndVerdicts(t, dir, "S1\tFAIL\t")
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	for i := 0; i < 2; i++ {
		rows := runAndVerdicts(t, dir, "S1\tFAIL\t")
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
			t.Fatalf("dispute round %d: %v", i+1, err)
		}
	}
	// six rounds are spent, and the seventh is refused whatever the re-baselines did
	rows := runAndVerdicts(t, dir, "S1\tFAIL\t")
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: the same holds when the item has claims. The claims freeze the check list (R5), so the
// re-baseline is refused — and either way the counts are not reset: the fourth code round is
// refused under R19.
func TestR19TheCountsSurviveARebaselineRefusedUnderR5(t *testing.T) {
	dir := roundItem(t, roundFailList)
	for i := 0; i < roundCap; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	// the claims freeze the check list (R5): the re-baseline is refused
	wantRule(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "again"), "R5")
	// and the counts are not reset: the fourth code round is refused under R19
	rows := runAndVerdicts(t, dir, roundFail...)
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: a refused round exits 2, writes no event, and leaves the item's state untouched.
func TestR19ARefusedRoundWritesNothing(t *testing.T) {
	dir := roundItem(t, roundFailList)
	for i := 0; i < roundCap; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	before, _ := readLedger(dir)
	if code := run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows}); code != 2 {
		t.Fatalf("bl round: exit = %d, want 2", code)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
	if last := after[len(after)-1]; last.T != "run" {
		t.Fatalf("a refused round wrote %+v", last)
	}
}

// R19: `bl status` shows the round counts — the total and how many of each cause, so the
// harness and dispute rounds are visible.
func TestStatusShowsTheHarnessAndDisputeCounts(t *testing.T) {
	dir := roundItem(t, roundList)
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	want := "rounds: 3 (code 0, harness 2, dispute 1, pass 0)"
	var err error
	out := capture(t, func() { err = call(t, cmdStatus, dir) })
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if !strings.Contains(out, want) {
		t.Errorf("bl status must show the counts (%q):\n%s", want, out)
	}
	out = capture(t, func() { err = call(t, cmdStatus, dir, "A1") })
	if err != nil {
		t.Fatalf("status A1: %v", err)
	}
	if !strings.Contains(out, want) {
		t.Errorf("bl status A1 must show the counts (%q):\n%s", want, out)
	}
}

// R19: `bl status` counts the PASS rounds too — the total is every round of every cause, and a
// PASS is a round, so it is visible in the budget.
func TestStatusCountsThePassRounds(t *testing.T) {
	dir, marker := switchItem(t)
	markFailing(t, marker)
	for i := 0; i < 3; i++ {
		rows := runAndVerdicts(t, dir, roundFail...)
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--finding", "a new finding", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	// the last round is a PASS: the marker is gone, so the binary row passes
	markPassing(t, marker)
	rows = runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	want := "rounds: 6 (code 3, harness 1, dispute 1, pass 1)"
	var err error
	out := capture(t, func() { err = call(t, cmdStatus, dir, "A1") })
	if err != nil {
		t.Fatalf("status A1: %v", err)
	}
	if !strings.Contains(out, want) {
		t.Errorf("bl status must count the PASS rounds (%q):\n%s", want, out)
	}
}

// R23: a harness round carries no rows, so it is recordable without --rows and its event
// records no rows.
func TestHarnessRoundIsRecordedWithoutRows(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "harness" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Rows != "" || ev.RowsSHA != "" {
		t.Fatalf("a harness round carries no rows: %+v", ev)
	}
	if files := evidenceFiles(t, dir); len(files) != 0 {
		t.Fatalf("a harness round wrote an evidence file: %v", files)
	}
	// a code round still needs rows
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code"); err == nil {
		t.Fatal("a code round without rows must be refused")
	}
	// and the ledger still verifies: a harness round has no evidence file to check
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// R18: the state guard is checked BEFORE the budget. An item whose last round is a PASS and
// whose budget is spent is refused with the already-passed refusal, not with the budget's —
// and the refusal writes nothing.
func TestR18TheStateGuardOutranksTheBudget(t *testing.T) {
	dir, marker := switchItem(t)
	markFailing(t, marker)
	for i, cause := range []string{"code", "harness", "dispute", "code", "dispute"} {
		argv := []string{"A1", "--verdict", "FAIL", "--cause", cause}
		if cause != "harness" {
			argv = append(argv, "--rows", runAndVerdicts(t, dir, roundFail...))
		}
		if cause == "dispute" {
			argv = append(argv, "--finding", "a new finding")
		}
		if err := call(t, cmdRound, dir, argv...); err != nil {
			t.Fatalf("round %d (%s): %v", i+1, cause, err)
		}
	}
	// the sixth round is a PASS: the marker is gone, so the binary row passes
	markPassing(t, marker)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	// and the round after it is a FAIL round again: the marker is back
	markFailing(t, marker)
	rows = runAndVerdicts(t, dir, roundFail...)
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
	if r.rule != "R18" {
		t.Fatalf("want a refusal under R18, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "already passed") {
		t.Errorf("the refusal must say the item already passed: %s", r.msg)
	}
	if code := run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows}); code != 2 {
		t.Fatalf("bl round: exit = %d, want 2", code)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
}

// R18: a round after a PASS is refused — the item is complete, so the only legal next step is
// the close, whatever is left of the budget (R19). The refusal names R18, says the item
// already passed, and writes nothing.
func TestR18ARoundAfterAPassIsRefused(t *testing.T) {
	dir, marker := switchItem(t)
	markFailing(t, marker)
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("code round: %v", err)
	}
	// the marker is gone, so the binary row passes and the round is a PASS
	markPassing(t, marker)
	rows = runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	// and the marker is back, so the round after it would be a FAIL round
	markFailing(t, marker)
	rows = runAndVerdicts(t, dir, roundFail...)
	before, _ := readLedger(dir)
	files := evidenceFiles(t, dir)
	// the budget still has four rounds: the state guard is what refuses
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
	if r.rule != "R18" {
		t.Fatalf("want a refusal under R18, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "already passed") {
		t.Errorf("the refusal must say the item already passed: %s", r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
	if got := evidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused round wrote an evidence file: %v", got)
	}
}

// R18: the refusal is written for a human to read — it names R18, says the item already
// passed, and the call exits 2.
func TestR18TheRefusalNamesTheRuleAndExitsTwo(t *testing.T) {
	dir, marker := switchItem(t)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	// the marker is back, so the round after the PASS is a FAIL round — and it is the state
	// guard that refuses it, not the verdict
	markFailing(t, marker)
	rows = runAndVerdicts(t, dir, roundFail...)
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows})
	})
	if code != 2 {
		t.Fatalf("bl round: exit = %d, want 2", code)
	}
	want := "bl: refused (R18): A1 already passed — close it"
	if !strings.Contains(out, want) {
		t.Errorf("the refusal must read %q:\n%s", want, out)
	}
}

// R12: a PASS round carries no cause and no finding, so it needs no rows of its own: it may be
// recorded without any.
//
// Under the design the verdict is DERIVED from the rows, so a PASS round needs the design's
// verdicts file — and a PASS round without one is refused (R6). The round that is recorded
// without rows is the harness round (R23): the verifier did not run, so there is nothing to
// record and nothing to hash (proved by TestHarnessRoundIsRecordedWithoutRows).
func TestAPassRoundIsRecordedWithoutRows(t *testing.T) {
	dir := roundItem(t, roundList)
	// A PASS round without the design's verdicts file is refused, and the refusal names the
	// rule and the reason.
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "PASS"))
	if r.rule != "R6" {
		t.Fatalf("want a refusal under R6, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "verdicts file") {
		t.Errorf("the refusal must name what is missing: %s", r.msg)
	}
	before, _ := readLedger(dir)
	files := roundEvidenceFiles(t, dir)
	// The harness round is the one that is recorded without rows: no rows, no rows_sha and
	// no evidence file.
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "harness" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Rows != "" || ev.RowsSHA != "" {
		t.Fatalf("a round recorded without rows carries none: %+v", ev)
	}
	if got := roundEvidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a round recorded without rows wrote an evidence file: %v", got)
	}
	// and the ledger still verifies: a round with no rows has no evidence file to check
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before)+1 {
		t.Fatalf("the ledger holds %d events, want %d", len(after), len(before)+1)
	}
}

// R12: a PASS round's verdict is DERIVED from the rows, so it always carries the verdicts file
// it was derived from and the evidence file that goes with it — and it carries no cause.
//
// The round that carries no rows is the harness round (R23): the verifier did not run, so
// there is nothing to record and nothing to hash. That is proved by
// TestHarnessRoundIsRecordedWithoutRows.
func TestAPassRoundIsRecordedWithItsRowsAndNoCause(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" || ev.Cause != "" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Rows == "" || ev.RowsSHA == "" {
		t.Fatalf("a PASS round carries the rows it was derived from: %+v", ev)
	}
	if files := roundEvidenceFiles(t, dir); len(files) != 1 {
		t.Fatalf("a PASS round wrote one evidence file: %v", files)
	}
	// and the ledger still verifies: the evidence file is hashed into the event
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}
