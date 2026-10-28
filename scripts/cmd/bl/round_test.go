package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// ── I3: the round path, to the design's contract ────────────────────────────

// gitItemWith opens an item in a repository that already holds the given files: the item's
// before-tree is the tree the item was opened against, so a file written before the open is
// part of the tree the round's diff is taken against.
func gitItemWith(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for name, body := range files {
		writeFile(t, dir, name, body)
	}
	if len(files) > 0 {
		commitSeed(t, dir)
	}
	for _, argv := range [][]string{
		{"init"},
		{"file", "--id", "A1", "--text", "add a verb; test it"},
		{"open", "A1"},
	} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	return dir
}

// commitSeed commits everything in dir, so a following bl open sees a clean tree (R5).
func commitSeed(t *testing.T, dir string) {
	t.Helper()
	for _, argv := range [][]string{{"add", "-A"}, {"-c", "user.name=bl test", "-c", "user.email=bl@test.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "seed"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
}

// A well-formed round over a well-formed run and verdicts file passes, and it writes the
// evidence file in the design's format (§2): one line per frozen check id, in the order the
// list was frozen, with the cmd, the exit code, the signalled and timed_out flags, the
// observed hashes, the excerpt and the row_verdict.
func TestRoundOverAWellFormedRunAndVerdictsFileWritesTheEvidenceFile(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" || ev.Cause != "" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Run != latestRunID(t, dir) {
		t.Errorf("the round must name the run it consumed: %+v", ev)
	}
	if ev.Tree == "" {
		t.Errorf("the round must record the tree it verified: %+v", ev)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "evidence", "A1-r1.txt"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("the evidence file has %d lines, want 3 (a header and two rows):\n%s", len(lines), data)
	}
	if !strings.HasPrefix(lines[0], "id\tcmd\texit\tsignalled\ttimed_out\tobserved\texcerpt\trow_verdict") {
		t.Errorf("the evidence file's header = %q", lines[0])
	}
	first := regexp.MustCompile(`^S1\techo two\t0\tfalse\tfalse\tstdout sha [0-9a-f]+, stderr sha [0-9a-f]+\t.*\tPASS$`)
	if !first.MatchString(lines[1]) {
		t.Errorf("the S1 line = %q", lines[1])
	}
	// the judge row carries its anchor in the observed column
	if !strings.Contains(lines[2], `; anchor "two"`) || !strings.HasSuffix(lines[2], "\tPASS") {
		t.Errorf("the J1 line = %q", lines[2])
	}
	// the event carries the hash of the evidence file
	if ev.RowsSHA != shaHex(data) {
		t.Errorf("the event must carry the evidence file's hash: %+v", ev)
	}
	// and the ledger still verifies
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// R6: the verdicts file must match the frozen list, the latest run and the current verified
// tree. Each of these is a refusal, and nothing is written.
func TestR6TheVerdictsFileMustMatchTheListTheRunAndTheTree(t *testing.T) {
	cases := []struct {
		name  string
		build func(t *testing.T, dir string) string
	}{
		{
			name: "a missing id",
			build: func(t *testing.T, dir string) string {
				return writeVerdicts(t, dir, "S1\tPASS\t")
			},
		},
		{
			name: "an unknown id",
			build: func(t *testing.T, dir string) string {
				return writeVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"two\"", "X9\tPASS\t")
			},
		},
		{
			name: "a stale run",
			build: func(t *testing.T, dir string) string {
				// a verdicts file that names the first run ...
				file := writeVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"two\"")
				// ... and then a second run, so that the first one is no longer the latest
				if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
					t.Fatalf("run: %v", err)
				}
				return file
			},
		},
		{
			name: "a wrong tree",
			build: func(t *testing.T, dir string) string {
				return writeVerdictsWith(t, dir, "0000000000000000000000000000000000000000",
					"S1\tPASS\t", "J1\tPASS\t\"two\"")
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := roundItem(t, roundList)
			if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
				t.Fatalf("run: %v", err)
			}
			rows := c.build(t, dir)
			before, _ := readLedger(dir)
			files := roundEvidenceFiles(t, dir)
			r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--rows", rows))
			if r.rule != "R6" {
				t.Fatalf("want a refusal under R6, got %s: %s", r.rule, r.msg)
			}
			after, _ := readLedger(dir)
			if len(after) != len(before) {
				t.Errorf("a refused round wrote %d events", len(after)-len(before))
			}
			if got := roundEvidenceFiles(t, dir); len(got) != len(files) {
				t.Errorf("a refused round wrote an evidence file: %v", got)
			}
		})
	}
}

