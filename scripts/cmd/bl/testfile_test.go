package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── I89/R37: the frozen test file, and the format-2 row ─────────────────────

// testMainBlock is the run contract's main block (I89, B4): it prints `bl-test: <name>` as its
// first action, exits 1 on a failed assertion, 3 on any other exception and 4 on an unknown
// name.
const testMainBlock = `
if __name__ == "__main__":
    _name = sys.argv[1] if len(sys.argv) > 1 else ""
    print("bl-test: " + _name)
    _fn = globals().get(_name)
    if _name.startswith("_") or not callable(_fn):
        sys.exit(4)
    try:
        _fn()
    except AssertionError:
        sys.exit(1)
    except Exception:
        import traceback
        traceback.print_exc()
        sys.exit(3)
`

// testFileWith is a test file whose one public function is name, decorated with a `_covers`
// naming path.
func testFileWith(path, name string) string {
	return "import sys\n\n" +
		"def _covers(*paths):\n    def mark(fn):\n        return fn\n    return mark\n\n" +
		"@_covers(" + fmt.Sprintf("%q", path) + ", \"scripts/cmd/bl/main.go\")\n" +
		"def " + name + "():\n    assert True\n" + testMainBlock
}

func checksWithTests(t *testing.T, dir, list, tests string) error {
	t.Helper()
	return call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list), "--tests", tests)
}

