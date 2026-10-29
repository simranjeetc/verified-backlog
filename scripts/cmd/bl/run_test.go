package main

import (
	"encoding/json"
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

// ── the run ──────────────────────────────────────────────────────────────────

// oneRowList is a well-formed list with one row: the smallest thing a run can be over.
const oneRowList = checksHeader + "S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tone row\n"

// runItem prepares an item with a frozen check list and frozen claims, ready to run.
func runItem(t *testing.T, list string) string {
	t.Helper()
	dir := openItem(t)
	legacyChecks(t, dir, list)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return dir
}

// readRunFile reads one run file and the bytes it holds.
func readRunFile(t *testing.T, dir, name string) (runFile, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "evidence", name))
	if err != nil {
		t.Fatalf("run file %s: %v", name, err)
	}
	var rf runFile
	if err := json.Unmarshal(data, &rf); err != nil {
		t.Fatalf("run file %s is not JSON: %v", name, err)
	}
	return rf, data
}

// ── the round path ───────────────────────────────────────────────────────────

// roundList is a well-formed list for the round path: a binary row and a judge row, both of
// which print a known word, so that an output anchor has something to point at. The binary
// row's expectation is `exit 0`, which its command meets, so the binary computes PASS for it
// (R7); the exit form is R8's and R37's alike, so the same list freezes as format 1 or 2.
const roundList = checksHeader +
	"S1\tbl\tblocking\tB1\techo two\texit 0\t\t\tprints two\n" +
	"J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n"

// roundFailList is roundList with a binary row whose expectation its command does NOT meet: the
// binary computes FAIL for it (R7), so a judge that writes PASS for it cannot make the round
// pass. `exit 1` is R8's and R37's alike, so the list freezes as format 1 or 2.
const roundFailList = checksHeader +
	"S1\tbl\tblocking\tB1\techo two\texit 1\t\t\tprints two\n" +
	"J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n"

// roundSwitchList is a list whose binary row passes or fails on demand: `test -f <marker>`
// exits 0 while the marker file is there and 1 while it is not, and the row expects `exit 1`,
// so the binary computes FAIL while the marker is there and PASS while it is not (R7). The
// marker sits outside the repository, so the tree does not move under the run, and the exit
// form and the single simple command are R8's and R37's alike, so the list freezes as either
// format.
func roundSwitchList(marker string) string {
	return checksHeader +
		"S1\tbl\tblocking\tB1\ttest -f " + marker + "\texit 1\t\t\tfails while the marker is there\n" +
		"J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n"
}

// switchItem prepares an item whose binary row passes or fails on demand, and returns the
// directory and the marker that decides it.
func switchItem(t *testing.T) (string, string) {
	t.Helper()
	marker := filepath.Join(t.TempDir(), "fail")
	return roundItem(t, roundSwitchList(marker)), marker
}