// writeVerdictsWith builds a verdicts file that names a tree of its own — a tree that is not
// the one the round verifies.
func writeVerdictsWith(t *testing.T, dir, tree string, rows ...string) string {
	t.Helper()
	var b strings.Builder
	b.WriteString("id\trow_verdict\tanchor\n")
	fmt.Fprintf(&b, "run %s\n", latestRunID(t, dir))
	fmt.Fprintf(&b, "tree %s\n", tree)
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	return writeFile(t, t.TempDir(), "verdicts.tsv", b.String())
}

// R6: a run that a round has already consumed cannot be consumed again.
func TestR6ARunThatARoundConsumedCannotBeConsumedAgain(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	before, _ := readLedger(dir)
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R6")
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Errorf("a refused round wrote %d events", len(after)-len(before))
	}
}

// R6: the item must have a claims event: the claims are frozen before the round.
func TestR6AnItemWithNoClaimsCannotHaveARound(t *testing.T) {
	dir := gitItem(t)
	legacyChecks(t, dir, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R6")
}

// R9: a judge row's anchor is checked in the form it is written in, and a missing or empty
// anchor is refused.
func TestR9AJudgeRowWithNoAnchorIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t")
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R9")
}

// R9: an output anchor's quoted text must appear in the row's captured stdout or stderr.
func TestR9AnOutputAnchorThatIsNotInTheOutputIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"zzz-not-in-output\"")
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R9")
}

// R9: a source anchor is checked as a source anchor, whatever its quoted text says: the line
// must lie in a hunk of the round's diff.
func TestR9ASourceAnchorOutsideEveryHunkIsRefused(t *testing.T) {
	dir := gitItemWith(t, map[string]string{"f.txt": "one\ntwo\nthree\n"})
	legacyChecks(t, dir, roundList)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	// only the first line changes: the hunk covers line 1 alone
	writeFile(t, dir, "f.txt", "ONE\ntwo\nthree\n")
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"f.txt:2 \"two\"\"")
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R9")
}

// R10: a hunk that no blocking row covers refuses the round.
func TestR10AnUncoveredHunkIsRefused(t *testing.T) {
	dir := gitItem(t)
	// the only blocking row's command does not name the file that changed
	list := checksHeader + "S1\tbl\tblocking\tB1\techo hi\tstdout matches ^hi\t\t\tdoes not name the changed file\n"
	legacyChecks(t, dir, list)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	writeFile(t, dir, "f.txt", "two\n")
	rows := runAndVerdicts(t, dir, "S1\tPASS\t")
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R10")
}

// R12: the verdict is derived from the blocking rows. A supplied --verdict that differs from
// the derived one is refused.
func TestR12ADerivedFailWithASuppliedPassIsRefused(t *testing.T) {
	dir := roundItem(t, roundFailList)
	rows := runAndVerdicts(t, dir, roundFail...)
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows))
	if r.rule != "R12" {
		t.Fatalf("want a refusal under R12, got %s: %s", r.rule, r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Errorf("a refused round wrote %d events", len(after)-len(before))
	}
	// and the derived verdict is recorded when --verdict is absent
	if err := call(t, cmdRound, dir, "A1", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "code" {
		t.Fatalf("round event = %+v", ev)
	}
}

// R7 and R12: the round's derived verdict is computed from the row-verdicts the binary computed,
// so a judge that writes PASS for a failing row does not get a PASS round. The judge's word for
// a binary row is not that row's verdict.
func TestTheRoundDoesNotTakeTheJudgesWordForABinaryRow(t *testing.T) {
	dir := roundItem(t, roundFailList)
	// the judge writes PASS for the row, and the run says the row failed
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"two\"")
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if rf.Rows[0].Verdict != "FAIL" {
		t.Fatalf("the run must record the row as FAIL: %+v", rf.Rows[0])
	}
	if err := call(t, cmdRound, dir, "A1", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" {
		t.Fatalf("round event = %+v — the judge's PASS must not make the round pass", ev)
	}
	// and the evidence file records the computed verdict, not the judge's word
	body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ev.Rows)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "\tFAIL\n") {
		t.Errorf("the evidence file must record the computed FAIL:\n%s", body)
	}
}

// R12: a supplied --verdict that disagrees with the derived one is refused under R12, and one
// that agrees is accepted.
func TestR12TheSuppliedVerdictIsCheckedAgainstTheDerivedOne(t *testing.T) {
	dir := roundItem(t, roundFailList)
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"two\"")
	// the derived verdict is FAIL, and a supplied PASS is refused under R12
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows))
	if r.rule != "R12" {
		t.Fatalf("want a refusal under R12, got %s: %s", r.rule, r.msg)
	}
	// and the supplied FAIL agrees with the derived one, so the round is recorded
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "code" {
		t.Fatalf("round event = %+v", ev)
	}
}