// B1: blocking tests may cover code, while documentation clauses use judge rows.
func TestR37BlockingTestMustCoverCode(t *testing.T) {
	rows := func(severity string) string {
		return checksHeader + "T1\tbl\t" + severity + "\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	}
	testFile := func(covers string) string {
		return "import sys\n\ndef _covers(*paths):\n    return lambda fn: fn\n\n" + covers + "def a():\n    assert True\n" + testMainBlock
	}
	for _, tc := range []struct {
		name, row, source string
		refuse            bool
	}{
		{"docs only", rows("blocking"), testFile("@_covers(\"SKILL.md\")\n"), true},
		{"mixed paths", rows("blocking"), testFile("@_covers(\"SKILL.md\", \"scripts/cmd/bl/main.go\")\n"), false},
		{"no covers", rows("blocking"), testFile(""), false},
		{"advisory docs", rows("advisory"), testFile("@_covers(\"SKILL.md\")\n"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := openItem(t)
			if err := call(t, cmdSignoff, dir, "A1", "--by", "fixture", "--note", "fixture permits a list without a blocking bl row"); err != nil {
				t.Fatalf("fixture signoff: %v", err)
			}
			err := checksWithTests(t, dir, tc.row, writeFile(t, t.TempDir(), "tests.py", tc.source))
			if tc.refuse {
				r := wantRefusal(t, err)
				if r.rule != "R37" || !strings.Contains(r.msg, "row T1") || !strings.Contains(r.msg, "decider: judge row or an advisory row") {
					t.Fatalf("wrong refusal: %s: %s", r.rule, r.msg)
				}
			} else if err != nil {
				t.Fatalf("freeze: %v", err)
			}
		})
	}
	dir := openItem(t)
	checksOK(t, dir, goodList)
	list := rows("blocking")
	src := writeFile(t, t.TempDir(), "tests.py", testFile("@_covers(\"README.md\")\n"))
	r := wantRefusal(t, call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", list), "--tests", src, "--reason", "move to format 2"))
	if r.rule != "R37" || !strings.Contains(r.msg, "row T1") {
		t.Fatalf("wrong rebaseline refusal: %s: %s", r.rule, r.msg)
	}
}

// B1: --tests copies the file beside checks.tsv, and the event records its path and hash.
func TestR37ChecksCopiesAndHashesTheTestFile(t *testing.T) {
	dir := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "R1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\ttest a\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("checks --tests: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "tests.py"))
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(src)
	if string(got) != string(want) {
		t.Fatalf("tests.py is not byte-identical to the supplied file")
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.T != "checks" || ev.Format != 2 || ev.Tests != ".backlog/items/A1/tests.py" || ev.TestsSHA != shaHex(want) {
		t.Fatalf("checks event = %+v", ev)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// B2: a test row's expect is exactly `exit 0`, and a test-row attempt is never a shell row.
func TestR37ATestRowMustExitZero(t *testing.T) {
	dir := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "R1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\tstdout matches ok\t\t\ttest a\n"
	r := wantRefusal(t, checksWithTests(t, dir, list, src))
	if r.rule != "R37" || !strings.Contains(r.msg, "R1") || !strings.Contains(r.msg, "exit 0") {
		t.Fatalf("refusal = %+v", r)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "tests.py")); err == nil {
		t.Fatal("a refused freeze left the test file behind")
	}
}

func TestR37TestRowAttemptsAreRefused(t *testing.T) {
	for _, cmd := range []string{
		"python3  .backlog/items/A1/tests.py a",
		"python3 ./.backlog/items/A1/tests.py a",
		"python3 .backlog/items/A1/tests.py _a",
		"python3 .backlog/items/A1/tests.py a extra",
	} {
		t.Run(cmd, func(t *testing.T) {
			dir := openItem(t)
			src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
			list := checksHeader + "R1\tbl\tblocking\tB1\t" + cmd + "\texit 0\t\t\ttest a\n"
			r := wantRefusal(t, checksWithTests(t, dir, list, src))
			if r.rule != "R37" || !strings.Contains(r.msg, "R1") {
				t.Fatalf("refusal = %+v", r)
			}
		})
	}
}

// B3: the test file must parse, the rows and the file must agree, and a public top-level name
// is a test row's function.
func TestR37TestFileContract(t *testing.T) {
	good := testFileWith("SKILL.md", "a")
	two := "import sys\n\ndef _covers(*paths):\n    return lambda fn: fn\n\ndef a():\n    assert True\n\ndef b():\n    assert True\n" + testMainBlock
	cases := []struct {
		name, src, list, names string
	}{
		{
			"does not parse",
			"def a(:\n",
			checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n",
			"",
		},
		{
			"names a missing function",
			good,
			checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py c\texit 0\t\t\tc\n",
			"c",
		},
		{
			"two rows name one function",
			good,
			checksHeader +
				"T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n" +
				"T2\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n",
			"a",
		},
		{
			"a public def no row names",
			two,
			checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n",
			"b",
		},
		{
			"a public class",
			"import sys\n\ndef a():\n    assert True\n\nclass Helper:\n    pass\n" + testMainBlock,
			checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n",
			"Helper",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := openItem(t)
			src := writeFile(t, t.TempDir(), "t.py", c.src)
			r := wantRefusal(t, checksWithTests(t, dir, c.list, src))
			if r.rule != "R37" {
				t.Fatalf("want R37, got %s: %s", r.rule, r.msg)
			}
			if c.names != "" && !strings.Contains(r.msg, c.names) {
				t.Fatalf("the refusal must name %q: %s", c.names, r.msg)
			}
		})
	}
}

// B5: the freeze probe runs each test row once. Exit 0 and 1 are the contract; anything else,
// a timeout or a missing bl-test line is refused, and the test file is restored.
func TestR37FreezeProbe(t *testing.T) {
	raise := "import sys\n\ndef a():\n    raise NameError(\"boom\")\n" + testMainBlock
	fail := "import sys\n\ndef a():\n    assert False\n" + testMainBlock
	noMain := "def a():\n    assert True\n"
	badImport := "import does_not_exist\n\ndef a():\n    assert True\n" + testMainBlock
	junk := "import sys\n\ndef a():\n    open(\".backlog/items/A1/junk.txt\", \"w\").write(\"x\")\n" + testMainBlock
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	cases := []struct{ name, src, want string }{
		{"a raising test", raise, "exit 3"},
		{"a failed assertion", fail, ""},
		{"no main block", noMain, "no bl-test line"},
		{"an import that fails", badImport, "no bl-test line"},
		{"a test that writes .backlog", junk, "directory changed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := openItem(t)
			src := writeFile(t, t.TempDir(), "t.py", c.src)
			err := checksWithTests(t, dir, list, src)
			if c.want == "" {
				if err != nil {
					t.Fatalf("a test that exits 1 must be accepted: %v", err)
				}
				return
			}
			r := wantRefusal(t, err)
			if r.rule != "R37" || !strings.Contains(r.msg, c.want) {
				t.Fatalf("want R37 naming %q, got %s: %s", c.want, r.rule, r.msg)
			}
			if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "tests.py")); err == nil {
				t.Fatal("a refused freeze left the test file behind")
			}
		})
	}
}

// B5: a refusal restores the test file that was already frozen.
func TestR37ARefusedProbeRestoresTheFrozenTestFile(t *testing.T) {
	dir := openItem(t)
	good := testFileWith("SKILL.md", "a")
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, writeFile(t, t.TempDir(), "t.py", good)); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	bad := good + "\ndef extra():\n    raise NameError(\"boom\")\n"
	added := list + "T2\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py extra\texit 0\t\t\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", added), "--tests", writeFile(t, t.TempDir(), "t2.py", bad)))
	if r.rule != "R37" {
		t.Fatalf("want R37, got %s: %s", r.rule, r.msg)
	}
	got, _ := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "tests.py"))
	if string(got) != good {
		t.Fatalf("the frozen test file was not restored")
	}
}

