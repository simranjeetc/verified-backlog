package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── brief, R20 at use, and re-baseline ───────────────────────────────────────

// wantRefusal fails unless err is a refusal, and returns it.
func wantRefusal(t *testing.T, err error) Refusal {
	t.Helper()
	var r Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal, got %v", err)
	}
	return r
}

// artefactSHA returns the hash the latest event recorded for one of an item's artefacts.
func artefactSHA(t *testing.T, dir, what string) string {
	t.Helper()
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	it, ok := byID["a1"]
	if !ok {
		t.Fatal("A1 is not in the ledger")
	}
	switch what {
	case "brief":
		return it.BriefSHA
	case "check list":
		return it.ChecksSHA
	case "claims":
		return it.ClaimsSHA
	}
	t.Fatalf("unknown artefact %q", what)
	return ""
}

func TestBriefRecordsEventAndHash(t *testing.T) {
	dir := openItem(t)
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\nB0: purpose. Example: x.\n" {
		t.Fatalf("brief.md = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "brief" || ev.Brief != ".backlog/items/A1/brief.md" || ev.BriefSHA != shaHex(data) {
		t.Fatalf("brief event = %+v", ev)
	}
	first := ev.BriefSHA

	// a second brief is a second event, and the first event keeps its own hash
	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb. Example: adds a verb.\nB2: test it twice. Example: runs the test twice.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", second); err != nil {
		t.Fatalf("second brief: %v", err)
	}
	events, _ = readLedger(dir)
	ev = events[len(events)-1]
	if ev.T != "brief" || ev.BriefSHA != shaHex([]byte("B1: add a verb. Example: adds a verb.\nB2: test it twice. Example: runs the test twice.\nB0: purpose. Example: x.\n")) {
		t.Fatalf("second brief event = %+v", ev)
	}
	if ev.BriefSHA == first {
		t.Fatal("the second brief must carry its own hash")
	}
	if events[len(events)-2].BriefSHA != first {
		t.Fatal("the earlier brief event must keep its hash")
	}
	// the file matches the latest hash, so bl check still passes (R24)
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