// R13: a finding makes the round a dispute: the verdict is FAIL, the cause is dispute, and
// the finding text is recorded.
func TestR13AFindingMakesTheRoundADispute(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--finding", "the migration drops a column", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "dispute" {
		t.Fatalf("round event = %+v", ev)
	}
	if !strings.Contains(ev.Finding, "the migration drops a column") {
		t.Errorf("the round must record the finding text: %+v", ev)
	}
}

// R13: a `# finding:` comment line in the verdicts file makes the round a dispute too, and a
// supplied --cause that conflicts with the derived cause is refused.
func TestR13AFindingInTheVerdictsFileIsADisputeAndTheCauseIsDerived(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	rows = appendComment(t, rows, "# finding: the migration drops a column v1 reads")
	wantRule(t, call(t, cmdRound, dir, "A1", "--cause", "code", "--rows", rows), "R23")
	// the same file without the cause is recorded as a dispute
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.Verdict != "FAIL" || ev.Cause != "dispute" {
		t.Fatalf("round event = %+v", ev)
	}
	if !strings.Contains(ev.Finding, "the migration drops a column") {
		t.Errorf("the round must record the finding text: %+v", ev)
	}
}

// mustTree returns the item's current verified tree.
func mustTree(t *testing.T, dir string) string {
	t.Helper()
	tree, err := verifiedTreeWriting(dir)
	if err != nil {
		t.Fatal(err)
	}
	return tree
}

// appendComment adds a comment line to a verdicts file.
func appendComment(t *testing.T, path, comment string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return writeFile(t, filepath.Dir(path), "verdicts.tsv", string(data)+comment+"\n")
}

// R14: an advisory row that fails must already be filed as an item, with the row id as a
// whole token in the item's text.
func TestR14AFailingAdvisoryRowMustBeFiled(t *testing.T) {
	list := checksHeader +
		"S1\tbl\tblocking\tB1\techo two\tstdout matches ^two\t\t\tprints two\n" +
		"J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n" +
		"V1\tbl\tadvisory\t\techo two\tstdout matches ^zzz\t\t\tadvisory misses\n"
	dir := roundItem(t, list)
	rows := runAndVerdicts(t, dir, "S1\tPASS\t", "J1\tPASS\t\"two\"", "V1\tFAIL\t")
	// not filed yet
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--rows", rows))
	if r.rule != "R14" {
		t.Fatalf("want a refusal under R14, got %s: %s", r.rule, r.msg)
	}
	// filed, but the id is not a whole token
	if err := call(t, cmdFile, dir, "A1", "--id", "B2", "--text", "V10 xV1 V1."); err != nil {
		t.Fatalf("file: %v", err)
	}
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R14")
	// filed with the id as a whole token
	if err := call(t, cmdFile, dir, "A1", "--id", "B3", "--text", "retry V1 now"); err != nil {
		t.Fatalf("file: %v", err)
	}
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" {
		t.Fatalf("round event = %+v", ev)
	}
}

// R16: a second check that disagrees with the judge row's recorded verdict refuses the round.
func TestR16ASecondCheckThatDisagreesRefusesTheRound(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	// the second judge's command prints two, and expects zzz: it computes FAIL for J1
	if err := call(t, cmdSecondCheck, dir, "A1", "--row", "J1", "--cmd", "echo two", "--expect", "stdout matches ^zzz"); err != nil {
		t.Fatalf("second check: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "second-check" || ev.Row != "J1" || ev.Computed != "FAIL" {
		t.Fatalf("second-check event = %+v", ev)
	}
	// the round's verdict says PASS, and the second check says FAIL: the round is refused
	wantRule(t, call(t, cmdRound, dir, "A1", "--rows", rows), "R16")
	// and the misclassified event is recorded: it is not a round, and it consumes no budget
	events, _ = readLedger(dir)
	ev = events[len(events)-1]
	if ev.T != "misclassified" || ev.Row != "J1" {
		t.Fatalf("the misclassified event = %+v", ev)
	}
}

// R16: a second check that agrees with the row's recorded verdict lets the round stand.
func TestR16ASecondCheckThatAgreesLetsTheRoundStand(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdSecondCheck, dir, "A1", "--row", "J1", "--cmd", "echo two", "--expect", "stdout matches ^two"); err != nil {
		t.Fatalf("second check: %v", err)
	}
	events, _ := readLedger(dir)
	if ev := events[len(events)-1]; ev.T != "second-check" || ev.Computed != "PASS" {
		t.Fatalf("second-check event = %+v, want computed PASS", ev)
	}
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ = readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" {
		t.Fatalf("round event = %+v", ev)
	}
}