// B6: a format-2 shell row is one simple command with an `exit <code>` expect.
func TestR37ShellRowsAreSimpleCommands(t *testing.T) {
	okRows := []string{
		"go -C scripts test ./...\texit 0",
		"echo \\$(date)\texit 0",
		"echo \"it's here\"\texit 0",
	}
	for _, row := range okRows {
		t.Run("ok "+row, func(t *testing.T) {
			checksOK(t, openItem(t), checksHeader+"S1\tbl\tblocking\tB1\t"+row+"\t\t\tc\n")
		})
	}
	badRows := []string{
		"cd scripts && go test ./...\texit 0",
		"go -C scripts test ./... | tail -1\texit 0",
		"echo \"it's\" | wc -l\texit 0",
		"grep -c x f\tstdout matches ^3$",
		"ls\texit 300",
	}
	for _, row := range badRows {
		t.Run("refused "+row, func(t *testing.T) {
			r := wantRefusal(t, call(t, cmdChecks, openItem(t), "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", checksHeader+"S1\tbl\tblocking\tB1\t"+row+"\t\t\tc\n")))
			if r.rule != "R37" || !strings.Contains(r.msg, "S1") {
				t.Fatalf("refusal = %+v", r)
			}
		})
	}
}

// B7: a test row covers a hunk or a bare path through its @_covers, never through its cmd.
func TestR37CoverageComesFromTheCoversDecorator(t *testing.T) {
	roundWith := func(t *testing.T, path, file string) error {
		t.Helper()
		dir := gitItemWith(t, map[string]string{"main.go": "one\n"})
		src := writeFile(t, t.TempDir(), "tests.py", testFileWith(path, "touch"))
		list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py touch\texit 0\t\t\tc\n"
		if err := checksWithTests(t, dir, list, src); err != nil {
			t.Fatalf("freeze: %v", err)
		}
		if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
			t.Fatalf("claims: %v", err)
		}
		writeFile(t, dir, file, "two\n")
		rows := runAndVerdicts(t, dir, "T1\tPASS\t")
		return call(t, cmdRound, dir, "A1", "--rows", rows)
	}
	r := wantRefusal(t, roundWith(t, "main.go", "main.go"))
	if r.rule != "R10" || !strings.Contains(r.msg, "green") {
		t.Fatalf("a test decorator alone must not cover a green code path: %+v", r)
	}
	r = wantRefusal(t, roundWith(t, "other.go", "main.go"))
	if r.rule != "R10" {
		t.Fatalf("want R10, got %s: %s", r.rule, r.msg)
	}
}

// B7: a file added with no textual hunk is covered by a test row's @_covers naming it.
func TestR37CoversAnAddedEmptyFile(t *testing.T) {
	dir := gitItemWith(t, nil)
	src := writeFile(t, t.TempDir(), "tests.py", testFileWith("e.txt", "touch"))
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py touch\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	writeFile(t, dir, "e.txt", "")
	rows := runAndVerdicts(t, dir, "T1\tPASS\t")
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--rows", rows))
	if r.rule != "R10" {
		t.Fatalf("a test decorator alone must not cover a green added path: %+v", r)
	}
}

// B8: the frozen test file is re-hashed at the point of use, and an amendment may only insert
// text before the frozen file's main-block line.
func TestR37TheFrozenTestFileIsRehashed(t *testing.T) {
	dir := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	frozen := filepath.Join(dir, ".backlog", "items", "A1", "tests.py")
	if err := os.WriteFile(frozen, append(mustRead(frozen), []byte("# edited\n")...), 0o644); err != nil {
		t.Fatal(err)
	}
	r := wantRefusal(t, call(t, cmdRun, dir, "A1"))
	if r.rule != "R20" || !strings.Contains(r.msg, "tests.py") {
		t.Fatalf("run refusal = %+v", r)
	}
}