// markFailing puts the marker there, so the binary row's command meets no `exit 1` and the
// binary computes FAIL for it.
func markFailing(t *testing.T, marker string) {
	t.Helper()
	if err := os.WriteFile(marker, []byte("fail\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// markPassing takes the marker away, so the binary row's command exits 1 and the binary
// computes PASS for it.
func markPassing(t *testing.T, marker string) {
	t.Helper()
	if err := os.Remove(marker); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
}

// The verdicts of the round list: both rows pass, or the binary row fails. A judge row carries
// an output anchor, and the row's command prints the anchored word.
//
// The judge's word for a binary row is not that row's verdict (R7): the round takes the verdict
// the binary computed from the run. The two agree in these fixtures, so the verdicts file reads
// as the judge read the run.
var (
	roundPass = []string{"S1\tPASS\t", "J1\tPASS\t\"two\""}
	roundFail = []string{"S1\tFAIL\t", "J1\tPASS\t\"two\""}
)

// gitItem opens an item in a directory that is also a git repository. The round path needs
// one: the verified tree, the diff and the tree pin are git's.
func gitItem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
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

// roundItem prepares an item for a round: the list is frozen and the claims are recorded. The
// list is frozen as a format-1 list (legacyChecks) so the round fixtures keep their shell rows
// and R8 expectations; the format-2 round path is covered by its own tests.
func roundItem(t *testing.T, list string) string {
	t.Helper()
	dir := gitItem(t)
	legacyChecks(t, dir, list)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return dir
}

// latestRunID returns the id of the item's latest run.
func latestRunID(t *testing.T, dir string) string {
	t.Helper()
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := ""
	for _, ev := range events {
		if ev.T == "run" {
			id = ev.Run
		}
	}
	return id
}

// writeVerdicts builds a verdicts file (§2) for the item's latest run: the header, the `run` and
// the `tree` lines, and one line per frozen id.
func writeVerdicts(t *testing.T, dir string, rows ...string) string {
	t.Helper()
	tree, err := verifiedTreeWriting(dir)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("id\trow_verdict\tanchor\n")
	fmt.Fprintf(&b, "run %s\n", latestRunID(t, dir))
	fmt.Fprintf(&b, "tree %s\n", tree)
	for _, r := range rows {
		b.WriteString(r + "\n")
	}
	return writeFile(t, t.TempDir(), "verdicts.tsv", b.String())
}

// runAndVerdicts runs the frozen list and builds the verdicts file for that run. A round
// consumes its run, so a sequence of rounds needs a run before each.
func runAndVerdicts(t *testing.T, dir string, rows ...string) string {
	t.Helper()
	if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
		t.Fatalf("run: %v", err)
	}
	return writeVerdicts(t, dir, rows...)
}

// A run over a one-row list writes the run file and records the run event with the file's hash.
func TestRunWritesTheRunFileAndRecordsTheEvent(t *testing.T) {
	dir := runItem(t, oneRowList)
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, data := readRunFile(t, dir, "A1-run1.json")
	if rf.ID != "A1" || rf.Run != "run1" {
		t.Fatalf("run file = %+v, want item A1 and run run1", rf)
	}
	if rf.TreeStart == "" || rf.TreeStart != rf.TreeEnd {
		t.Fatalf("the trees must be present and equal: %+v", rf)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "run" || ev.ID != "A1" || ev.Run != "run1" {
		t.Fatalf("run event = %+v", ev)
	}
	if ev.RunSHA != shaHex(data) {
		t.Fatalf("the run event carries %s, want the file's hash %s", ev.RunSHA, shaHex(data))
	}
}

// The run file holds the row's cmd, its exit code, its output and the hash of that output.
func TestRunRecordsTheRowItRan(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo OUT; echo ERR >&2\texit 0\t\t\tstreams\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 1 {
		t.Fatalf("want one row, got %d", len(rf.Rows))
	}
	row := rf.Rows[0]
	if row.ID != "S1" || row.Cmd != "echo OUT; echo ERR >&2" {
		t.Errorf("row = %+v, want the row's id and cmd", row)
	}
	if row.Exit != 0 || row.TimedOut {
		t.Errorf("row = %+v, want exit 0 and no timeout", row)
	}
	if row.Stdout != "OUT\n" || row.Stderr != "ERR\n" {
		t.Errorf("row = %+v, want the full output of both streams", row)
	}
	if row.StdoutSHA != shaHex([]byte("OUT\n")) || row.StderrSHA != shaHex([]byte("ERR\n")) {
		t.Errorf("row = %+v, want the hash of each stream", row)
	}
}

func TestRunRecordsNonnegativeRowDuration(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\tsleep 1\texit 0\t\t\tsleeps\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 1 || rf.Rows[0].MS < 1000 {
		t.Fatalf("row duration = %+v, want at least 1000 ms", rf.Rows)
	}
}

func TestRunKeepsFrozenRowOrder(t *testing.T) {
	list := checksHeader +
		"S1\tbl\tblocking\tB1\tsleep 0.2; echo first\texit 0\t\t\tfirst\n" +
		"S2\tbl\tblocking\tB1\tsleep 0.1; echo second\texit 0\t\t\tsecond\n" +
		"S3\tbl\tblocking\tB1\techo third\texit 0\t\t\tthird\n"
	dir := runItem(t, list)
	if err := call(t, cmdRun, dir, "A1", "--jobs", "3"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	for i, want := range []string{"S1", "S2", "S3"} {
		if rf.Rows[i].ID != want {
			t.Errorf("row %d id = %q, want %q", i, rf.Rows[i].ID, want)
		}
	}
}

func TestRunJobsControlsSleepRows(t *testing.T) {
	list := checksHeader
	for i := 1; i <= 4; i++ {
		list += fmt.Sprintf("S%d\tbl\tblocking\tB1\tsleep 1\texit 0\t\t\tsleeps\n", i)
	}
	serial := runItem(t, list)
	started := time.Now()
	if err := call(t, cmdRun, serial, "A1", "--jobs", "1"); err != nil {
		t.Fatalf("serial run: %v", err)
	}
	serialDuration := time.Since(started)
	if serialDuration < 4*time.Second {
		t.Errorf("--jobs 1 took %s, want at least 4 seconds", serialDuration)
	}
	parallel := runItem(t, list)
	started = time.Now()
	if err := call(t, cmdRun, parallel, "A1", "--jobs", "4"); err != nil {
		t.Fatalf("parallel run: %v", err)
	}
	if elapsed := time.Since(started); elapsed >= 3*time.Second {
		t.Errorf("--jobs 4 took %s, want under 3 seconds", elapsed)
	}
}

func TestRunGivesEachRowItsOwnTMPDIR(t *testing.T) {
	list := checksHeader +
		"S1\tbl\tblocking\tB1\tmkdir \"$TMPDIR/x\" && printf '%s|%s' \"$TMPDIR\" \"$HOME\"\texit 0\t\t\tfirst\n" +
		"S2\tbl\tblocking\tB1\tmkdir \"$TMPDIR/x\" && printf '%s|%s' \"$TMPDIR\" \"$HOME\"\texit 0\t\t\tsecond\n"
	dir := runItem(t, list)
	if err := call(t, cmdRun, dir, "A1", "--jobs", "2"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	first := strings.Split(rf.Rows[0].Stdout, "|")
	second := strings.Split(rf.Rows[1].Stdout, "|")
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("row environments = %q and %q", rf.Rows[0].Stdout, rf.Rows[1].Stdout)
	}
	if first[0] == second[0] || first[1] != second[1] {
		t.Errorf("row environments = %q and %q, want distinct TMPDIR and shared HOME", first, second)
	}
}

func TestRunRejectsInvalidJobsBeforeWritingRun(t *testing.T) {
	for _, jobs := range []string{"0", "-1", "no", ""} {
		t.Run(fmt.Sprintf("jobs_%q", jobs), func(t *testing.T) {
			dir := runItem(t, oneRowList)
			code := run([]string{"--root", dir, "run", "A1", "--jobs=" + jobs})
			if code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
				t.Error("invalid --jobs must write no run file")
			}
		})
	}
}

func TestRunJobsStillRefusesTreeChange(t *testing.T) {
	list := checksHeader +
		"S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\tfirst\n" +
		"S2\tbl\tblocking\tB1\techo changed >> changed.txt\texit 0\t\t\tchanges tree\n" +
		"S3\tbl\tblocking\tB1\ttrue\texit 0\t\t\tlast\n"
	dir := runItem(t, list)
	wantRule(t, call(t, cmdRun, dir, "A1", "--jobs", "4"), "R26")
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
		t.Error("R26 refusal must write no run file")
	}
}

func TestRunWithoutMSStillRoundsShowsChecksAndCloses(t *testing.T) {
	dir := gitItemWith(t, map[string]string{".gitignore": backlogDir + "/\n"})
	legacyChecks(t, dir, roundList)
	claimsOK(t, dir)
	rows := runAndVerdicts(t, dir, roundPass...)
	path := filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")
	var run map[string]any
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &run); err != nil {
		t.Fatal(err)
	}
	for _, row := range run["rows"].([]any) {
		delete(row.(map[string]any), "ms")
	}
	data, err = json.MarshalIndent(run, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	ledgerPath := filepath.Join(dir, ".backlog", "ledger.jsonl")
	ledger, err := os.ReadFile(ledgerPath)
	if err != nil {
		t.Fatal(err)
	}
	var events []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(ledger)), "\n") {
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatal(err)
		}
		if ev["t"] == "run" {
			ev["run_sha"] = shaHex(append(data, '\n'))
		}
		events = append(events, ev)
	}
	var ledgerOut strings.Builder
	for _, ev := range events {
		line, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		ledgerOut.Write(line)
		ledgerOut.WriteByte('\n')
	}
	if err := os.WriteFile(ledgerPath, []byte(ledgerOut.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdShow, dir, "A1"); err != nil {
		t.Fatalf("show: %v", err)
	}
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
	closeItem(t, dir)
}