// ── I3: the scope and the ignored files under it (R28) ───────────────────────

// scopedItem prepares an item whose scope holds an ignored file, and returns the directory.
// The ignored file is written before the item is opened, so the before-state records its
// hash.
func scopedItem(t *testing.T, scopeIgnored string) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	writeFile(t, dir, ".gitignore", "build/\n")
	if err := os.MkdirAll(filepath.Join(dir, "build"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "build/out.txt", "one\n")
	commitSeed(t, dir)
	open := []string{"open", "A1", "--scope", "build/"}
	if scopeIgnored != "" {
		open = append(open, "--scope-ignored", scopeIgnored)
	}
	for _, argv := range [][]string{
		{"init"},
		{"file", "--id", "A1", "--text", "item"},
		open,
	} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	legacyChecks(t, dir, roundList)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return dir
}

// R28: an ignored file under the item's scope may not change, unless the item declared it as
// one it may write.
func TestR28AnIgnoredFileUnderTheScopeMayNotChange(t *testing.T) {
	dir := scopedItem(t, "")
	// the before-state holds the ignored file and its hash
	events, _ := readLedger(dir)
	open := events[1]
	if open.T != "open" || len(open.Scope) != 1 || open.Scope[0] != "build/" {
		t.Fatalf("the open event must record the scope: %+v", open)
	}
	if open.BeforeTree == "" {
		t.Errorf("the open event must record the tree the item was opened against: %+v", open)
	}
	if len(open.IgnoredBefore) != 1 || !strings.HasPrefix(open.IgnoredBefore[0], "build/out.txt\t") {
		t.Fatalf("the open event must record the ignored files under the scope: %+v", open)
	}
	// the ignored file changes
	writeFile(t, dir, "build/out.txt", "two\n")
	rows := runAndVerdicts(t, dir, roundPass...)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--rows", rows))
	if r.rule != "R28" {
		t.Fatalf("want a refusal under R28, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "build/out.txt") {
		t.Errorf("the refusal must name the file: %s", r.msg)
	}
}

// R28: a file the item declared as one it may write is not a refusal.
func TestR28AnIgnoredFileTheItemMayWriteIsAccepted(t *testing.T) {
	dir := scopedItem(t, "build/out.txt")
	writeFile(t, dir, "build/out.txt", "two\n")
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" {
		t.Fatalf("round event = %+v", ev)
	}
}

// ── I3, round 2: a harness round carries no finding ──────────────────────────

// R23: a harness round carries no finding. The verifier did not run, so it found nothing, and
// a finding is never silently ignored (R13). The refusal writes nothing.
func TestR23AHarnessRoundWithAFindingIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	before, _ := readLedger(dir)
	files := roundEvidenceFiles(t, dir)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--cause", "harness", "--verdict", "FAIL",
		"--finding", "the migration drops a column"))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "finding") {
		t.Errorf("the refusal must name the finding: %s", r.msg)
	}
	// the call exits 2 and the message names the rule
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"--root", dir, "round", "A1", "--cause", "harness", "--verdict", "FAIL",
			"--finding", "the migration drops a column"})
	})
	if code != 2 {
		t.Fatalf("bl round: exit = %d, want 2", code)
	}
	if !strings.Contains(out, "refused (R23)") {
		t.Errorf("the refusal must name R23: %s", out)
	}
	// and nothing was written: no event, no evidence file
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
	if got := roundEvidenceFiles(t, dir); len(got) != len(files) {
		t.Fatalf("a refused round wrote an evidence file: %v", got)
	}
}