func TestR37AmendInsertsBeforeTheMainBlock(t *testing.T) {
	dir := openItem(t)
	good := testFileWith("SKILL.md", "a")
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, writeFile(t, t.TempDir(), "t.py", good)); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	grown := strings.Replace(good, testMainBlock, "\ndef b():\n    assert True\n"+testMainBlock, 1)
	added := list + "T2\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py b\texit 0\t\t\tc\n"
	if err := call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", added), "--tests", writeFile(t, t.TempDir(), "t2.py", grown)); err != nil {
		t.Fatalf("an inserted def must be accepted: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.Format != 2 || ev.TestsSHA != shaHex([]byte(grown)) {
		t.Fatalf("amended event = %+v", ev)
	}

	// one changed byte is not an insertion
	changed := strings.Replace(good, "assert True", "assert Truth", 1)
	added2 := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n" +
		"T2\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py b\texit 0\t\t\tc\n" +
		"T3\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py b\texit 0\t\t\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", added2), "--tests", writeFile(t, t.TempDir(), "t3.py", changed)))
	if r.rule != "R37" {
		t.Fatalf("want R37, got %s: %s", r.rule, r.msg)
	}
}

// B8: an amendment cannot introduce a test file; only a re-baseline can.
func TestR37AmendCannotIntroduceATestFile(t *testing.T) {
	dir := openItem(t)
	checksOK(t, dir, goodList)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + goodRows + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--tests", src))
	if r.rule != "R37" || !strings.Contains(r.msg, "rebaseline") {
		t.Fatalf("refusal = %+v", r)
	}
}

// B9: a format-1 list is untouched. An amendment without --tests keeps format 1, and an
// amendment with --tests on it is refused.
func TestR37FormatOneIsUnchanged(t *testing.T) {
	dir := openItem(t)
	legacyChecks(t, dir, goodList)
	added := checksHeader + goodRows + "ADD1\tbl\tblocking\tB1\techo a\texit 0\t\t\tc\n"
	if err := call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", added)); err != nil {
		t.Fatalf("amend: %v", err)
	}
	events, _ := readLedger(dir)
	if ev := events[len(events)-1]; ev.T != "checks" || ev.Format != 1 || ev.Tests != "" {
		t.Fatalf("amended format-1 event = %+v", ev)
	}
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + goodRows + "ADD1\tbl\tblocking\tB1\techo a\texit 0\t\t\tc\n" +
		"ADD2\tbl\tblocking\tB1\techo a\texit 0\t\t\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c2.tsv", list), "--tests", src))
	if r.rule != "R37" || !strings.Contains(r.msg, "rebaseline") {
		t.Fatalf("refusal = %+v", r)
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	if byID["a1"].ChecksFormat != 1 {
		t.Fatalf("format = %d, want 1", byID["a1"].ChecksFormat)
	}
}

// B9: a format-2 list of judge and shell rows needs no test file, and a test file is refused
// for a list with no test row.
func TestR37ATestFileNeedsATestRow(t *testing.T) {
	dir := openItem(t)
	judgeOnly := checksHeader + "J1\tjudge\tblocking\tB1\techo x\t\tit fails\ta reading of the intent\tc\n"
	checksOK(t, dir, judgeOnly)
	events, _ := readLedger(dir)
	if ev := events[len(events)-1]; ev.Format != 2 || ev.Tests != "" {
		t.Fatalf("checks event = %+v", ev)
	}

	dir2 := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	r := wantRefusal(t, checksWithTests(t, dir2, judgeOnly, src))
	if r.rule != "R37" {
		t.Fatalf("want R37, got %s: %s", r.rule, r.msg)
	}

	// a carried-over test file with a re-baseline that has no test row is refused too
	dir3 := openItem(t)
	testRow := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir3, testRow, src); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	r = wantRefusal(t, call(t, cmdRebaseline, dir3, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", judgeOnly), "--reason", "no test row"))
	if r.rule != "R37" {
		t.Fatalf("want R37, got %s: %s", r.rule, r.msg)
	}
}

// B9: a format-1 list runs, rounds and checks exactly as before.
func TestR37FormatOneStillRuns(t *testing.T) {
	dir := gitItem(t)
	legacyChecks(t, dir, roundList)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("round: %v", err)
	}
	if err := call(t, cmdCheck, dir); err != nil {
		t.Fatalf("check: %v", err)
	}
}

// B10: the schema lists the new fields of a checks or rebaseline event.
func TestR37SchemaListsTheFormatAndTestFields(t *testing.T) {
	events := schemaJSON()["events"].(map[string]map[string][]string)
	for _, kind := range []string{"checks", "rebaseline"} {
		opt := strings.Join(events[kind]["optional"], ",")
		for _, field := range []string{"format", "tests", "tests_sha"} {
			if !strings.Contains(opt, field) {
				t.Errorf("the %s event's optional fields must name %s: %v", kind, field, events[kind]["optional"])
			}
		}
	}
}

