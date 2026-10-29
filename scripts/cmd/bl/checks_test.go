package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── checks and claims ────────────────────────────────────────────────────────

const checksHeader = "id\tdecider\tseverity\tbrief_ref\tcmd\texpect\tfail_if\twhy_not_mech\tcriterion\n"

// openItem initialises a ledger in a temp dir, files A1 with a two-clause text and opens it.
func openItem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{
		{"init"},
		{"file", "--id", "A1", "--text", "add a verb Example: adds a verb; test it Example: runs the test"},
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
	rows, err := parseChecks(list)
	if err == nil {
		qualifies := false
		for _, row := range rows {
			if row.f["severity"] == "blocking" && row.f["decider"] == "bl" {
				qualifies = true
				break
			}
		}
		if !qualifies {
			if err := call(t, cmdSignoff, dir, "A1", "--by", "fixture", "--note", "fixture permits this legacy judge-only list"); err != nil {
				t.Fatalf("fixture signoff: %v", err)
			}
		}
	}
	if err := call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list)); err != nil {
		t.Fatalf("checks: %v", err)
	}
}

func checksRefused(t *testing.T, dir, list, rule string, extra ...string) {
	t.Helper()
	argv := append([]string{"A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list)}, extra...)
	wantRule(t, call(t, cmdChecks, dir, argv...), rule)
}

// legacyChecks freezes a check list the way a list frozen before I89 was frozen: it writes the
// list and appends a `checks` event with no `format` field, which is format 1 (I89, B1, B9).
// The format-2 write path is `checksOK`; fixtures that need a format-1 list — R8's five
// expect forms, R34's shell dialect, a row that is a shell script — use this instead, because
// those rules still apply to a format-1 list and no new list can be frozen as one.
func legacyChecks(t *testing.T, dir, list string) {
	t.Helper()
	rel := itemFile("A1", checksName)
	if err := atomicWrite(filepath.Join(dir, rel), list); err != nil {
		t.Fatal(err)
	}
	if _, err := appendEvent(dir, Event{T: "checks", ID: "A1", Checks: rel, ChecksSHA: shaHex([]byte(list))}); err != nil {
		t.Fatal(err)
	}
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
	if string(data) != "B1: add a verb Example: adds a verb\nB2: test it Example: runs the test\n" {
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

func TestChecksR15RequiresBlockingBLOrSignoff(t *testing.T) {
	dir := openItem(t)
	list := checksHeader + "J1\tjudge\tblocking\tB1\techo x\t\tit fails\ta reading of the intent\tc\n"
	before := ledgerBytes(t, dir)
	err := call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list))
	wantRule(t, err, "R15")
	if !strings.Contains(err.Error(), "links resolve, examples run") || !strings.Contains(err.Error(), "bl signoff") {
		t.Fatalf("R15 refusal should suggest a mechanical row or sign-off: %v", err)
	}
	if after := ledgerBytes(t, dir); after != before {
		t.Fatal("a refused check list changed the ledger")
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "checks.tsv")); err == nil {
		t.Fatal("a refused check list was stored")
	}
	if err := call(t, cmdSignoff, dir, "A1", "--by", "h", "--note", "n"); err != nil {
		t.Fatalf("signoff: %v", err)
	}
	checksOK(t, dir, list)
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

// R8's five forms are the format-1 expectation rule (I89, B10): a format-2 `decider: bl` row
// is governed by R37 instead. The write path is exercised through a re-baseline onto a
// format-1 item, which keeps R8's checks.
func TestChecksR8ExpectForms(t *testing.T) {
	for _, expect := range []string{
		"exit 0", "exit 255", "stdout matches ^ok", "stderr matches (?i)error", "file exists scripts/go.mod",
		"tree == 0123456789abcdef0123456789abcdef01234567",
	} {
		t.Run("ok "+expect, func(t *testing.T) {
			dir := openItem(t)
			legacyChecks(t, dir, rebaselinedList)
			list := checksHeader + "S1\tbl\tblocking\tB1\tx\t" + expect + "\t\t\tc\n"
			if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "the premise moved"); err != nil {
				t.Fatalf("R8 must accept %q: %v", expect, err)
			}
		})
	}
	for _, expect := range []string{
		"exit -1", "exit 256", "exit zero", "exit 01", "returns 0", "stdout contains ok", "stdout matches (",
		"file exists /etc/passwd", "file exists ../x", "tree == ABC", "Exit 0",
	} {
		t.Run("refused "+expect, func(t *testing.T) {
			dir := openItem(t)
			legacyChecks(t, dir, rebaselinedList)
			list := checksHeader + "S1\tbl\tblocking\tB1\tx\t" + expect + "\t\t\tc\n"
			r := wantRefusal(t, call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "the premise moved"))
			if r.rule != "R8" {
				t.Fatalf("want a refusal under R8, got %s: %s", r.rule, r.msg)
			}
		})
	}
}

func TestChecksR4WrittenOnce(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	checksRefused(t, dir, goodList, "R4")

	// an amendment may not change severity
	checksRefused(t, dir, strings.Replace(goodList, "\tjudge\tadvisory\t", "\tjudge\tblocking\tB1", 1), "R4", "--amend")
	// nor remove a row, change cmd, or reclassify bl → judge
	checksRefused(t, dir, checksHeader+"S1\tbl\tblocking\tB1\tgo test ./...\texit 0\t\t\ttests pass\n", "R4", "--amend")
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