// R7: the binary computes a binary row's row-verdict from the row's expectation and the values
// the run observed, and records it with them. The exit code is not the verdict: a row whose
// command exits 2 because the binary refused is a PASS row when its expectation matches what
// the refusal printed, and the run exits 0.
func TestRunComputesPassForARowThatRefusedAndMatchesItsExpectation(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo refused >&2; exit 2\tstderr matches refused\t\t\trefuses\n")
	if code := run([]string{"--root", dir, "run", "A1"}); code != 0 {
		t.Fatalf("bl run: exit = %d, want 0 — the row's expectation matched", code)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 1 {
		t.Fatalf("want one row, got %d", len(rf.Rows))
	}
	row := rf.Rows[0]
	if row.Exit != 2 {
		t.Errorf("exit = %d, want 2 — the command refused", row.Exit)
	}
	if row.Verdict != "PASS" {
		t.Errorf("verdict = %q, want PASS — the expectation matches what the refusal printed", row.Verdict)
	}
}

// R7: a binary row whose observed values do not match its expectation is a FAIL row, even when
// its command exits 0, and the run exits non-zero.
func TestRunComputesFailForARowThatDoesNotMatchItsExpectation(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo ok\tstdout matches ^never$\t\t\tdoes not match\n")
	if code := run([]string{"--root", dir, "run", "A1"}); code == 0 {
		t.Fatal("bl run: exit = 0, want non-zero — the row failed")
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	row := rf.Rows[0]
	if row.Exit != 0 {
		t.Errorf("exit = %d, want 0 — the command succeeded", row.Exit)
	}
	if row.Verdict != "FAIL" {
		t.Errorf("verdict = %q, want FAIL — the output does not match the expectation", row.Verdict)
	}
}

// R7 and R8: the row-verdict is computed from the expectation, form by form, against the values
// the run observed. The exit code is never the verdict on its own.
func TestTheRowVerdictIsComputedFormByForm(t *testing.T) {
	cases := []struct {
		name   string
		cmd    string
		expect string
		want   string
	}{
		{"the exit matches", "exit 3", "exit 3", "PASS"},
		{"the exit does not match", "exit 3", "exit 0", "FAIL"},
		{"the stdout matches", "echo hello", "stdout matches (?m)^hello$", "PASS"},
		{"the stdout does not match", "echo hello", "stdout matches ^never$", "FAIL"},
		{"the stderr matches", "echo oops >&2", "stderr matches oops", "PASS"},
		{"the stderr does not match", "echo oops >&2", "stderr matches never", "FAIL"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\t"+c.cmd+"\t"+c.expect+"\t\t\tone row\n")
			if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
				t.Fatalf("run: %v", err)
			}
			rf, _ := readRunFile(t, dir, "A1-run1.json")
			if got := rf.Rows[0].Verdict; got != c.want {
				t.Errorf("verdict = %q, want %q", got, c.want)
			}
		})
	}
}

