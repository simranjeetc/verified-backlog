package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// R36: a restart ends the attempt and starts the next one. It prints the line the brief names,
// `bl show` prints the attempt immediately after the status, `bl show --json` carries it as an
// integer, and the item leaves flight with everything the attempt recorded cleared — so the
// next `bl open` is accepted, and the new attempt's list lands in a2/.
func TestRestartEndsTheAttemptAndStartsTheNext(t *testing.T) {
	dir := restartItem(t)
	// B2's example: the item is still in flight, so a re-open is refused first
	if err := call(t, cmdOpen, dir, "A1"); err == nil || !strings.Contains(err.Error(), "already in flight") {
		t.Fatalf("a re-open of the item in flight must be refused as already in flight, got %v", err)
	}
	var err error
	out := capture(t, func() {
		err = call(t, cmdRestart, dir, "A1", "--reason", "bad-check",
			"--why", "row S1 expects ^never and cannot match", "--change", "the pattern allows a trailing newline")
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	if out != "bl: A1 restarted as attempt 2\n" {
		t.Fatalf("restart printed %q", out)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if ev.T != "restart" || ev.Reason != "bad-check" || ev.By != "" {
		t.Fatalf("restart event = %+v", ev)
	}
	if ev.Attempt == nil || *ev.Attempt != 1 || ev.Why == "" || ev.Change == "" {
		t.Fatalf("restart event = %+v", ev)
	}
	if ev.Record != ".backlog/items/A1/a1-failure.md" {
		t.Fatalf("the record path is %q", ev.Record)
	}
	record, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(ev.Record)))
	if err != nil || shaHex(record) != ev.RecordSHA {
		t.Fatalf("the record does not match the hash the event records: %v", err)
	}
	// `bl show` prints the attempt immediately after the status, and nothing about an actor
	shown := showText(t, dir)
	lines := strings.Split(shown, "\n")
	if !strings.HasPrefix(lines[1], "status: open") {
		t.Fatalf("show: %q", shown)
	}
	if lines[2] != "attempt: 2" {
		t.Fatalf("the attempt must follow the status line:\n%s", shown)
	}
	// `bl show --json` carries it as an integer
	var jsonErr error
	js := capture(t, func() { jsonErr = call(t, cmdShow, dir, "A1", "--json") })
	if jsonErr != nil {
		t.Fatalf("show --json: %v", jsonErr)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(js), &item); err != nil {
		t.Fatalf("show --json: %v", err)
	}
	if item["Attempt"] != float64(2) {
		t.Fatalf("show --json Attempt = %v", item["Attempt"])
	}
	// the item keeps its own record
	if !strings.Contains(shown, "A1 — add a verb; test it") {
		t.Fatalf("the restart lost the item's text:\n%s", shown)
	}
	// it is out of flight, and `bl next` can name it again
	if got := nextText(t, dir); !strings.HasPrefix(got, "A1\t") {
		t.Fatalf("bl next = %q, want A1", got)
	}
	// the new attempt opens, freezes its list and claims in a2/
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open after the restart: %v", err)
	}
	checksOK(t, dir, roundFailList)
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "a2", "checks.tsv")); err != nil {
		t.Fatalf("the new attempt's list is not in a2/: %v", err)
	}
}