func TestBriefReadsStdinWithoutFile(t *testing.T) {
	dir := openItem(t)
	f, err := os.Open(writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB0: purpose. Example: x.\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	old := os.Stdin
	os.Stdin = f
	defer func() { os.Stdin = old }()
	if err := call(t, cmdBrief, dir, "A1"); err != nil {
		t.Fatalf("brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb. Example: adds a verb.\nB0: purpose. Example: x.\n" {
		t.Fatalf("brief.md = %q, %v", data, err)
	}
}

func TestBriefRefusedAfterClaims(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB0: purpose. Example: x.\n")
	wantRule(t, call(t, cmdBrief, dir, "A1", "--file", brief), "R5")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "claims" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if string(data) != "B1: add a verb Example: adds a verb\nB2: test it Example: runs the test\n" {
		t.Fatalf("a refused brief rewrote the file: %q", data)
	}
}

func TestBriefRefusedWithoutClause(t *testing.T) {
	dir := openItem(t)
	brief := writeFile(t, t.TempDir(), "brief.md", "add a verb and test it\n")
	wantRule(t, call(t, cmdBrief, dir, "A1", "--file", brief), "R2")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "open" {
		t.Fatalf("a refusal wrote %+v", last)
	}
}

func TestBriefRefusedWithoutB0(t *testing.T) {
	dir := openItem(t)
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\n")
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdBrief, dir, "A1", "--file", brief))
	if r.rule != "R2" || !strings.Contains(r.msg, "B0") {
		t.Fatalf("want R2 naming B0, got %s: %s", r.rule, r.msg)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused brief wrote an event: before %d after %d", len(before), len(after))
	}
}

func TestBriefRequiresExamplePerClause(t *testing.T) {
	dir := openItem(t)
	brief := writeFile(t, t.TempDir(), "brief.md", "B0: purpose. Example: x.\nB1: prints ok.\nB2: exits 3. Example: empty repo exits 3.\n")
	r := wantRefusal(t, call(t, cmdBrief, dir, "A1", "--file", brief))
	if r.rule != "R2" || !strings.Contains(r.msg, "B1") || strings.Contains(r.msg, "B2") {
		t.Fatalf("want R2 naming only B1, got %s: %s", r.rule, r.msg)
	}
	if err := os.WriteFile(brief, []byte("B0: purpose. Example: x.\nB1: prints ok. Example: prints ok and exits 0.\nB2: exits 3. example: empty repo exits 3.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("lowercase example: %v", err)
	}

	dir = openItem(t)
	brief = writeFile(t, t.TempDir(), "brief.md", "B0: purpose. Example: x.\nB1: prints ok.\n  Example: `bl x` prints ok and exits 0.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("continuation example: %v", err)
	}

	dir = openItem(t)
	brief = writeFile(t, t.TempDir(), "brief.md", "B0: purpose. Example: x.\nB1: prints ok. Example without colon\n")
	r = wantRefusal(t, call(t, cmdBrief, dir, "A1", "--file", brief))
	if r.rule != "R2" || !strings.Contains(r.msg, "B1") {
		t.Fatalf("want R2 naming B1, got %s: %s", r.rule, r.msg)
	}
}

func TestBriefAmendmentRequiresExamplesAndKeepsPriorBrief(t *testing.T) {
	dir := openItem(t)
	good := writeFile(t, t.TempDir(), "brief.md", "B0: purpose. Example: x.\nB1: prints ok. Example: prints ok.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", good); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	bad := writeFile(t, t.TempDir(), "brief2.md", "B0: purpose. Example: x.\nB1: prints ok. Example: prints ok.\nB2: exits 3.\n")
	r := wantRefusal(t, call(t, cmdBrief, dir, "A1", "--file", bad))
	if r.rule != "R2" || !strings.Contains(r.msg, "B2") {
		t.Fatalf("want R2 naming B2, got %s: %s", r.rule, r.msg)
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if string(after) != string(before) {
		t.Fatalf("refused amendment changed prior brief: %q", after)
	}
}

// R20 at use: `bl brief` re-hashes the stored brief before it overwrites it.
func TestBriefRefusesEditedStoredBrief(t *testing.T) {
	dir := openItem(t)
	first := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", first); err != nil {
		t.Fatalf("first brief: %v", err)
	}
	stored := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	events, _ := readLedger(dir)
	n := len(events)
	recorded := artefactSHA(t, dir, "brief")

	// the stored brief is edited on disk after it was recorded
	tampered := []byte("B1: tampered. Example: prints tampered.\n")
	if err := os.WriteFile(stored, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb. Example: adds a verb.\nB2: test it twice. Example: runs the test twice.\nB0: purpose. Example: x.\n")
	r := wantRefusal(t, call(t, cmdBrief, dir, "A1", "--file", second))
	if r.rule != "R20" {
		t.Fatalf("want a refusal under R20, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "brief") || !strings.Contains(r.msg, ".backlog/items/A1/brief.md") {
		t.Errorf("the refusal must name the artefact: %s", r.msg)
	}
	if !strings.Contains(r.msg, recorded) || !strings.Contains(r.msg, shaHex(tampered)) {
		t.Errorf("the refusal must name both hashes: %s", r.msg)
	}
	// the stored brief is unchanged and the ledger has no new event
	after, _ := os.ReadFile(stored)
	if string(after) != string(tampered) {
		t.Errorf("a refused brief rewrote the file: %q", after)
	}
	events, _ = readLedger(dir)
	if len(events) != n {
		t.Fatalf("a refused brief appended %d events", len(events)-n)
	}
	if last := events[len(events)-1]; last.T != "brief" || last.BriefSHA != recorded {
		t.Fatalf("the last event changed: %+v", last)
	}
}

func TestBriefRecordsOverUntouchedStoredBrief(t *testing.T) {
	dir := openItem(t)
	first := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB2: test it. Example: runs the test.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", first); err != nil {
		t.Fatalf("first brief: %v", err)
	}
	// the stored brief was not touched: the verb proceeds
	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb. Example: adds a verb.\nB2: test it twice. Example: runs the test twice.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", second); err != nil {
		t.Fatalf("second brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb. Example: adds a verb.\nB2: test it twice. Example: runs the test twice.\nB0: purpose. Example: x.\n" {
		t.Fatalf("brief.md = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "brief" || ev.BriefSHA != shaHex(data) {
		t.Fatalf("brief event = %+v", ev)
	}
}

func TestBriefProceedsWhenNoStoredBrief(t *testing.T) {
	dir := t.TempDir()
	// a ledger written before bl wrote briefs: the open event records no brief
	for _, ev := range []Event{
		{T: "file", ID: "A1", Text: "add a verb; test it"},
		{T: "open", ID: "A1"},
	} {
		if _, err := appendEvent(dir, ev); err != nil {
			t.Fatalf("append %s: %v", ev.T, err)
		}
	}
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb. Example: adds a verb.\nB0: purpose. Example: x.\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb. Example: adds a verb.\nB0: purpose. Example: x.\n" {
		t.Fatalf("brief.md = %q, %v", data, err)
	}
}

func TestCheckRefusesEditedArtefact(t *testing.T) {
	cases := []struct{ what, file, named string }{
		{"brief", "brief.md", "brief"},
		{"check list", "checks.tsv", "check list"},
		{"claims", "claims.md", "claims"},
	}
	for _, c := range cases {
		t.Run(c.what, func(t *testing.T) {
			dir := openItem(t)
			checksOK(t, dir, goodList)
			claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
			if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
				t.Fatalf("claims: %v", err)
			}
			// nothing edited: the check passes
			if err := call(t, cmdCheck, dir); err != nil {
				t.Fatalf("check: %v", err)
			}
			recorded := artefactSHA(t, dir, c.what)
			edited := "edited on disk\n"
			if err := os.WriteFile(filepath.Join(dir, ".backlog", "items", "A1", c.file), []byte(edited), 0o644); err != nil {
				t.Fatal(err)
			}
			r := wantRefusal(t, call(t, cmdCheck, dir))
			if r.rule != "R20" {
				t.Fatalf("want a refusal under R20, got %s: %s", r.rule, r.msg)
			}
			if !strings.Contains(r.msg, c.named) {
				t.Errorf("the refusal must name the artefact: %s", r.msg)
			}
			if !strings.Contains(r.msg, recorded) || !strings.Contains(r.msg, shaHex([]byte(edited))) {
				t.Errorf("the refusal must name both hashes: %s", r.msg)
			}
		})
	}
}

func TestRoundRefusesEditedArtefact(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	if err := os.WriteFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"), []byte(goodList+"# edited\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R20")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "checks" {
		t.Fatalf("a refused round wrote %+v", last)
	}
}

const rebaselinedList = checksHeader +
	"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\ttests pass\n"

func TestRebaselineRecordsOldAndNewHash(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	old := shaHex([]byte(goodList))
	reason := "the list's own command was broken"
	if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", reason); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if err != nil || string(data) != rebaselinedList {
		t.Fatalf("checks.tsv = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "rebaseline" || ev.Checks != ".backlog/items/A1/checks.tsv" {
		t.Fatalf("rebaseline event = %+v", ev)
	}
	if ev.ChecksSHA != shaHex([]byte(rebaselinedList)) {
		t.Errorf("the event must carry the new hash: %+v", ev)
	}
	if ev.PrevSHA != old {
		t.Errorf("the event must carry the previous hash: %+v", ev)
	}
	if ev.Reason != reason {
		t.Errorf("the event must carry the reason: %+v", ev)
	}
	if events[len(events)-2].ChecksSHA != old {
		t.Error("the earlier checks event must keep its hash")
	}
}

func TestRebaselineRefusedAfterClaims(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	wantRule(t, call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "too late"), "R5")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "claims" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != goodList {
		t.Fatalf("a refused re-baseline changed the list: %q", data)
	}
}

func TestRebaselineRefusedMalformedList(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	// a blocking row with no brief_ref (R2)
	wantRule(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", checksHeader+"S1\tbl\tblocking\t\tx\texit 0\t\t\tc\n"),
		"--reason", "broken"), "R2")
	// a header that names the wrong columns (R1)
	wantRule(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c2.tsv", "id\tdecider\nS1\tbl\n"),
		"--reason", "broken"), "R1")
	// a judge row with no why_not_mech (R3)
	wantRule(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c3.tsv", checksHeader+"J1\tjudge\tadvisory\t\tx\t\tit fails\t\tc\n"),
		"--reason", "broken"), "R3")
	// the stored list is untouched and nothing was written
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != goodList {
		t.Fatalf("a refused re-baseline changed the list: %q", data)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "checks" {
		t.Fatalf("a refusal wrote %+v", last)
	}
}