// R23: a harness round whose verdicts file carries a `# finding:` line is refused the same
// way: a finding on a round that never ran is never silently ignored.
func TestR23AHarnessRoundWithAFindingInTheVerdictsFileIsRefused(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	rows = appendComment(t, rows, "# finding: the migration drops a column v1 reads")
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--cause", "harness", "--verdict", "FAIL", "--rows", rows))
	if r.rule != "R23" {
		t.Fatalf("want a refusal under R23, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "finding") {
		t.Errorf("the refusal must name the finding: %s", r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
}

// A round that is not a harness round is unaffected: its finding still makes it a dispute
// under R13.
func TestR13ANonHarnessRoundStillAcceptsAFinding(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--finding", "the migration drops a column", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "FAIL" || ev.Cause != "dispute" {
		t.Fatalf("round event = %+v", ev)
	}
	if !strings.Contains(ev.Finding, "the migration drops a column") {
		t.Errorf("the round must record the finding text: %+v", ev)
	}
}

// ── I3, round 2: a nested .git is skipped from the verified tree ─────────────

// hermeticGit runs a git command that does not read the machine's git configuration: the global
// and the system config are switched off, so the test does not depend on the user it runs as.
func hermeticGit(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	home := t.TempDir()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(),
		"HOME="+home,
		"XDG_CONFIG_HOME="+home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
	)
	return cmd
}

// A nested `.git` — a submodule's gitfile, or a nested repository — is skipped from the
// verified tree, the way git skips it: git refuses a tree that holds a .git entry, so hashing
// one would fail the whole tree. The tree is computed, and the run succeeds.
//
// The nested repository is a REAL one — a git init, a file and a commit in it — so that the
// case binds: a tree computation that stopped skipping a nested `.git` directory would hold
// the nested repository's metadata, and the tree would hold a .git entry.
func TestANestedGitIsSkippedFromTheVerifiedTree(t *testing.T) {
	for _, kind := range []string{"file", "dir"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			if out, err := hermeticGit(t, "init", "-q", dir).CombinedOutput(); err != nil {
				t.Fatalf("git init: %v: %s", err, out)
			}
			// the item is opened on the clean tree (R5); the nested .git is written after
			for _, argv := range [][]string{
				{"init"},
				{"file", "--id", "A1", "--text", "item"},
				{"open", "A1"},
			} {
				if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
					t.Fatalf("bl %v: exit %d", argv, code)
				}
			}
			sub := filepath.Join(dir, "sub")
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if kind == "file" {
				// a submodule's gitfile
				if err := os.WriteFile(filepath.Join(sub, ".git"), []byte("gitdir: ../.git/modules/sub\n"), 0o644); err != nil {
					t.Fatal(err)
				}
			} else {
				// a nested repository: git init, a file, and a commit — the identity is given
				// in the invocation, so the commit needs no global one
				if out, err := hermeticGit(t, "init", "-q", sub).CombinedOutput(); err != nil {
					t.Fatalf("git init (nested): %v: %s", err, out)
				}
				writeFile(t, sub, "keep.txt", "nested\n")
				if out, err := hermeticGit(t, "-C", sub, "add", "keep.txt").CombinedOutput(); err != nil {
					t.Fatalf("git add (nested): %v: %s", err, out)
				}
				if out, err := hermeticGit(t, "-C", sub,
					"-c", "user.name=bl test", "-c", "user.email=bl@test.invalid",
					"-c", "commit.gpgsign=false", "commit", "-q", "-m", "nested commit").CombinedOutput(); err != nil {
					t.Fatalf("git commit (nested): %v: %s", err, out)
				}
				// the nested repository really holds a commit
				if out, err := hermeticGit(t, "-C", sub, "rev-parse", "--verify", "HEAD").CombinedOutput(); err != nil {
					t.Fatalf("the nested repository holds no commit: %v: %s", err, out)
				}
			}
			writeFile(t, dir, "a.txt", "hi\n")
			// the tree is computed, and it holds no .git entry
			tree, err := verifiedTreeWriting(dir)
			if err != nil {
				t.Fatalf("the verified tree: %v", err)
			}
			out, code := git(dir, "ls-tree", "-r", tree)
			if code != 0 {
				t.Fatalf("ls-tree: exit %d", code)
			}
			if strings.Contains(out, ".git") {
				t.Errorf("the tree must not hold a .git entry:\n%s", out)
			}
			if kind == "dir" {
				// only the nested .git is skipped: the nested repository's other files are
				// part of the tree
				if !strings.Contains(out, "sub/keep.txt") {
					t.Errorf("the tree must hold the nested repository's file:\n%s", out)
				}
			}
			// and the run succeeds: the list is frozen, the claims are recorded, and the rows run.
			legacyChecks(t, dir, roundList)
			if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
				t.Fatalf("claims: %v", err)
			}
			if err := call(t, cmdRun, dir, "A1"); err != nil {
				t.Fatalf("run: %v", err)
			}
			rf, _ := readRunFile(t, dir, "A1-run1.json")
			if rf.TreeStart == "" || rf.TreeStart != rf.TreeEnd {
				t.Fatalf("the trees must be present and equal: %+v", rf)
			}
		})
	}
}

// ── I16: R25 says so when the artefacts are tracked ──────────────────────────