func TestMatchStreamIgnoresExactlyOneTrailingNewline(t *testing.T) {
	cases := []struct {
		pattern, input string
		want           bool
	}{
		{"^gofmt-clean$", "gofmt-clean\n", true}, {"^a$", "a\n\n", false},
		{"^a$", "a", true}, {"^oops$", "oops\n", true},
	}
	for _, c := range cases {
		re := regexp.MustCompile(c.pattern)
		if got := matchStream(re, c.input); got != c.want {
			t.Errorf("matchStream(%q) = %v, want %v", c.input, got, c.want)
		}
	}
}

// R8: `file exists` is true when the path is there when the row runs, and `tree ==` is true
// when the tree the run verified is the one the row names.
func TestTheRowVerdictIsComputedForTheFileAndTheTreeForms(t *testing.T) {
	t.Run("the file is there", func(t *testing.T) {
		dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\ttrue\tfile exists there.txt\t\t\tone row\n")
		writeFile(t, dir, "there.txt", "x\n")
		if err := call(t, cmdRun, dir, "A1"); err != nil {
			t.Fatalf("run: %v", err)
		}
		rf, _ := readRunFile(t, dir, "A1-run1.json")
		if got := rf.Rows[0].Verdict; got != "PASS" {
			t.Errorf("verdict = %q, want PASS — the file is there", got)
		}
	})
	t.Run("the file is not there", func(t *testing.T) {
		dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\ttrue\tfile exists nothere.txt\t\t\tone row\n")
		if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
			t.Fatalf("run: %v", err)
		}
		rf, _ := readRunFile(t, dir, "A1-run1.json")
		if got := rf.Rows[0].Verdict; got != "FAIL" {
			t.Errorf("verdict = %q, want FAIL — the file is not there", got)
		}
	})
	t.Run("the tree matches", func(t *testing.T) {
		dir := openItem(t)
		tree, err := verifiedTreeWriting(dir)
		if err != nil {
			t.Fatal(err)
		}
		legacyChecks(t, dir, checksHeader+"S1\tbl\tblocking\tB1\ttrue\ttree == "+tree+"\t\t\tone row\n")
		if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
			t.Fatalf("claims: %v", err)
		}
		if err := call(t, cmdRun, dir, "A1"); err != nil {
			t.Fatalf("run: %v", err)
		}
		rf, _ := readRunFile(t, dir, "A1-run1.json")
		if got := rf.Rows[0].Verdict; got != "PASS" {
			t.Errorf("verdict = %q, want PASS — the run verified the tree the row names", got)
		}
	})
	t.Run("the tree does not match", func(t *testing.T) {
		dir := openItem(t)
		legacyChecks(t, dir, checksHeader+"S1\tbl\tblocking\tB1\ttrue\ttree == 0000000000000000000000000000000000000000\t\t\tone row\n")
		if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
			t.Fatalf("claims: %v", err)
		}
		if err := call(t, cmdRun, dir, "A1"); err != nil && !errors.Is(err, errFailed) {
			t.Fatalf("run: %v", err)
		}
		rf, _ := readRunFile(t, dir, "A1-run1.json")
		if got := rf.Rows[0].Verdict; got != "FAIL" {
			t.Errorf("verdict = %q, want FAIL — the run verified another tree", got)
		}
	})
}

