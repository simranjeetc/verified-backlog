package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
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
	if len(events) != 12 {
		t.Errorf("want 12 event kinds, got %d", len(events))
	}
	for _, kind := range []string{"checks", "claims"} {
		if _, ok := events[kind]; !ok {
			t.Errorf("schema is missing event kind %q", kind)
		}
	}
	for _, kind := range []string{"brief", "rebaseline"} {
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
	if len(kinds) != 12 {
		t.Errorf("want 12 event kinds in the schema output, got %d: %v", len(kinds), kinds)
	}
	for _, kind := range []string{"brief", "rebaseline"} {
		if !kinds[kind] {
			t.Errorf("the schema output is missing event kind %q", kind)
		}
	}
}

// The schema output reports how many event kinds it lists, so a reader (and a grep) can tell
// 12 from 11 without counting the lines.
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("round %d: %v", i+1, err)
		}
	}
	// the cap is spent
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R19")

	// a re-baseline is a new premise: the counts are not
	if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "the list's own command was broken"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
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

// R2 at use: a frozen row whose brief_ref names a clause the current brief no longer holds is a
// refusal — the row's criterion no longer exists in the brief it was written against (R24).
// The check runs where the round path reads the frozen artefacts, so a round cannot pass on a
// list whose brief_ref has gone missing.
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
	if r.rule != "R2" {
		t.Fatalf("want a refusal under R2, got %s: %s", r.rule, r.msg)
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
	wantRule(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows), "R2")
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	// a dispute round is a round too, and the budget still has room
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"))
}

// R19: three dispute rounds per item, and a fourth is refused under R19.
func TestR19TheFourthDisputeRoundIsRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
			t.Fatalf("dispute round %d: %v", i+1, err)
		}
	}
	// a harness round is a round too
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows))
}

// R19: six rounds in total, of any cause. The seventh is refused under R19 although no cause
// has spent three rounds.
func TestR19TheSeventhRoundIsRefused(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	rounds := []struct{ cause string }{
		{"code"}, {"code"}, {"harness"}, {"harness"}, {"dispute"}, {"dispute"},
	}
	for i, r := range rounds {
		argv := []string{"A1", "--verdict", "FAIL", "--cause", r.cause}
		if r.cause != "harness" {
			argv = append(argv, "--rows", rows)
		}
		if err := call(t, cmdRound, dir, argv...); err != nil {
			t.Fatalf("round %d (%s): %v", i+1, r.cause, err)
		}
	}
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: a PASS round is a round too — it counts towards the six, and the seventh is refused.
// The refusal is the state guard's (R18): the item's last round is a PASS, so the only
// legal next step is the close.
func TestR19ACountsAPassRoundTowardsTheTotal(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	// the sixth round is a PASS and it is accepted
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < 2; i++ {
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
		list := writeFile(t, t.TempDir(), "c.tsv", rebaselinedList)
		if err := call(t, cmdRebaseline, dir, "A1", "--file", list, "--reason", "the list's own command was broken"); err != nil {
			t.Fatalf("rebaseline %d: %v", i+1, err)
		}
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
			t.Fatalf("dispute round %d: %v", i+1, err)
		}
	}
	// six rounds are spent, and the seventh is refused whatever the re-baselines did
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: the same holds when the item has claims. The claims freeze the check list (R5), so the
// re-baseline is refused — and either way the counts are not reset: the fourth code round is
// refused under R19.
func TestR19TheCountsSurviveARebaselineRefusedUnderR5(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	// the claims freeze the check list (R5): the re-baseline is refused
	wantRule(t, call(t, cmdRebaseline, dir, "A1",
		"--file", writeFile(t, t.TempDir(), "c.tsv", rebaselinedList), "--reason", "again"), "R5")
	// and the counts are not reset: the fourth code round is refused under R19
	wantBudgetRefusal(t, call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows))
}

// R19: a refused round exits 2, writes no event, and leaves the item's state untouched.
func TestR19ARefusedRoundWritesNothing(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < roundCap; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	before, _ := readLedger(dir)
	if code := run([]string{"--root", dir, "round", "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows}); code != 2 {
		t.Fatalf("bl round: exit = %d, want 2", code)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatalf("a refused round wrote %d events", len(after)-len(before))
	}
	if last := after[len(after)-1]; last.T != "round" {
		t.Fatalf("a refused round wrote %+v", last)
	}
}

// R19: `bl status` shows the round counts — the total and how many of each cause, so the
// harness and dispute rounds are visible.
func TestStatusShowsTheHarnessAndDisputeCounts(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < 2; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
			t.Fatalf("harness round %d: %v", i+1, err)
		}
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i := 0; i < 3; i++ {
		if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
			t.Fatalf("code round %d: %v", i+1, err)
		}
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "harness"); err != nil {
		t.Fatalf("harness round: %v", err)
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "dispute", "--rows", rows); err != nil {
		t.Fatalf("dispute round: %v", err)
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS"); err != nil {
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	for i, cause := range []string{"code", "harness", "dispute", "code", "dispute"} {
		argv := []string{"A1", "--verdict", "FAIL", "--cause", cause}
		if cause != "harness" {
			argv = append(argv, "--rows", rows)
		}
		if err := call(t, cmdRound, dir, argv...); err != nil {
			t.Fatalf("round %d (%s): %v", i+1, cause, err)
		}
	}
	// the sixth round is a PASS
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS"); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", rows); err != nil {
		t.Fatalf("code round: %v", err)
	}
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS"); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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
	dir := openItem(t)
	checksOK(t, dir, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tFAIL\n")
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS"); err != nil {
		t.Fatalf("pass round: %v", err)
	}
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

// R23: a PASS round carries no cause and no finding, so it needs no rows: it may be recorded
// without any, and then it has no evidence file to hash.
func TestAPassRoundIsRecordedWithoutRows(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS"); err != nil {
		t.Fatalf("pass round: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "round" || ev.Verdict != "PASS" || ev.Cause != "" {
		t.Fatalf("round event = %+v", ev)
	}
	if ev.Rows != "" || ev.RowsSHA != "" {
		t.Fatalf("a PASS round without rows carries none: %+v", ev)
	}
	if files := evidenceFiles(t, dir); len(files) != 0 {
		t.Fatalf("a PASS round without rows wrote an evidence file: %v", files)
	}
	// a PASS written against its evidence still carries it — in another item, because a PASS
	// ends an item's rounds (R18)
	dir2 := openItem(t)
	checksOK(t, dir2, goodList)
	rows := writeFile(t, t.TempDir(), "rows.txt", "S1\tPASS\n")
	if err := call(t, cmdRound, dir2, "A1", "--verdict", "PASS", "--rows", rows); err != nil {
		t.Fatalf("pass round with rows: %v", err)
	}
	events, _ = readLedger(dir2)
	ev = events[len(events)-1]
	if ev.Rows == "" || ev.RowsSHA == "" {
		t.Fatalf("a PASS round with rows carries them: %+v", ev)
	}
	// and the ledger still verifies: a round with no rows has no evidence file to check
	if err := call(t, cmdCheck, dir2); err != nil {
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
)

// p1Order is the order §4 gives for `bl checks`: the rules are evaluated over the whole list, in
// this order, and the first broken rule ends the command.
var p1Order = []string{"R1", "R5", "R4", "R2", "R3", "R8"}

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
	}
	return ""
}

// P1: where two rules can both match, the one that comes first in the order wins — over the
// whole list, and whatever order the rows sit in.
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
	for _, rule := range []string{"R1", "R2", "R3", "R8"} {
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
