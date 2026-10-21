package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

func sampleEvents() []Event {
	return []Event{
		{Seq: 1, At: "2026-10-20T09:00:00Z", T: "file", ID: "A1", Text: "first"},
		{Seq: 2, At: "2026-10-20T09:01:00Z", T: "file", ID: "A2", Text: "second", After: "A1"},
		{Seq: 3, At: "2026-10-20T09:02:00Z", T: "open", ID: "A1"},
		{Seq: 4, At: "2026-10-20T09:03:00Z", T: "round", ID: "A1", Verdict: "FAIL", Cause: "code", Rows: "e", RowsSHA: "x"},
		{Seq: 5, At: "2026-10-20T09:04:00Z", T: "round", ID: "A1", Verdict: "PASS", Rows: "e", RowsSHA: "x"},
		{Seq: 6, At: "2026-10-20T09:05:00Z", T: "close", ID: "A1", Commit: "abc1234"},
	}
}

func TestDeriveStates(t *testing.T) {
	order, byID, err := derive(sampleEvents())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("want 2 items, got %d", len(order))
	}
	if got := byID["a1"].Status; got != "closed" {
		t.Errorf("A1 status = %q, want closed", got)
	}
	if got := byID["a1"].codeFails(); got != 1 {
		t.Errorf("A1 code fails = %d, want 1", got)
	}
	if got := byID["a2"].BlockedBy; got != "" {
		t.Errorf("A2 blocked by %q, want none after the dependency closed", got)
	}
}

func TestBlockedUntilDependencyCloses(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second", After: "A1"},
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if got := byID["a2"].BlockedBy; got != "A1" {
		t.Errorf("BlockedBy = %q, want A1", got)
	}
}

func TestDeriveRejectsDuplicateFile(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "a1", Text: "again"},
	}
	if _, _, err := derive(events); err == nil {
		t.Fatal("expected corruption for a duplicate id")
	}
}