// B8: a module docstring that contains the main-block text does not move the split.
func TestR37ADocstringDoesNotMoveTheMainBlock(t *testing.T) {
	dir := openItem(t)
	good := "\"\"\"\nif __name__ == \"__main__\":\n\"\"\"\n" + testFileWith("SKILL.md", "a")
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, writeFile(t, t.TempDir(), "t.py", good)); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	grown := strings.Replace(good, testMainBlock, "\ndef b():\n    assert True\n"+testMainBlock, 1)
	added := list + "T2\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py b\texit 0\t\t\tc\n"
	if err := call(t, cmdChecks, dir, "A1", "--amend", "--file", writeFile(t, t.TempDir(), "c.tsv", added), "--tests", writeFile(t, t.TempDir(), "t2.py", grown)); err != nil {
		t.Fatalf("the docstring moved the split: %v", err)
	}
}

// B8: `bl check` reports a missing or changed test file as a problem, not a refusal, and keeps
// checking the other items.
func TestR37CheckReportsATestFileProblem(t *testing.T) {
	dir := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, ".backlog", "items", "A1", "tests.py")); err != nil {
		t.Fatal(err)
	}
	var err error
	out := capture(t, func() { err = call(t, cmdCheck, dir) })
	if !errors.Is(err, errFailed) {
		t.Fatalf("check = %v, want a problem", err)
	}
	if !strings.Contains(out, "FAIL A1 tests .backlog/items/A1/tests.py is missing") {
		t.Fatalf("check output = %q", out)
	}
}

// B8: a re-baseline without --tests keeps the frozen test file and carries its hash.
func TestR37RebaselineCarriesTheTestFile(t *testing.T) {
	dir := openItem(t)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("freeze: %v", err)
	}
	want, _ := os.ReadFile(src)
	if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--reason", "same premise, new list"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.Format != 2 || ev.Tests != ".backlog/items/A1/tests.py" || ev.TestsSHA != shaHex(want) {
		t.Fatalf("rebaseline event = %+v", ev)
	}
}

// B9: `bl rebaseline --tests` moves a format-1 list to format 2.
func TestR37RebaselineMovesFormatOneToTwo(t *testing.T) {
	dir := openItem(t)
	legacyChecks(t, dir, goodList)
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	if err := call(t, cmdRebaseline, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list), "--tests", src, "--reason", "the premise moved"); err != nil {
		t.Fatalf("rebaseline: %v", err)
	}
	events, _ := readLedger(dir)
	ev := events[len(events)-1]
	if ev.Format != 2 || ev.TestsSHA != shaHex(mustRead(src)) {
		t.Fatalf("rebaseline event = %+v", ev)
	}
}

// B9: a freeze with a test row and no test file is refused.
func TestR37ATestRowNeedsATestFile(t *testing.T) {
	dir := openItem(t)
	list := checksHeader + "T1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/tests.py a\texit 0\t\t\tc\n"
	r := wantRefusal(t, call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "c.tsv", list)))
	if r.rule != "R37" || !strings.Contains(r.msg, "test file") {
		t.Fatalf("refusal = %+v", r)
	}
}

// R36 + I89: attempt 2's test file lands beside its own checks.tsv, in a2/, and attempt 1's
// test path is never written.
func TestARestartPutsTheNextAttemptsTestFileUnderA2(t *testing.T) {
	dir := restartItem(t)
	restartOK(t, dir, "the list is the wrong premise", "a list that runs a test file")
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	src := writeFile(t, t.TempDir(), "t.py", testFileWith("SKILL.md", "a"))
	list := checksHeader + "R1\tbl\tblocking\tB1\tpython3 .backlog/items/A1/a2/tests.py a\texit 0\t\t\ttest a\n"
	if err := checksWithTests(t, dir, list, src); err != nil {
		t.Fatalf("checks --tests in attempt 2: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, ".backlog", "items", "A1", "a2", "tests.py"))
	if err != nil {
		t.Fatalf("attempt 2's test file is not in a2/: %v", err)
	}
	want, _ := os.ReadFile(src)
	if string(got) != string(want) {
		t.Fatalf("a2/tests.py is not byte-identical to the supplied file")
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if ev.T != "checks" || ev.Tests != ".backlog/items/A1/a2/tests.py" || ev.TestsSHA != shaHex(want) {
		t.Fatalf("checks event = %+v", ev)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "tests.py")); err == nil {
		t.Fatal("attempt 2 wrote attempt 1's test path")
	}
}