// A row that exits non-zero is recorded as a failure, with its code and its output, and the
// run file still exists. The next row still runs.
func TestRunRecordsAFailingRowAndStillWritesTheFile(t *testing.T) {
	dir := runItem(t, checksHeader+
		"S1\tbl\tblocking\tB1\techo BADOUT >&2; exit 211\texit 0\t\t\tfails\n"+
		"S2\tbl\tblocking\tB1\techo AFTER_FAIL\texit 0\t\t\tstill runs\n")
	err := call(t, cmdRun, dir, "A1")
	if !errors.Is(err, errFailed) {
		t.Fatalf("a failing row must be reported: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 2 {
		t.Fatalf("want two rows, got %d", len(rf.Rows))
	}
	if rf.Rows[0].Exit != 211 {
		t.Errorf("exit = %d, want 211", rf.Rows[0].Exit)
	}
	if !strings.Contains(rf.Rows[0].Stderr, "BADOUT") {
		t.Errorf("the failing row's output must be recorded: %q", rf.Rows[0].Stderr)
	}
	if rf.Rows[1].Exit != 0 || !strings.Contains(rf.Rows[1].Stdout, "AFTER_FAIL") {
		t.Errorf("the next row must still run: %+v", rf.Rows[1])
	}
	// and the failure was recorded in the ledger too
	events, _ := readLedger(dir)
	if ev := events[len(events)-1]; ev.T != "run" {
		t.Fatalf("the run event must be recorded even when a row failed: %+v", ev)
	}
}

// A command that cannot be run at all is recorded as 127, and a death by a signal as
// 128+signal — the two are distinguishable from a command that exits with that code itself.
func TestRunRecordsAnUnrunnableCommandAndASignalDeath(t *testing.T) {
	dir := runItem(t, checksHeader+
		"S1\tbl\tblocking\tB1\t/nonexistent/bin_xyz\texit 0\t\t\tunrunnable\n"+
		"S2\tbl\tblocking\tB1\tkill -KILL $$\texit 0\t\t\tsignal death\n")
	err := call(t, cmdRun, dir, "A1")
	if !errors.Is(err, errFailed) {
		t.Fatalf("the failures must be reported: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if rf.Rows[0].Exit != 127 {
		t.Errorf("exit = %d, want 127 for a command that cannot run", rf.Rows[0].Exit)
	}
	if rf.Rows[0].Signalled {
		t.Errorf("a command that cannot run is not a signal death: %+v", rf.Rows[0])
	}
	if rf.Rows[1].Exit != 137 {
		t.Errorf("exit = %d, want 128+9 for a death by SIGKILL", rf.Rows[1].Exit)
	}
	if !rf.Rows[1].Signalled {
		t.Errorf("a death by a signal must say so: %+v", rf.Rows[1])
	}
}

// A row that hangs is killed with its process group and recorded as a timeout: exit = -1,
// which is distinct from any command's own exit code, so a timeout can never be written up as
// a pass.
func TestRunRecordsATimeoutAsATimeout(t *testing.T) {
	old := runTimeout
	runTimeout = 300 * time.Millisecond
	defer func() { runTimeout = old }()
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\tsleep 30\texit 0\t\t\thangs\n")
	err := call(t, cmdRun, dir, "A1")
	if !errors.Is(err, errFailed) {
		t.Fatalf("a timeout must be reported: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if !rf.Rows[0].TimedOut {
		t.Fatalf("the row must be recorded as timed out: %+v", rf.Rows[0])
	}
	if rf.Rows[0].Exit != -1 {
		t.Errorf("exit = %d, want -1 for a timeout", rf.Rows[0].Exit)
	}
}

// Every row runs, in the order of the frozen list, whatever the decider.
func TestRunRunsEveryRowInFileOrder(t *testing.T) {
	dir := runItem(t, checksHeader+
		"S1\tbl\tblocking\tB1\techo a\texit 0\t\t\tfirst\n"+
		"J1\tjudge\tadvisory\t\techo b\t\tfail if b is wrong\treading needed not a match\tjudge row\n"+
		"S2\tbl\tblocking\tB1\techo c\texit 0\t\t\tthird\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 3 {
		t.Fatalf("want three rows, got %d", len(rf.Rows))
	}
	for i, want := range []string{"S1", "J1", "S2"} {
		if rf.Rows[i].ID != want || strings.TrimSpace(rf.Rows[i].Stdout) != string(rune('a'+i)) {
			t.Errorf("row %d = %+v, want %s and its output", i, rf.Rows[i], want)
		}
	}
}

// The environment is fixed: HOME and TMPDIR sit outside the repository and are removed after the
// run, PATH is inherited and recorded, LANG=C and TZ=UTC.
func TestRunRecordsTheFixedEnvironment(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\tenv\texit 0\t\t\tenvironment probe\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	home, tmpdir := rf.Env["HOME"], rf.Env["TMPDIR"]
	if home == "" || tmpdir == "" {
		t.Fatalf("the environment must record HOME and TMPDIR: %+v", rf.Env)
	}
	if inside(dir, home) || inside(dir, tmpdir) {
		t.Fatalf("HOME and TMPDIR must sit outside the repository: %s %s", home, tmpdir)
	}
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("the scratch directory must be removed after the run: %s", home)
	}
	if rf.Env["PATH"] != os.Getenv("PATH") {
		t.Errorf("PATH must be inherited and recorded: %q", rf.Env["PATH"])
	}
	if rf.Env["LANG"] != "C" || rf.Env["TZ"] != "UTC" {
		t.Errorf("LANG must be C and TZ must be UTC: %+v", rf.Env)
	}
}

// No variable of the caller's reaches the rows — in particular no GIT_*.
func TestRunGivesTheRowsNoCallerVariable(t *testing.T) {
	t.Setenv("GIT_AUTHOR_NAME", "leak")
	t.Setenv("LEAK_ME", "1")
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\tenv\texit 0\t\t\tenvironment probe\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	out := rf.Rows[0].Stdout
	if strings.Contains(out, "GIT_") {
		t.Errorf("a GIT_* variable reached the row: %q", out)
	}
	if strings.Contains(out, "LEAK_ME") {
		t.Errorf("a variable of the caller's reached the row: %q", out)
	}
	if !strings.Contains(out, "LANG=C") || !strings.Contains(out, "TZ=UTC") {
		t.Errorf("the fixed environment did not reach the row: %q", out)
	}
}

// R26: a row that changes a file moves the tree, so the run is refused — with no run file and
// no ledger event.
func TestRunRefusesWhenTheTreeMoves(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo more >> f.txt\texit 0\t\t\tmoves the tree\n")
	writeFile(t, dir, "f.txt", "hi\n")
	before, _ := readLedger(dir)
	wantRule(t, call(t, cmdRun, dir, "A1"), "R26")
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
		t.Error("a refused run must write no run file")
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Errorf("a refused run must write no ledger event: %d events, was %d", len(after), len(before))
	}
}

// A timestamp-only change is not a change of content, so it does not trip R26.
func TestRunATouchDoesNotMoveTheTree(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\ttouch f.txt\texit 0\t\t\ttouches a timestamp\n")
	writeFile(t, dir, "f.txt", "hi\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// R27 excludes run.log because P10 defines it as a log of calls, not state.
func TestRunAllowsARowThatWritesOnlyRunlog(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo x >> .backlog/run.log\texit 0\t\t\twrites the call log\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
}

// R27: a row that writes the ledger is refused — with no run file and no ledger event.
func TestRunRefusesARowThatWritesLedger(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\tprintf '\\n' >> .backlog/ledger.jsonl\texit 0\t\t\twrites the ledger\n")
	before, _ := readLedger(dir)
	wantRule(t, call(t, cmdRun, dir, "A1"), "R27")
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
		t.Error("a refused run must write no run file")
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Errorf("a refused run must write no ledger event: %d events, was %d", len(after), len(before))
	}
}

// R20: a frozen artefact edited after the freeze is refused at the point of use, and no run
// file is written.
func TestRunRefusesAnEditedArtefact(t *testing.T) {
	for _, what := range []string{"brief.md", "checks.tsv", "claims.md"} {
		t.Run(what, func(t *testing.T) {
			dir := runItem(t, oneRowList)
			stored := filepath.Join(dir, ".backlog", "items", "A1", what)
			data, err := os.ReadFile(stored)
			if err != nil {
				t.Fatalf("%s: %v", what, err)
			}
			if err := os.WriteFile(stored, append(data, []byte("edited\n")...), 0o644); err != nil {
				t.Fatal(err)
			}
			wantRule(t, call(t, cmdRun, dir, "A1"), "R20")
			if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
				t.Error("a refused run must write no run file")
			}
		})
	}
}

// The second run of an item is run2, and its file is A1-run2.json.
func TestRunNumbersTheRunsOfAnItem(t *testing.T) {
	dir := runItem(t, oneRowList)
	for n := 1; n <= 2; n++ {
		if err := call(t, cmdRun, dir, "A1"); err != nil {
			t.Fatalf("run %d: %v", n, err)
		}
		name := fmt.Sprintf("A1-run%d.json", n)
		rf, _ := readRunFile(t, dir, name)
		if rf.Run != fmt.Sprintf("run%d", n) {
			t.Errorf("%s holds the run id %q", name, rf.Run)
		}
	}
}

// The run file is a frozen artefact: `bl check` re-hashes it, and a tampered one is a FAIL.
func TestCheckRehashesTheRunFile(t *testing.T) {
	dir := runItem(t, oneRowList)
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
	path := filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte("tampered\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	err = call(t, cmdCheck, dir)
	if !errors.Is(err, errFailed) {
		t.Fatalf("a tampered run file must fail the check: %v", err)
	}
}

// ── R35: only the item in flight is worked on ────────────────────────────────

// R35: `bl run` refuses an item that is not the item in flight. The refusal names the rule,
// the call exits 2, and nothing is written: no run file and no ledger event.
func TestRunRefusesAnItemThatIsNotInFlight(t *testing.T) {
	dir := runItem(t, oneRowList)
	// The item is dropped, so it is no longer the item in flight.
	if code := run([]string{"--root", dir, "drop", "A1", "--reason", "not now"}); code != 0 {
		t.Fatalf("bl drop: exit %d", code)
	}
	before, err := os.ReadFile(filepath.Join(dir, ".backlog", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	wantRule(t, call(t, cmdRun, dir, "A1"), "R35")
	var code int
	out := captureStderr(t, func() {
		code = run([]string{"--root", dir, "run", "A1"})
	})
	if code != 2 {
		t.Fatalf("bl run: exit = %d, want 2", code)
	}
	want := "bl: refused (R35): A1 is not the item in flight — run `bl open A1`"
	if !strings.Contains(out, want) {
		t.Errorf("the refusal must read %q:\n%s", want, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); !os.IsNotExist(err) {
		t.Error("a refused run must write no run file")
	}
	after, err := os.ReadFile(filepath.Join(dir, ".backlog", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Error("a refused run must leave the ledger byte-identical")
	}
}

// R35 is the FIRST check on the run path: an item that was never opened, and so has no check
// list at all, is refused under R35 and not under the missing-list rule.
func TestRunNamesR35BeforeTheMissingList(t *testing.T) {
	dir := openItem(t) // A1 is the item in flight.
	if code := run([]string{"--root", dir, "file", "--id", "A2", "--text", "second"}); code != 0 {
		t.Fatalf("bl file: exit %d", code)
	}
	r := wantRefusal(t, call(t, cmdRun, dir, "A2"))
	if r.rule != "R35" {
		t.Fatalf("want a refusal under R35, got %s: %s", r.rule, r.msg)
	}
	if strings.Contains(r.msg, "check list") {
		t.Errorf("the R35 refusal must not name the missing-list rule: %s", r.msg)
	}
}

// R35 does not over-refuse: the item that IS in flight still runs, and its run file is written.
func TestRunForTheItemInFlightStillSucceeds(t *testing.T) {
	dir := runItem(t, oneRowList)
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")); err != nil {
		t.Fatalf("the run file must be written: %v", err)
	}
}

// The round path shares the helper with `bl run`, so its refusal names R35 too.
func TestRoundRefusalNamesR35(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"file", "--id", "A1", "--text", "first"},
		{"file", "--id", "A2", "--text", "second"},
		{"open", "A1"},
	} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	rows := writeFile(t, t.TempDir(), "v.tsv", "id\trow_verdict\tanchor\n")
	r := wantRefusal(t, call(t, cmdRound, dir, "A2", "--verdict", "PASS", "--rows", rows))
	if r.rule != "R35" {
		t.Fatalf("want a refusal under R35, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "is not the item in flight") {
		t.Errorf("the refusal must say which item is not in flight: %s", r.msg)
	}
}

// The design's rule R35 covers `bl run` and `bl round` only. The other paths that hold the lock
// — brief, checks, claims, rebaseline and second-check — refuse without naming a rule, so the
// refusal each of those verbs can raise stays as the design has it.
func TestTheOtherLockHoldingPathsRefuseWithoutNamingARule(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"file", "--id", "A1", "--text", "first"},
	} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: it works. Example: prints ok.\n")
	list := writeFile(t, t.TempDir(), "checks.tsv", checksHeader+"S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\tok\n")
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	cases := []struct {
		name string
		h    func(string, args) error
		verb string
		argv []string
	}{
		{"brief", cmdBrief, "brief", []string{"A1", "--file", brief}},
		{"checks", cmdChecks, "checks", []string{"A1", "--file", list}},
		{"claims", cmdClaims, "claims", []string{"A1", "--file", claims}},
		{"rebaseline", cmdRebaseline, "rebaseline", []string{"A1", "--file", list, "--reason", "again"}},
		{"second-check", cmdSecondCheck, "second-check", []string{"A1", "--row", "S1", "--cmd", "echo two", "--expect", "stdout matches ^two"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// The handler refuses with no rule named.
			r := wantRefusal(t, call(t, c.h, dir, c.argv...))
			if r.rule != "" {
				t.Fatalf("the %s refusal must name no rule, got %s: %s", c.name, r.rule, r.msg)
			}
			if strings.Contains(r.msg, "R35") {
				t.Errorf("the %s refusal must not name R35: %s", c.name, r.msg)
			}
			if !strings.Contains(r.msg, "is not the item in flight") {
				t.Errorf("the %s refusal must say what is wrong: %s", c.name, r.msg)
			}
			// And the binary prints it without a rule, in the house style.
			var code int
			out := captureStderr(t, func() {
				code = run(append([]string{"--root", dir, c.verb}, c.argv...))
			})
			if code != 2 {
				t.Fatalf("bl %s: exit = %d, want 2", c.name, code)
			}
			want := "bl: refused: A1 is not the item in flight — run `bl open A1`"
			if !strings.Contains(out, want) {
				t.Errorf("the %s refusal must read %q:\n%s", c.name, want, out)
			}
			if strings.Contains(out, "R35") {
				t.Errorf("the %s refusal must not name R35:\n%s", c.name, out)
			}
		})
	}
}