func scopedRestartItem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}
	for _, argv := range [][]string{{"init"}, {"file", "--id", "A1", "--text", "add a verb; test it"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	if err := call(t, cmdOpen, dir, "A1", "--scope", "src/", "--scope", "README.md", "--scope-ignored", "build/out.bin"); err != nil {
		t.Fatal(err)
	}
	legacyChecks(t, dir, roundFailList)
	claimsOK(t, dir)
	failRound(t, dir, roundFail...)
	restartOK(t, dir, "bad check", "change check")
	return dir
}

func TestRestartCarriesScopeAndOpenUsesIt(t *testing.T) {
	dir := scopedRestartItem(t)
	var err error
	out := capture(t, func() { err = call(t, cmdOpen, dir, "A1") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "bl: A1 scope carried from attempt 1: src/, README.md\nbl: A1 in flight\n" {
		t.Fatalf("open printed %q", out)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if !reflect.DeepEqual(ev.Scope, []string{"src/", "README.md"}) || !reflect.DeepEqual(ev.ScopeIgnored, []string{"build/out.bin"}) {
		t.Fatalf("open scope = %#v / %#v", ev.Scope, ev.ScopeIgnored)
	}
}

func TestOpenExplicitScopeReplacesCarriedScope(t *testing.T) {
	dir := scopedRestartItem(t)
	var err error
	out := capture(t, func() { err = call(t, cmdOpen, dir, "A1", "--scope", "lib/") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "bl: A1 in flight\n" {
		t.Fatalf("open printed %q", out)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if !reflect.DeepEqual(ev.Scope, []string{"lib/"}) || len(ev.ScopeIgnored) != 0 {
		t.Fatalf("open scope = %#v / %#v", ev.Scope, ev.ScopeIgnored)
	}
}

func TestRestartWithoutScopeDoesNotCarry(t *testing.T) {
	dir := restartItem(t)
	restartOK(t, dir, "bad check", "change check")
	var err error
	out := capture(t, func() { err = call(t, cmdOpen, dir, "A1") })
	if err != nil {
		t.Fatal(err)
	}
	if out != "bl: A1 in flight\n" {
		t.Fatalf("open printed %q", out)
	}
}

// R36: the rounds, runs and second checks of the attempt a restart ended are void. They stay in
// the history and keep their numbers, but they spend none of the new attempt's budget and the
// new attempt's first round continues the numbering.
func TestRestartVoidsTheRoundsAndRunsOfTheAttemptItEnds(t *testing.T) {
	dir := roundItem(t, roundFailList)
	for i := 0; i < roundCap; i++ {
		failRound(t, dir, roundFail...)
	}
	rows := runAndVerdicts(t, dir, roundFail...)
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
	restartOK(t, dir, "the list's own row cannot pass", "attempt 2 freezes a new list")
	shown := showText(t, dir)
	for i := 1; i <= roundCap; i++ {
		want := fmt.Sprintf("r%d FAIL cause=code .backlog/evidence/A1-r%d.txt void", i, i)
		if !strings.Contains(shown, want) {
			t.Errorf("bl show must mark r%d void:\n%s", i, shown)
		}
	}
	if !strings.Contains(shown, "rounds: 0 (code 0, harness 0, dispute 0, pass 0)") {
		t.Errorf("the counts must be the current attempt's:\n%s", shown)
	}
	// the new attempt's first round is r4, and the cap starts again at zero
	nextAttempt(t, dir, roundFailList)
	// a round in attempt 2 cannot cite attempt 1's run
	rows = writeVerdicts(t, dir, roundFail...)
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R6")
	failRound(t, dir, roundFail...)
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "evidence", "A1-r4.txt")); err != nil {
		t.Errorf("attempt 2's first round must be r4: %v", err)
	}
	if got := showText(t, dir); !strings.Contains(got, "rounds: 1 (code 1, harness 0, dispute 0, pass 0)") {
		t.Errorf("attempt 2's counts must start at zero:\n%s", got)
	}
}

// R36: a void PASS round cannot let `bl close` succeed.
func TestAVoidPassRoundCannotCloseTheItem(t *testing.T) {
	dir, marker := switchItem(t)
	markFailing(t, marker)
	failRound(t, dir, roundFail...)
	markPassing(t, marker)
	passRound(t, dir)
	restartOK(t, dir, "the list's own row is the wrong one", "a narrower list")
	nextAttempt(t, dir, roundSwitchList(marker))
	err := call(t, cmdClose, dir, "A1", "--commit", "HEAD")
	if err == nil || !strings.Contains(err.Error(), "no PASS round") {
		t.Fatalf("a void PASS round must not close the item: %v", err)
	}
}

// R36: the failure record is exactly the lines the brief names, and `bl check` reports it once it
// is edited.
func TestRestartWritesTheFailureRecordOfTheAttemptItEnds(t *testing.T) {
	const twoFailRows = checksHeader +
		"R3\tbl\tblocking\tB1\tprintf ok; exit 1\tstdout matches ^nope\t\t\tprints ok and fails\n" +
		"R7\tbl\tblocking\tB1\texit 2\tstdout matches ^nope\t\t\tsays nothing and fails\n"
	dir := roundItem(t, twoFailRows)
	failRound(t, dir, "R3\tFAIL\t", "R7\tFAIL\t")
	restartOK(t, dir, "both rows expect the wrong output", "the rows expect what the code prints")
	path := filepath.Join(dir, ".backlog", "items", "A1", "a1-failure.md")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the record: %v", err)
	}
	want := "# Failure record: A1 attempt 1\n" +
		"class: bad-check\n" +
		"why: both rows expect the wrong output\n" +
		"change: the rows expect what the code prints\n" +
		"rows:\n" +
		"- R3 exit 1\n  | ok\n- R7 exit 2\n" +
		"artefacts:\n- (none)\n"
	if string(data) != want {
		t.Fatalf("the record is\n%s\nwant\n%s", data, want)
	}
	// one byte more, and `bl check` names the record
	if err := os.WriteFile(path, append(data, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	var checkErr error
	out := capture(t, func() { checkErr = call(t, cmdCheck, dir) })
	if !errors.Is(checkErr, errFailed) {
		t.Fatalf("an edited record must fail bl check: %v", checkErr)
	}
	if !strings.Contains(out, "A1 failure record .backlog/items/A1/a1-failure.md does not match its hash") {
		t.Fatalf("bl check must name the record:\n%s", out)
	}
}

// R36: a row's output is split on LF with one trailing empty segment dropped, a binary stream is
// one line, and at most 20 lines are kept per row with the 21st saying they were cut.
func TestTheFailureRecordsRowsAreCappedAt20Lines(t *testing.T) {
	long := strings.Repeat("line\n", 25)
	rec := runRow{Stdout: long}
	lines := outputLines(rec)
	if len(lines) != 21 {
		t.Fatalf("want 20 lines and the truncation line, got %d", len(lines))
	}
	if lines[20] != "... (truncated)" {
		t.Errorf("the 21st line is %q", lines[20])
	}
	if got := outputLines(runRow{Stdout: "ok\n"}); len(got) != 1 || got[0] != "ok" {
		t.Errorf("`ok\\n` is one line, got %v", got)
	}
	if got := outputLines(runRow{Stdout: "a", Stderr: "b\nc\n"}); len(got) != 3 || got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("stdout comes first, then stderr: %v", got)
	}
	if got := outputLines(runRow{StdoutBinary: true, StderrBinary: true}); len(got) != 2 || got[0] != "(binary output)" {
		t.Errorf("a binary stream is one line: %v", got)
	}
	if got := outputLines(runRow{}); len(got) != 0 {
		t.Errorf("no output is no line: %v", got)
	}
}

// R36: attempt n's brief is the item's brief followed by the failure record of every earlier
// attempt, oldest first — from `bl open`, and from `bl brief`, which adds the missing newline to
// the submitted file first.
func TestRestartBriefCarriesTheRecordsOfEveryEarlierAttempt(t *testing.T) {
	dir := restartItem(t)
	restartOK(t, dir, "first attempt", "first change")
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	first, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a1-failure.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief2, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a2", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(brief2), string(first)) {
		t.Fatalf("a2's brief must end with a1's record:\n%s", brief2)
	}
	// `bl brief` stores the submitted text followed by the records, adding the missing newline
	submitted := "B1: add a verb. Example: adds a verb.\nB2: test it, narrowly. Example: runs the focused test.\nB0: purpose. Example: x."
	if err := call(t, cmdBrief, dir, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", submitted)); err != nil {
		t.Fatalf("brief: %v", err)
	}
	stored, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a2", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if want := submitted + "\n" + string(first); string(stored) != want {
		t.Fatalf("the stored brief is\n%s\nwant\n%s", stored, want)
	}
	// a second attempt, then a third: a3's brief ends with a1's record then a2's
	checksOK(t, dir, roundFailList)
	claimsOK(t, dir)
	failRound(t, dir, roundFail...)
	restartOK(t, dir, "second attempt", "second change")
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	second, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a2-failure.md"))
	if err != nil {
		t.Fatal(err)
	}
	brief3, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a3", "brief.md"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(brief3), string(first)+string(second)) {
		t.Fatalf("a3's brief must end with a1's record then a2's:\n%s", brief3)
	}
}

// R36: no file of an earlier attempt changes because of a later one.
func TestRestartLeavesEveryEarlierAttemptsFileAlone(t *testing.T) {
	dir := restartItem(t)
	record := restartOK(t, dir, "the list is the wrong premise", "a new list").Record
	before := map[string]string{}
	for _, rel := range []string{
		".backlog/items/A1/brief.md", ".backlog/items/A1/checks.tsv", ".backlog/items/A1/claims.md",
		".backlog/evidence/A1-run1.json", ".backlog/evidence/A1-r1.txt", record,
	} {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		before[rel] = shaHex(data)
	}
	// attempt 2 opens, briefs, freezes, claims, runs and rounds
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := call(t, cmdBrief, dir, "A1", "--file", writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB2: test it narrowly. Example: runs the focused test.\nB0: purpose. Example: x.\n")); err != nil {
		t.Fatalf("brief: %v", err)
	}
	checksOK(t, dir, roundFailList)
	claimsOK(t, dir)
	failRound(t, dir, roundFail...)
	for rel, want := range before {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if got := shaHex(data); got != want {
			t.Errorf("%s changed under attempt 2: %s, was %s", rel, got, want)
		}
	}
}

// R36: `bl check` excuses the damage a failure record lists, and only that damage. The I3 case:
// an item closed on a PASS round whose evidence was overwritten is reopened by a restart, and
// `bl check` passes while the evidence still hashes to what the record says it now holds.
func TestCheckExcusesTheDamageAFailureRecordLists(t *testing.T) {
	dir := gitItemWith(t, map[string]string{".gitignore": backlogDir + "/\n"})
	legacyChecks(t, dir, roundList)
	claimsOK(t, dir)
	passRound(t, dir)
	closeItem(t, dir)
	evidence := filepath.Join(dir, ".backlog", "evidence", "A1-r1.txt")
	pristine, err := os.ReadFile(evidence)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append(append([]byte{}, pristine...), []byte("tampered\n")...)
	if err := os.WriteFile(evidence, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	var checkErr error
	out := capture(t, func() { checkErr = call(t, cmdCheck, dir) })
	if !errors.Is(checkErr, errFailed) {
		t.Fatalf("the overwritten evidence must fail bl check: %v", checkErr)
	}
	if !strings.Contains(out, "A1 round .backlog/evidence/A1-r1.txt does not match its hash") {
		t.Fatalf("bl check must name the evidence:\n%s", out)
	}
	var err2 error
	capture(t, func() {
		err2 = call(t, cmdRestart, dir, "A1", "--reason", "damaged-evidence", "--why", "r1 evidence overwritten", "--change", "bl check catches the overwrite")
	})
	if err2 != nil {
		t.Fatalf("restart: %v", err2)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("the record must excuse the damage it lists: %v", err)
	}
	shown := showText(t, dir)
	if !strings.Contains(shown, "status: open") || strings.Contains(shown, "commit:") {
		t.Fatalf("a damaged-evidence restart reopens the item and drops the commit:\n%s", shown)
	}
	// editing it again is a new hash, and the record does not excuse it
	if err := os.WriteFile(evidence, append(tampered, []byte("again\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdCheck, dir); !errors.Is(err, errFailed) {
		t.Fatalf("a second edit must fail bl check: %v", err)
	}
	// so is a file the record did not list: the run file of the attempt
	if err := os.WriteFile(evidence, pristine, 0o644); err != nil {
		t.Fatal(err)
	}
	run := filepath.Join(dir, ".backlog", "evidence", "A1-run1.json")
	data, err := os.ReadFile(run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(run, append(data, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdCheck, dir); !errors.Is(err, errFailed) {
		t.Fatalf("an artefact the record does not list must fail bl check: %v", err)
	}
}

// R36: the refusals, in the order the brief gives them, and every one of them leaves the ledger
// byte-identical to what it was.
func TestRestartRefusals(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) string
		argv  []string
		rule  string
		want  string
	}{
		{
			name:  "unknown id",
			setup: func(t *testing.T) string { return openItem(t) },
			argv:  []string{"Z9", "--reason", "bad-check", "--why", "x", "--change", "y"},
			want:  "unknown item",
		},
		{
			name: "declined",
			setup: func(t *testing.T) string {
				dir := openItem(t)
				if err := call(t, cmdDrop, dir, "A1", "--reason", "not worth it"); err != nil {
					t.Fatalf("drop: %v", err)
				}
				return dir
			},
			argv: []string{"A1", "--reason", "bad-check", "--why", "x", "--change", "y"},
			want: "declined",
		},
		{
			name: "parked",
			setup: func(t *testing.T) string {
				dir := openItem(t)
				if err := call(t, cmdDefer, dir, "A1", "--reason", "later"); err != nil {
					t.Fatalf("defer: %v", err)
				}
				return dir
			},
			argv: []string{"A1", "--reason", "bad-check", "--why", "x", "--change", "y"},
			want: "parked",
		},
		{
			name: "closed and the class is not damaged-evidence",
			setup: func(t *testing.T) string {
				dir := gitItemWith(t, map[string]string{".gitignore": backlogDir + "/\n"})
				legacyChecks(t, dir, roundList)
				claimsOK(t, dir)
				passRound(t, dir)
				closeItem(t, dir)
				return dir
			},
			argv: []string{"A1", "--reason", "bad-check", "--why", "x", "--change", "y"},
			rule: "R36",
			want: "closed",
		},
		{
			name:  "the class is not one of the four",
			setup: func(t *testing.T) string { return restartItem(t) },
			argv:  []string{"A1", "--reason", "Bad-Check", "--why", "x", "--change", "y"},
			rule:  "R36",
			want:  "--reason must be one of",
		},
		{
			name:  "the class is missing",
			setup: func(t *testing.T) string { return restartItem(t) },
			argv:  []string{"A1", "--why", "x", "--change", "y"},
			rule:  "R36",
			want:  "--reason must be one of",
		},
		{
			name:  "why is empty",
			setup: func(t *testing.T) string { return restartItem(t) },
			argv:  []string{"A1", "--reason", "bad-check", "--why", "   ", "--change", "y"},
			want:  "why is empty",
		},
		{
			name:  "change is empty",
			setup: func(t *testing.T) string { return restartItem(t) },
			argv:  []string{"A1", "--reason", "bad-check", "--why", "x", "--change", "  "},
			want:  "change is empty",
		},
		{
			name: "the same change twice",
			setup: func(t *testing.T) string {
				dir := restartItem(t)
				restartOK(t, dir, "the first attempt's why", "the same change")
				return dir
			},
			argv: []string{"A1", "--reason", "bad-check", "--why", "another why", "--change", "  the same change  "},
			rule: "R36",
			want: "already restarted with the change",
		},
		{
			name: "nothing to learn from",
			setup: func(t *testing.T) string {
				dir := openItem(t)
				checksOK(t, dir, goodList)
				return dir
			},
			argv: []string{"A1", "--reason", "bad-check", "--why", "x", "--change", "y"},
			rule: "R36",
			want: "no FAIL round and no damaged artefact",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := c.setup(t)
			before := ledgerBytes(t, dir)
			err := call(t, cmdRestart, dir, c.argv...)
			if err == nil {
				t.Fatal("the restart must be refused")
			}
			if c.rule != "" {
				wantRule(t, err, c.rule)
			} else {
				wantRefusal(t, err)
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("the refusal must say %q: %s", c.want, err)
			}
			if after := ledgerBytes(t, dir); after != before {
				t.Fatal("a refused restart wrote to the ledger")
			}
		})
	}
}

func TestRestartBadCheckForR15BlockedPASS(t *testing.T) {
	newBlocked := func(t *testing.T) string {
		dir := roundItem(t, checksHeader+"J1\tjudge\tblocking\tB1\techo two\t\tif output is not two, FAIL\tthe output is judged by reading it\tthe output is two\n")
		rows := runAndVerdicts(t, dir, "J1\tPASS\t\"two\"")
		report := writeFile(t, t.TempDir(), "report.md", "Report-only: 0\n")
		if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows, "--report", report); err != nil {
			t.Fatalf("PASS round: %v", err)
		}
		return dir
	}
	dir := newBlocked(t)
	out := capture(t, func() {
		if err := call(t, cmdRestart, dir, "A1", "--reason", "bad-check", "--why", "R15 blocks close", "--change", "add a mechanical row"); err != nil {
			t.Fatalf("bad-check restart: %v", err)
		}
	})
	if out != "bl: A1 restarted as attempt 2\n" {
		t.Fatalf("restart output = %q", out)
	}
	dir = newBlocked(t)
	wantRule(t, call(t, cmdRestart, dir, "A1", "--reason", "wrong-premise", "--why", "x", "--change", "y"), "R36")
	dir = roundItem(t, roundList)
	passRound(t, dir)
	wantRule(t, call(t, cmdRestart, dir, "A1", "--reason", "bad-check", "--why", "x", "--change", "y"), "R36")
}

// R36: an item may be restarted twice. A restart in the last attempt is refused and parks the
// item — the one refusal that writes — and a closed item in the last attempt is refused with no
// write at all.
func TestRestartLimitParksTheItemForAHuman(t *testing.T) {
	dir := restartItem(t)
	restartOK(t, dir, "first attempt", "first change")
	nextAttempt(t, dir, roundFailList)
	failRound(t, dir, roundFail...)
	restartOK(t, dir, "second attempt", "second change")
	nextAttempt(t, dir, roundFailList)
	failRound(t, dir, roundFail...)
	shown := showText(t, dir)
	if !strings.Contains(shown, "attempt: 3") {
		t.Fatalf("the item must be in attempt 3:\n%s", shown)
	}
	before := strings.Count(ledgerBytes(t, dir), "\n")
	r := wantRefusal(t, call(t, cmdRestart, dir, "A1", "--reason", "bad-check", "--why", "third attempt", "--change", "third change"))
	if r.rule != "R36" {
		t.Fatalf("want a refusal under R36, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "restart limit") {
		t.Errorf("the refusal must say `restart limit`: %s", r.msg)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != before+1 {
		t.Fatalf("the limit refusal must append exactly one event, appended %d", len(events)-before)
	}
	if last := events[len(events)-1]; last.T != "defer" || !strings.Contains(last.Reason, "restart limit") {
		t.Fatalf("the limit refusal must park the item: %+v", last)
	}
	if got := showText(t, dir); !strings.Contains(got, "status: parked") {
		t.Fatalf("the item must be parked:\n%s", got)
	}
	if got := nextText(t, dir); strings.HasPrefix(got, "A1") {
		t.Fatalf("a parked item must not be named by bl next: %q", got)
	}
	// a reopen does not change the attempt, so the same call parks it again
	if err := call(t, cmdReopen, dir, "A1"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	wantRule(t, call(t, cmdRestart, dir, "A1", "--reason", "bad-check", "--why", "third attempt", "--change", "third change"), "R36")
	if got := showText(t, dir); !strings.Contains(got, "status: parked") {
		t.Fatalf("the second limit refusal must park the item again:\n%s", got)
	}
}

// R36: a closed item in its last attempt is refused with no write at all.
func TestARestartOfAClosedItemInTheLastAttemptWritesNothing(t *testing.T) {
	dir := gitItemWith(t, map[string]string{".gitignore": backlogDir + "/\n"})
	legacyChecks(t, dir, roundFailList)
	claimsOK(t, dir)
	failRound(t, dir, roundFail...)
	restartOK(t, dir, "first attempt", "first change")
	nextAttempt(t, dir, roundFailList)
	failRound(t, dir, roundFail...)
	restartOK(t, dir, "second attempt", "second change")
	nextAttempt(t, dir, roundList)
	passRound(t, dir)
	closeItem(t, dir)
	before := ledgerBytes(t, dir)
	r := wantRefusal(t, call(t, cmdRestart, dir, "A1", "--reason", "damaged-evidence", "--why", "damaged", "--change", "third change"))
	if r.rule != "R36" {
		t.Fatalf("want a refusal under R36, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "closed") {
		t.Errorf("the refusal must say the item is closed: %s", r.msg)
	}
	if after := ledgerBytes(t, dir); after != before {
		t.Fatal("a closed item in the last attempt must be refused with no write")
	}
}

// R36: the ledger without a restart event parses as it always did, and an item that was never
// restarted prints no attempt line.
func TestARestartlessItemHasNoAttemptLine(t *testing.T) {
	dir := restartItem(t)
	shown := showText(t, dir)
	if strings.Contains(shown, "attempt:") {
		t.Fatalf("an item that was never restarted must print no attempt line:\n%s", shown)
	}
	var err error
	js := capture(t, func() { err = call(t, cmdShow, dir, "A1", "--json") })
	if err != nil {
		t.Fatalf("show --json: %v", err)
	}
	var item map[string]any
	if err := json.Unmarshal([]byte(js), &item); err != nil {
		t.Fatalf("show --json: %v", err)
	}
	if item["Attempt"] != float64(1) {
		t.Fatalf("every item starts in attempt 1, got %v", item["Attempt"])
	}
}