func TestPinnedRefusesTwo(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second"},
		{Seq: 3, T: "open", ID: "A1"},
		{Seq: 4, T: "open", ID: "A2"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := pinned(order); err == nil {
		t.Fatal("expected corruption when two items are in flight")
	}
}

func TestEligibleSkipsBlockedAndParked(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second", After: "A1"},
		{Seq: 3, T: "file", ID: "O4", Text: "parked"},
		{Seq: 4, T: "defer", ID: "O4", Reason: "later"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := eligible(order)
	if len(got) != 1 || got[0].ID != "A1" {
		t.Fatalf("eligible = %v, want just A1", ids(got))
	}
}

func ids(items []*Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestParseArgs(t *testing.T) {
	a, err := parseArgs([]string{"A1", "--verdict", "FAIL", "--cause=code", "--rows", "-"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.pos(0) != "A1" {
		t.Errorf("positional = %q", a.pos(0))
	}
	if a.get("verdict") != "FAIL" || a.get("cause") != "code" || a.get("rows") != "-" {
		t.Errorf("flags = %v", a.flags)
	}
	if _, err := parseArgs([]string{"--verdict"}); err == nil {
		t.Error("expected a refusal for a flag with no value")
	}
}

func TestParseSince(t *testing.T) {
	cases := map[string]time.Duration{"2d": 48 * time.Hour, "36h": 36 * time.Hour, "1w": 168 * time.Hour}
	for in, want := range cases {
		got, err := parseSince(in)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseSince("soon"); err == nil {
		t.Error("expected a refusal for an unparseable period")
	}
}

func TestRenderQueueGroups(t *testing.T) {
	order, _, err := derive(sampleEvents())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	out := renderQueue("/tmp/pilot", order)
	for _, want := range []string{"## Queue (1)", "## Closed (1)", "- [x] A1 — first", "- [ ] A2 — second"} {
		if !strings.Contains(out, want) {
			t.Errorf("queue missing %q", want)
		}
	}
}

func TestRenderRecordsShowsDecisionNotVerdict(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "D1", Text: "cosmetic"},
		{Seq: 2, T: "drop", ID: "D1", Reason: "not worth it"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	out := renderRecords("/tmp/pilot", order)
	if !strings.Contains(out, "Decision: declined — not worth it (not verified)") {
		t.Fatalf("declined record must not look like a verdict:\n%s", out)
	}
	if strings.Contains(out, "Verdict: PASS") {
		t.Fatal("a decline must never render as a PASS")
	}
}

func TestParseLegacy(t *testing.T) {
	dir := t.TempDir()
	todo := filepath.Join(dir, "TODO.md")
	progress := filepath.Join(dir, "PROGRESS.md")
	os.WriteFile(todo, []byte("- [x] A1 first — **PASS**: done (commit abcdef1)\n- [ ] A2 second\n- [ ] no id\n"), 0o644)
	os.WriteFile(progress, []byte("### A1 — first — PASS\n\nVerdict: PASS\n\nclosed at abcdef1\n"), 0o644)

	items, findings, err := parseLegacy(todo, progress)
	if err != nil {
		t.Fatalf("parseLegacy: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !items[0].Closed || items[0].Commit != "abcdef1" {
		t.Errorf("A1 = %+v, want closed at abcdef1", items[0])
	}
	if len(findings) != 1 || !strings.Contains(findings[0], "checkbox without a readable id") {
		t.Errorf("findings = %v", findings)
	}
}

func TestSchemaIsJSON(t *testing.T) {
	s := schemaJSON()
	if s["version"] != 1 {
		t.Errorf("version = %v", s["version"])
	}
	events := s["events"].(map[string]map[string][]string)
	if len(events) != 15 {
		t.Errorf("want 15 event kinds, got %d", len(events))
	}
	for _, kind := range []string{"checks", "claims"} {
		if _, ok := events[kind]; !ok {
			t.Errorf("schema is missing event kind %q", kind)
		}
	}
	for _, kind := range []string{"brief", "rebaseline", "run", "second-check", "misclassified"} {
		if _, ok := events[kind]; !ok {
			t.Errorf("schema is missing event kind %q", kind)
		}
	}
}

// capture runs fn with stdout redirected and returns what it printed.
func capture(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stdout = old
	out := <-done
	r.Close()
	return out
}

// captureStderr runs fn with stderr redirected and returns what it printed.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stderr
	os.Stderr = w
	done := make(chan string, 1)
	go func() {
		var b strings.Builder
		io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	w.Close()
	os.Stderr = old
	out := <-done
	r.Close()
	return out
}

func TestSchemaListsEveryKind(t *testing.T) {
	var err error
	out := capture(t, func() { err = call(t, cmdSchema, t.TempDir()) })
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	kinds := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "required:" {
			kinds[f[0]] = true
		}
	}
	if len(kinds) != 15 {
		t.Errorf("want 15 event kinds in the schema output, got %d: %v", len(kinds), kinds)
	}
	for _, kind := range []string{"brief", "rebaseline", "run", "second-check", "misclassified"} {
		if !kinds[kind] {
			t.Errorf("the schema output is missing event kind %q", kind)
		}
	}
}

// The schema output reports how many event kinds it lists, so a reader (and a grep) can tell
// 15 from 14 without counting the lines.
func TestSchemaReportsTheNumberOfEventKinds(t *testing.T) {
	var err error
	out := capture(t, func() { err = call(t, cmdSchema, t.TempDir()) })
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	listed := 0
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "required:" {
			listed++
		}
	}
	want := fmt.Sprintf("%d event kinds", listed)
	if !strings.Contains(out, want) {
		t.Fatalf("the schema output must report its count (%q):\n%s", want, out)
	}
}

// ── checks and claims ────────────────────────────────────────────────────────

const checksHeader = "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\tcriterion\n"

// openItem initialises a ledger in a temp dir, files A1 with a two-clause text and opens it.
func openItem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
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

func writeFile(t *testing.T, dir, name, text string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// call runs one handler against dir and returns its error.
func call(t *testing.T, h func(string, args) error, dir string, argv ...string) error {
	t.Helper()
	a, err := parseArgs(argv)
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	return h(dir, a)
}

func wantRule(t *testing.T, err error, rule string) {
	t.Helper()
	var r Refusal
	if !errors.As(err, &r) {
		t.Fatalf("want a refusal (%s), got %v", rule, err)
	}
	if r.rule != rule {
		t.Fatalf("want refusal under %s, got %s: %s", rule, r.rule, r.msg)
	}
}

func checksOK(t *testing.T, dir, list string) {
	t.Helper()
	if err := call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list)); err != nil {
		t.Fatalf("checks: %v", err)
	}
}

func checksRefused(t *testing.T, dir, list, rule string, extra ...string) {
	t.Helper()
	argv := append([]string{"A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list)}, extra...)
	wantRule(t, call(t, cmdChecks, dir, argv...), rule)
}

const goodRows = "S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\ttests pass\n" +
	"J1\tjudge\tadvisory\t\tgit diff\t\tthe diff renames a public verb\tnaming quality is a reading of the diff\tnames stay stable\n"

const goodList = checksHeader + goodRows

func TestOpenWritesBriefAndRecordsHash(t *testing.T) {
	dir := openItem(t)
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil {
		t.Fatalf("brief: %v", err)
	}
	if string(data) != "B1: add a verb\nB2: test it\n" {
		t.Fatalf("brief = %q", data)
	}
	events, _ := readLedger(dir)
	open := events[len(events)-1]
	if open.T != "open" || open.Brief != ".backlog/items/A1/brief.md" || open.BriefSHA != shaHex(data) {
		t.Fatalf("open event = %+v", open)
	}
	if got := briefText("one clause only"); got != "B1: one clause only\n" {
		t.Errorf("briefText = %q", got)
	}
}

func TestChecksRecordsListAndHash(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if err != nil || string(data) != goodList {
		t.Fatalf("checks.tsv = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "checks" || ev.Checks != ".backlog/items/A1/checks.tsv" || ev.ChecksSHA != shaHex(data) {
		t.Fatalf("checks event = %+v", ev)
	}
	if _, _, err := derive(events); err != nil {
		t.Fatalf("derive: %v", err)
	}
}

func TestChecksColumnsReadByName(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, "criterion\tid\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\n"+
		"tests pass\tS1\tbl\tblocking\tB2\tgo test ./...\texit 0\t\t\n")
}

func TestChecksR1Schema(t *testing.T) {
	cases := map[string]string{
		"missing column":  "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\nS1\tbl\tblocking\tB1\tx\texit 0\t\t\n",
		"unknown column":  strings.Replace(goodList, "criterion", "notes", 1),
		"short row":       checksHeader + "S1\tbl\tblocking\tB1\tx\texit 0\n",
		"bad decider":     checksHeader + "S1\tmodel\tblocking\tB1\tx\texit 0\t\t\tc\n",
		"bad severity":    checksHeader + "S1\tbl\tmajor\tB1\tx\texit 0\t\t\tc\n",
		"empty cmd":       checksHeader + "S1\tbl\tblocking\tB1\t\texit 0\t\t\tc\n",
		"duplicate id":    goodList + "S1\tbl\tadvisory\t\tx\texit 0\t\t\tc\n",
		"reserved id":     checksHeader + "run\tbl\tblocking\tB1\tx\texit 0\t\t\tc\n",
		"bad id":          checksHeader + "S 1\tbl\tblocking\tB1\tx\texit 0\t\t\tc\n",
		"bl no expect":    checksHeader + "S1\tbl\tblocking\tB1\tx\t\t\t\tc\n",
		"bl with fail_if": checksHeader + "S1\tbl\tblocking\tB1\tx\texit 0\tit fails\t\tc\n",
		"judge w/ expect": checksHeader + "J1\tjudge\tblocking\tB1\tx\texit 0\tit fails\ta reading of prose text\tc\n",
		"no rows":         checksHeader,
	}
	for name, list := range cases {
		t.Run(name, func(t *testing.T) {
			checksRefused(t, openItem(t), list, "R1")
		})
	}
}

func TestChecksR2BriefRef(t *testing.T) {
	dir := openItem(t)
	checksRefused(t, dir, checksHeader+"S1\tbl\tblocking\t\tx\texit 0\t\t\tc\n", "R2")
	checksRefused(t, dir, checksHeader+"S1\tbl\tblocking\tB9\tx\texit 0\t\t\tc\n", "R2")
	checksRefused(t, dir, checksHeader+"S1\tbl\tadvisory\tB9\tx\texit 0\t\t\tc\n", "R2")
	checksOK(t, dir, checksHeader+"S1\tbl\tadvisory\t\tx\texit 0\t\t\tc\n")
}

func TestChecksR3WhyNotMech(t *testing.T) {
	for _, why := range []string{"", "TBD", "  n/a ", "None", "because", "Because it is judgment", "too short"} {
		t.Run(why, func(t *testing.T) {
			checksRefused(t, openItem(t), checksHeader+"J1\tjudge\tblocking\tB1\tx\t\tit fails\t"+why+"\tc\n", "R3")
		})
	}
	// a judge row with no fail_if is a schema fault, so it is refused under R1, not R3
	checksRefused(t, openItem(t), checksHeader+"J1\tjudge\tblocking\tB1\tx\t\t\ta reading of prose text\tc\n", "R1")
}

// R1 owns the row schema: a judge row needs a non-empty fail_if. R3 owns the justification —
// the placeholder and the too-short why_not_mech.
func TestChecksR1JudgeRowNeedsFailIf(t *testing.T) {
	dir := openItem(t)
	list := checksHeader + "J1\tjudge\tblocking\tB1\tx\t\t\ta reading of prose text\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
	if r.rule != "R1" {
		t.Fatalf("want a refusal under R1, got %s: %s", r.rule, r.msg)
	}
	if !strings.Contains(r.msg, "fail_if") {
		t.Errorf("the refusal must name the missing field: %s", r.msg)
	}
	// the list is not stored
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "open" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); err == nil {
		t.Fatal("a refused list must not be stored")
	}
}

func TestChecksR8ExpectForms(t *testing.T) {
	for _, expect := range []string{
		"exit 0", "exit 255", "stdout matches ^ok", "stderr matches (?i)error", "file exists scripts/go.mod",
		"tree == 0123456789abcdef0123456789abcdef01234567",
	} {
		t.Run("ok "+expect, func(t *testing.T) {
			checksOK(t, openItem(t), checksHeader+"S1\tbl\tblocking\tB1\tx\t"+expect+"\t\t\tc\n")
		})
	}
	for _, expect := range []string{
		"exit -1", "exit 256", "exit zero", "exit 01", "returns 0", "stdout contains ok", "stdout matches (",
		"file exists /etc/passwd", "file exists ../x", "tree == ABC", "Exit 0",
	} {
		t.Run("refused "+expect, func(t *testing.T) {
			checksRefused(t, openItem(t), checksHeader+"S1\tbl\tblocking\tB1\tx\t"+expect+"\t\t\tc\n", "R8")
		})
	}
}

func TestChecksR4WrittenOnce(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	checksRefused(t, dir, goodList, "R4")

	// an amendment may not change severity
	checksRefused(t, dir, strings.Replace(goodList, "\tjudge\tadvisory\t", "\tjudge\tblocking\tB1", 1), "R4", "--amend")
	// nor add a row, change cmd, or reclassify bl → judge
	checksRefused(t, dir, goodList+"S2\tbl\tadvisory\t\tx\texit 0\t\t\tc\n", "R4", "--amend")
	checksRefused(t, dir, strings.Replace(goodList, "go test ./...", "true", 1), "R4", "--amend")
	checksRefused(t, dir, goodList, "R4", "--amend")

	// filling an empty brief_ref on an advisory row is accepted, and records the old hash
	before := shaHex([]byte(goodList))
	amended := strings.Replace(goodList, "\tjudge\tadvisory\t\t", "\tjudge\tadvisory\tB2\t", 1)
	if err := call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", amended)); err != nil {
		t.Fatalf("amend: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "checks" || ev.PrevSHA != before || ev.ChecksSHA != shaHex([]byte(amended)) {
		t.Fatalf("amend event = %+v", ev)
	}
	if events[len(events)-2].ChecksSHA != before {
		t.Fatal("the earlier checks event must keep its hash")
	}

	// reclassifying judge → bl, with expect set and fail_if/why_not_mech cleared, is accepted
	reclassed := strings.Replace(amended, "J1\tjudge\tadvisory\tB2\tgit diff\t\tthe diff renames a public verb\tnaming quality is a reading of the diff",
		"J1\tbl\tadvisory\tB2\tgit diff\texit 0\t\t", 1)
	if err := call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", reclassed)); err != nil {
		t.Fatalf("reclassify: %v", err)
	}
}

func TestChecksAmendWithoutListRefused(t *testing.T) {
	checksRefused(t, openItem(t), goodList, "R4", "--amend")
}

func TestR5Ordering(t *testing.T) {
	dir := openItem(t)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	wantRule(t, call(t, cmdClaims, dir, "A1", "--file", claims), "R5")

	checksOK(t, dir, goodList)
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "claims.md"))
	if ev.T != "claims" || ev.Claims != ".backlog/items/A1/claims.md" || ev.ClaimsSHA != shaHex(data) {
		t.Fatalf("claims event = %+v", ev)
	}
	if _, _, err := derive(events); err != nil {
		t.Fatalf("derive: %v", err)
	}

	wantRule(t, call(t, cmdClaims, dir, "A1", "--file", claims), "R5")
	checksRefused(t, dir, goodList, "R5")
	checksRefused(t, dir, strings.Replace(goodList, "\tjudge\tadvisory\t\t", "\tjudge\tadvisory\tB2\t", 1), "R5", "--amend")
}

func TestR20EditedArtefactRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	os.WriteFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"), []byte(goodList+"# edited\n"), 0o644)
	claims := writeFile(t, t.TempDir(), "claims.md", "done\n")
	wantRule(t, call(t, cmdClaims, dir, "A1", "--file", claims), "R20")
}

func TestRefusalExitCodeNamesRule(t *testing.T) {
	dir := openItem(t)
	path := writeFile(t, t.TempDir(), "c.tsv", checksHeader+"S1\tbl\tblocking\t\tx\texit 0\t\t\tc\n")
	if code := run([]string{"--root", dir, "checks", "A1", "--file", path}); code != 2 {
		t.Fatalf("exit = %d, want 2", code)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "open" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); err == nil {
		t.Fatal("a refused list must not be stored")
	}
}

func TestDeriveRejectsClaimsBeforeChecks(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "open", ID: "A1"},
		{Seq: 3, T: "claims", ID: "A1", Claims: "c", ClaimsSHA: "x"},
	}
	if _, _, err := derive(events); err == nil {
		t.Fatal("expected corruption for claims with no check list")
	}
}

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
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\nB2: test it\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb\nB2: test it\n" {
		t.Fatalf("brief.md = %q, %v", data, err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "brief" || ev.Brief != ".backlog/items/A1/brief.md" || ev.BriefSHA != shaHex(data) {
		t.Fatalf("brief event = %+v", ev)
	}
	first := ev.BriefSHA

	// a second brief is a second event, and the first event keeps its own hash
	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb\nB2: test it twice\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", second); err != nil {
		t.Fatalf("second brief: %v", err)
	}
	events, _ = readLedger(dir)
	ev = events[len(events)-1]
	if ev.T != "brief" || ev.BriefSHA != shaHex([]byte("B1: add a verb\nB2: test it twice\n")) {
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
	f, err := os.Open(writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\n"))
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
	if err != nil || string(data) != "B1: add a verb\n" {
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
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\n")
	wantRule(t, call(t, cmdBrief, dir, "A1", "--file", brief), "R5")
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "claims" {
		t.Fatalf("a refusal wrote %+v", last)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if string(data) != "B1: add a verb\nB2: test it\n" {
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

// R20 at use: `bl brief` re-hashes the stored brief before it overwrites it.
func TestBriefRefusesEditedStoredBrief(t *testing.T) {
	dir := openItem(t)
	first := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\nB2: test it\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", first); err != nil {
		t.Fatalf("first brief: %v", err)
	}
	stored := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	events, _ := readLedger(dir)
	n := len(events)
	recorded := artefactSHA(t, dir, "brief")

	// the stored brief is edited on disk after it was recorded
	tampered := []byte("B1: tampered\n")
	if err := os.WriteFile(stored, tampered, 0o644); err != nil {
		t.Fatal(err)
	}

	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb\nB2: test it twice\n")
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
	first := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\nB2: test it\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", first); err != nil {
		t.Fatalf("first brief: %v", err)
	}
	// the stored brief was not touched: the verb proceeds
	second := writeFile(t, t.TempDir(), "brief2.md", "B1: add a verb\nB2: test it twice\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", second); err != nil {
		t.Fatalf("second brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb\nB2: test it twice\n" {
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
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\n")
	if err := call(t, cmdBrief, dir, "A1", "--file", brief); err != nil {
		t.Fatalf("brief: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "brief.md"))
	if err != nil || string(data) != "B1: add a verb\n" {
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
	checksOK(t, dir, roundList)
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
	tampered := []byte("B1: tampered\n")
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
	amended := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\n")
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
	amended := writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\nB2: test it\n")
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
			return []string{"brief", "A1", "--file", writeFile(t, t.TempDir(), "brief.md", "B1: add a verb\n")}
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
			tampered := []byte("B1: tampered\n")
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
	dir := roundItem(t, roundList)
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
	dir := roundItem(t, roundList)
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
	dir := roundItem(t, roundList)
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
	// the sixth round is a PASS and it is accepted
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
	checksOK(t, dir, roundList)
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
	dir := roundItem(t, roundList)
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
	dir := roundItem(t, roundList)
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
	dir := roundItem(t, roundList)
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
	dir := roundItem(t, roundList)
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
	// the sixth round is a PASS
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundFail...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("code round: %v", err)
	}
	rows = runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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
				// the state rules are broken by the item's state
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
				r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
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
			r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
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
			checksRefused(t, openItem(t), checksHeader+p1Row(rule), rule)
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

// r24List freezes two blocking rows: S1 cites B1, S2 cites B2.
const r24List = checksHeader +
	"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\ttests pass\n" +
	"S2\tbl\tblocking\tB2\tgo test ./...\texit 0\t\t\tother tests pass\n"

// r24BriefWithoutB2 is the brief after an amendment that removed B2.
const r24BriefWithoutB2 = "B1: add a verb\n"

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
		"an amendment that adds a clause": {"B1: add a verb\nB2: test it\nB3: and a third thing\n"},
		// a clause removed and then restored by a later amendment exists in the latest brief
		"a clause removed and then restored": {"B1: add a verb\n", "B1: add a verb\nB2: test it\n"},
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
	checksOK(t, dir, goodList)
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
	dir := roundItem(t, roundList)
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
// literal pipe, so the row does not do what it says.
const r34BadRow = "X34\tbl\tblocking\tB1\tgrep a\\|b f\texit 0\t\t\tc\n"

// r34GoodRow is a row whose cmd holds a plain pipe: the shell reads that as a pipe, which is
// what the row means.
const r34GoodRow = "S1\tbl\tblocking\tB1\techo ab | grep a\texit 0\t\t\tc\n"

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
		"S1\tbl\tblocking\tB1\techo ab | grep a\texit 0\t\t\tc\n"+
		"S2\tbl\tblocking\tB1\techo ab | grep -E 'a|b' | wc -l\texit 0\t\t\tc\n"+
		"S3\tbl\tblocking\tB1\ttrue || false\texit 0\t\t\tc\n")
}

// A list with no pipe at all is still accepted.
func TestChecksR34AcceptsAListWithNoPipe(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, checksHeader+"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\tc\n")
}

// R34 is about the cmd field only: an expect pattern is Go's RE2, where a backslash before a
// pipe means a literal pipe, and that is what the pattern says.
func TestChecksR34DoesNotRefuseAnEscapedPipeInAnExpect(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, checksHeader+"S1\tbl\tblocking\tB1\techo \"a|b\"\tstdout matches a\\|b\t\t\tc\n")
}

// Within R34 the refusal names the first broken row in file order, and that row only.
func TestR34NamesTheFirstBrokenRowOnly(t *testing.T) {
	dir := openItem(t)
	list := checksHeader +
		"X1\tbl\tblocking\tB1\tgrep a\\|b f\texit 0\t\t\tc\n" +
		"X2\tbl\tblocking\tB1\tgrep c\\|d f\texit 0\t\t\tc\n"
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
		{"no backslash", "PIPE0\tbl\tblocking\tB1\techo a | cat\texit 0\t\t\tc\n", false},
		{"one backslash", "PIPE1\tbl\tblocking\tB1\techo a \\| cat\texit 0\t\t\tc\n", true},
		{"two backslashes", "PIPE2\tbl\tblocking\tB1\techo a \\\\| cat\texit 0\t\t\tc\n", false},
		{"three backslashes", "PIPE3\tbl\tblocking\tB1\techo a \\\\\\| cat\texit 0\t\t\tc\n", true},
		{"four backslashes", "PIPE4\tbl\tblocking\tB1\techo a \\\\\\\\| cat\texit 0\t\t\tc\n", false},
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
	list := checksHeader + "PIPE2\tbl\tblocking\tB1\techo a \\\\| cat\texit 0\t\t\tc\n"
	if err := call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "the premise moved"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv"))
	if string(data) != list {
		t.Fatalf("the re-baseline stored %q, want %q", data, list)
	}
}

// ── the run ──────────────────────────────────────────────────────────────────

// oneRowList is a well-formed list with one row: the smallest thing a run can be over.
const oneRowList = checksHeader + "S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tone row\n"

// runItem prepares an item with a frozen check list and frozen claims, ready to run.
func runItem(t *testing.T, list string) string {
	t.Helper()
	dir := openItem(t)
	checksOK(t, dir, list)
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
// which print a known word, so that an output anchor has something to point at.
const roundList = checksHeader +
	"S1\tbl\tblocking\tB1\techo two\tstdout matches ^two\t\t\tprints two\n" +
	"J1\tjudge\tblocking\tB1\techo two\t\tif the output is not two, FAIL\ttwo is a reading of intent\tthe output is two\n"

// The verdicts of the round list: both rows pass, or the binary row fails. A judge row carries
// an output anchor, and the row's command prints the anchored word.
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

// roundItem prepares an item for a round: the list is frozen and the claims are recorded.
func roundItem(t *testing.T, list string) string {
	t.Helper()
	dir := gitItem(t)
	checksOK(t, dir, list)
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
	order := filepath.Join(t.TempDir(), "order")
	dir := runItem(t, checksHeader+
		"S1\tbl\tblocking\tB1\techo a >> "+order+"\texit 0\t\t\tfirst\n"+
		"J1\tjudge\tadvisory\t\techo b >> "+order+"\t\tfail if b is wrong\treading needed not a match\tjudge row\n"+
		"S2\tbl\tblocking\tB1\techo c >> "+order+"\texit 0\t\t\tthird\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	got, err := os.ReadFile(order)
	if err != nil {
		t.Fatalf("order: %v", err)
	}
	if strings.Join(strings.Fields(string(got)), "") != "abc" {
		t.Fatalf("the rows ran in the wrong order: %q", got)
	}
	rf, _ := readRunFile(t, dir, "A1-run1.json")
	if len(rf.Rows) != 3 {
		t.Fatalf("want three rows, got %d", len(rf.Rows))
	}
	for i, want := range []string{"S1", "J1", "S2"} {
		if rf.Rows[i].ID != want {
			t.Errorf("row %d = %s, want %s", i, rf.Rows[i].ID, want)
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

// R27: a row that writes under .backlog is refused — with no run file and no ledger event.
func TestRunRefusesARowThatWritesUnderBacklog(t *testing.T) {
	dir := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo x > .backlog/junk.txt\texit 0\t\t\twrites under .backlog\n")
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
	brief := writeFile(t, t.TempDir(), "brief.md", "B1: it works\n")
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
	checksOK(t, dir, roundList)
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
	checksOK(t, dir, roundList)
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
	checksOK(t, dir, list)
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
	dir := roundItem(t, roundList)
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
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	events, _ := readLedger(dir)
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
	checksOK(t, dir, roundList)
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
			// and the run succeeds: the item is opened, the list is frozen, the claims are
			// recorded, and the rows run.
			for _, argv := range [][]string{
				{"init"},
				{"file", "--id", "A1", "--text", "item"},
				{"open", "A1"},
			} {
				if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
					t.Fatalf("bl %v: exit %d", argv, code)
				}
			}
			checksOK(t, dir, roundList)
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