// trackedArtefactReport is the shape of the message the check looks for: it names `.backlog` and
// says that it is tracked.
var trackedArtefactReport = regexp.MustCompile(`(?i)(\.backlog[^\n]*[^a-z]tracked|[^a-z]tracked[^\n]*\.backlog)`)

// trackedWord is the word "tracked" as a word: "untracked" is not it.
var trackedWord = regexp.MustCompile(`(?i)(^|[^a-z])tracked`)

// gitRunItem prepares an item with a frozen check list in a git repository, ready to run.
func gitRunItem(t *testing.T) string {
	t.Helper()
	dir := gitItem(t)
	checksOK(t, dir, oneRowList)
	return dir
}

// trackedArtefactItem prepares the same item in a repository that TRACKS `.backlog/`: the
// artefacts are staged, the way a mistake would leave them.
func trackedArtefactItem(t *testing.T) string {
	t.Helper()
	dir := gitRunItem(t)
	if out, err := exec.Command("git", "-C", dir, "add", "-f", ".backlog").CombinedOutput(); err != nil {
		t.Fatalf("git add -f .backlog: %v: %s", err, out)
	}
	return dir
}

// A repository that tracks `.backlog/` is a repository error, and `bl` says so: the run says it
// on stderr, and the run still happens. The artefacts are excluded from the tree either way.
func TestR25TheRunSaysSoWhenTheArtefactsAreTracked(t *testing.T) {
	dir := trackedArtefactItem(t)
	var code int
	out := captureStderr(t, func() {
		capture(t, func() { code = run([]string{"--root", dir, "run", "A1"}) })
	})
	if code != 0 {
		t.Fatalf("the run must happen with the artefacts tracked: exit %d\n%s", code, out)
	}
	if !trackedArtefactReport.MatchString(out) {
		t.Fatalf("the run must say that .backlog is tracked:\n%s", out)
	}
	// the report is not a refusal: the run file is written
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); err != nil {
		t.Fatalf("run file: %v", err)
	}
	// and the artefacts are still not part of the verified tree
	tree, err := verifiedTreeWriting(dir)
	if err != nil {
		t.Fatalf("the verified tree: %v", err)
	}
	ls, lsCode := git(dir, "ls-tree", "-r", tree)
	if lsCode != 0 {
		t.Fatalf("ls-tree: exit %d", lsCode)
	}
	if strings.Contains(ls, backlogDir) {
		t.Errorf("the tree must not hold the artefacts:\n%s", ls)
	}
}

// With the artefacts untracked — the normal case — the run says nothing about them being tracked.
func TestR25TheRunSaysNothingWhenTheArtefactsAreUntracked(t *testing.T) {
	dir := gitRunItem(t)
	var code int
	out := captureStderr(t, func() {
		capture(t, func() { code = run([]string{"--root", dir, "run", "A1"}) })
	})
	if code != 0 {
		t.Fatalf("the run must succeed: exit %d\n%s", code, out)
	}
	if trackedWord.MatchString(out) {
		t.Fatalf("the run must say nothing about .backlog being tracked:\n%s", out)
	}
}

// The round's own check is unchanged by the report: a repository that tracks the artefacts is
// refused under R25, and a repository that does not is not.
func TestR25TheRoundStillRefusesATrackedArtefactDirectory(t *testing.T) {
	wantRule(t, checkR25(trackedArtefactItem(t)), "R25")
	if err := checkR25(gitRunItem(t)); err != nil {
		t.Fatalf("checkR25: %v", err)
	}
}

// The two rendered views are artefacts (R25): every event in the ledger rewrites them, so they
// are never part of a verified tree. A tree computed where the views exist and computed again
// after the ledger moved and rewrote them is the same tree: the tree pin does not refuse a round
// because the ledger moved.
func TestR25TheRenderedViewsAreExcludedFromTheVerifiedTree(t *testing.T) {
	dir := gitItem(t)
	before, err := verifiedTreeWriting(dir)
	if err != nil {
		t.Fatalf("the verified tree: %v", err)
	}
	ls, code := git(dir, "ls-tree", "-r", before)
	if code != 0 {
		t.Fatalf("ls-tree: exit %d", code)
	}
	if strings.Contains(ls, queueView) || strings.Contains(ls, recordView) {
		t.Fatalf("the tree must not hold the rendered views:\n%s", ls)
	}
	// the ledger moves: the event rewrites the two views
	view := filepath.Join(dir, recordView)
	old, err := os.ReadFile(view)
	if err != nil {
		t.Fatalf("the record view: %v", err)
	}
	if _, err := appendEvent(dir, Event{T: "note", ID: "A1", Text: "the ledger moved"}); err != nil {
		t.Fatalf("appendEvent: %v", err)
	}
	now, err := os.ReadFile(view)
	if err != nil {
		t.Fatalf("the record view: %v", err)
	}
	if string(now) == string(old) {
		t.Fatalf("the event must have rewritten the view")
	}
	after, err := verifiedTreeWriting(dir)
	if err != nil {
		t.Fatalf("the verified tree: %v", err)
	}
	if after != before {
		t.Fatalf("the tree moved when the views were rewritten: %s before, %s after", before, after)
	}
}