func TestRebaselineNeedsAReason(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	wantRefusal(t, call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList)))
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "checks" {
		t.Fatalf("a refusal wrote %+v", last)
	}
}

// A re-baseline is a new premise, not a new item: it resets no count (R19). The code-round cap
// and the total-round budget both survive it, so a re-baseline cannot buy more rounds.
func TestRebaselineResetsNoCount(t *testing.T) {
	dir := gitItem(t)
	legacyChecks(t, dir, roundList)
	// A re-baseline replaces the frozen list with a new premise. The claims freeze the list
	// (R5), so the re-baseline comes before them — and before every round, because a round
	// needs a claims event (R6).
	if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "the list's own command was broken"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	for i := 0; i < roundCap; i++ {
		rows := runAndVerdicts(t, dir, "S1\tFAIL\t")
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	// the cap is spent
	rows := runAndVerdicts(t, dir, "S1\tFAIL\t")
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R19")

	// the earlier rounds stay in the ledger: a re-baseline is not a way to erase a failure
	events, _ := readLedger(dir)
	rounds := 0
	for _, ev := range events {
		if ev.T == "round" {
			rounds++
		}
	}
	if rounds != roundCap {
		t.Fatalf("rounds = %d, want %d — a re-baseline does not erase the rounds before it", rounds, roundCap)
	}
}