// ── I23: a defective row is corrected after the claims by adding a row ───────

const amendAddedRow = "ADD1\tbl\tblocking\tB1\techo added\texit 0\t\t\tadded\n"

// amendJudgeToBL is roundList's J1 row reclassified judge → bl.
func amendJudgeToBL() string {
	return strings.Replace(roundList, "J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\t",
		"J1\tbl\tblocking\tB1\techo two\tstdout matches ^two\t\t\t", 1)
}

func amendList(t *testing.T, dir, list string) error {
	t.Helper()
	return call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "checks.tsv", list))
}

func TestAmendAddsARowAfterTheClaims(t *testing.T) {
	dir := roundItem(t, roundList)
	before := shaHex([]byte(roundList))
	added := roundList + amendAddedRow
	if err := amendList(t, dir, added); err != nil {
		t.Fatalf("an amendment that adds a row is refused after the claims: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "checks" || ev.PrevSHA != before || ev.ChecksSHA != shaHex([]byte(added)) {
		t.Fatalf("amend event = %+v", ev)
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, byID["a1"].Checks))
	if err != nil {
		t.Fatal(err)
	}
	rows, err := parseChecks(string(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].f["id"] != "S1" || rows[1].f["id"] != "J1" || rows[2].f["id"] != "ADD1" {
		t.Fatalf("the added row goes at the end, the others stay: %v", rows)
	}
	// the added row is the one the item then runs
	if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
		t.Fatalf("run: %v", err)
	}
	var runFiles []string
	for _, name := range evidenceFiles(t, dir) {
		if strings.HasPrefix(name, "A1-run") && strings.HasSuffix(name, ".json") {
			runFiles = append(runFiles, name)
		}
	}
	if len(runFiles) == 0 {
		t.Fatal("run wrote no evidence")
	}
	run, _ := os.ReadFile(filepath.Join(dir, ".backlog", "evidence", runFiles[len(runFiles)-1]))
	if !strings.Contains(string(run), `"ADD1"`) {
		t.Fatalf("the run does not hold the added row: %s", run)
	}
}

func TestAmendAddedRowIsValidatedLikeAFreshRow(t *testing.T) {
	dir := roundItem(t, roundList)
	cases := map[string]struct{ row, rule string }{
		"R2 clause":  {"ADD2\tbl\tblocking\tB9\techo a\texit 0\t\t\tbad ref\n", "R2"},
		"R8 form":    {"ADD3\tbl\tblocking\tB1\techo a\tstatus 0\t\t\tbad form\n", "R8"},
		"R3 judge":   {"ADD4\tjudge\tblocking\tB1\techo a\t\tif no a, FAIL\tshort\tbad why\n", "R3"},
		"R34 pipe":   {"ADD5\tbl\tblocking\tB1\techo a \\| cat\texit 0\t\t\tescaped pipe\n", "R34"},
		"R1 schema":  {"ADD6\tbl\tblocking\tB1\techo a\t\t\t\tno expect\n", "R1"},
		"R1 same id": {"S1\tbl\tblocking\tB1\techo a\texit 0\t\t\tsame id\n", "R1"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			wantRule(t, amendList(t, dir, roundList+c.row), c.rule)
		})
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T == "checks" && last.PrevSHA != "" {
		t.Fatalf("a refused amendment wrote %+v", last)
	}
}

func TestAmendThatChangesARowIsRefusedAfterTheClaims(t *testing.T) {
	list := roundList + "V1\tbl\tadvisory\t\techo x\texit 0\t\t\tfine\n"
	dir := roundItem(t, list)
	reclass := strings.Replace(list, "J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\t",
		"J1\tbl\tblocking\tB1\techo two\tstdout matches ^two\t\t\t", 1)
	wantRule(t, amendList(t, dir, reclass), "R5")
	wantRule(t, amendList(t, dir, strings.Replace(list, "V1\tbl\tadvisory\t\t", "V1\tbl\tadvisory\tB1\t", 1)), "R5")
	// a change plus an added row is still a change
	wantRule(t, amendList(t, dir, reclass+amendAddedRow), "R5")
}

func TestAmendIsRefusedOnceTheItemHasAPassRound(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	err := amendList(t, dir, roundList+amendAddedRow)
	wantRule(t, err, "R4")
	if !strings.Contains(err.Error(), "PASS round") {
		t.Fatalf("the refusal must name the reason: %v", err)
	}
	// an amendment that changes a row still says R5
	wantRule(t, amendList(t, dir, amendJudgeToBL()), "R5")
}

func TestAmendAddedFailingBlockingRowFailsTheRun(t *testing.T) {
	dir := roundItem(t, roundList)
	if err := amendList(t, dir, roundList+"ADD4\tbl\tblocking\tB1\tfalse\texit 0\t\t\tfails\n"); err != nil {
		t.Fatalf("amend: %v", err)
	}
	if err := call(t, cmdRun, dir, "A1"); !errors.Is(err, errFailed) {
		t.Fatalf("run = %v, want errFailed", err)
	}
}

// The ledger tells an amendment from a forbidden checks event by the hash it records (R4, R5).
func TestDeriveChecksEventAfterClaims(t *testing.T) {
	base := func() []Event {
		return []Event{
			{T: "file", ID: "A1", Text: "x"},
			{T: "checks", ID: "A1", Checks: "c.tsv", ChecksSHA: "h1"},
			{T: "claims", ID: "A1", Claims: "claims.md", ClaimsSHA: "hc"},
		}
	}
	withEvent := func(ev Event) error {
		_, _, err := derive(append(base(), ev))
		return err
	}
	// an amendment over the current list is accepted and becomes the current list
	_, byID, err := derive(append(base(), Event{T: "checks", ID: "A1", Checks: "c.tsv", ChecksSHA: "h2", PrevSHA: "h1"}))
	if err != nil {
		t.Fatalf("an amendment over the current list is corrupt: %v", err)
	}
	if byID["a1"].ChecksSHA != "h2" {
		t.Fatalf("the amendment must become the current list: %s", byID["a1"].ChecksSHA)
	}
	// a checks event with no previous hash after the claims is corrupt
	if err := withEvent(Event{T: "checks", ID: "A1", Checks: "c.tsv", ChecksSHA: "h2"}); err == nil || !strings.Contains(err.Error(), "after its claims") {
		t.Fatalf("a plain checks event after the claims must be corrupt: %v", err)
	}
	// an amendment over some other list is corrupt, after the claims or before them
	if err := withEvent(Event{T: "checks", ID: "A1", Checks: "c.tsv", ChecksSHA: "h2", PrevSHA: "other"}); err == nil || !strings.Contains(err.Error(), "amends list other") {
		t.Fatalf("an amendment over the wrong list must be corrupt: %v", err)
	}
	if _, _, err := derive(append(base()[:2], Event{T: "checks", ID: "A1", Checks: "c.tsv", ChecksSHA: "h2", PrevSHA: "other"})); err == nil {
		t.Fatal("an amendment over the wrong list must be corrupt before the claims too")
	}
	// a rebaseline after the claims stays corrupt
	if err := withEvent(Event{T: "rebaseline", ID: "A1", Checks: "c.tsv", ChecksSHA: "h2", PrevSHA: "h1"}); err == nil {
		t.Fatal("a rebaseline after the claims must be corrupt")
	}
}

// R25: the audit report is an artefact of the ledger. It lives under .backlog/audit/, nothing
// is written at the root, and an audit never changes the verified tree.
func TestAuditWritesUnderBacklogAndLeavesTheTreeAlone(t *testing.T) {
	dir := gitItem(t)
	before, err := verifiedTree(dir)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if err := call(t, cmdAudit, dir, "--since", "2d"); err != nil {
		t.Fatalf("audit: %v", err)
	}
	stamp := time.Now().UTC().Format("2006-01-02")
	for _, ext := range []string{".md", ".json"} {
		if _, err := os.Stat(filepath.Join(dir, ".backlog", "audit", stamp+ext)); err != nil {
			t.Errorf("report %s not under .backlog/audit/: %v", ext, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "audit")); !os.IsNotExist(err) {
		t.Errorf("an audit directory exists at the root of the working tree (err=%v)", err)
	}
	after, err := verifiedTree(dir)
	if err != nil {
		t.Fatalf("tree: %v", err)
	}
	if before != after {
		t.Errorf("the audit changed the verified tree: %s -> %s", before, after)
	}
	if err := call(t, cmdAudit, dir, "--due"); err != nil {
		t.Errorf("audit --due: %v", err)
	}
}
