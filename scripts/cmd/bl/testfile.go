package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// ── R37: the test file, and the format-2 row ─────────────────────────────────

// testRowRe matches a test row's cmd exactly (I89, B2): `python3 <tests path> <name>`, with
// single spaces, the path being the item's frozen test file, and the name a Python function
// name that does not start with an underscore.
var testRowRe = regexp.MustCompile(`^python3 (\S+) ([A-Za-z][A-Za-z0-9_]*)$`)

// testPath is the frozen test file's repository-relative path (I89, B1): beside the attempt's
// checks.tsv, so attempt n >= 2 keeps its file under a<n>/ (R36) and attempt 1 keeps the path
// it has always had, `.backlog/items/<id>/tests.py`.
func testPath(id string, attempt int) string { return attemptFile(id, attempt, testsName) }

// testRowName reports whether a row of the given format is a test row (I89, B2) and, if it is,
// the test function it names. A test row is a format-2 `decider: bl` row whose cmd is exactly
// `python3 <the item's tests path> <name>`.
func testRowName(format int, it *Item, r checkRow) (string, bool) {
	if format != 2 || r.f["decider"] != "bl" {
		return "", false
	}
	m := testRowRe.FindStringSubmatch(r.f["cmd"])
	if m == nil || m[1] != testPath(it.ID, it.Attempt) {
		return "", false
	}
	return m[2], true
}

// testRowAttempt reports whether a row is a test-row ATTEMPT (I89, B2): a format-2
// `decider: bl` row whose cmd's first word is `python3` and which contains `.backlog/items/`.
// Such a row must be exactly the test-row form; it is never treated as a shell row.
func testRowAttempt(format int, it *Item, r checkRow) bool {
	if format != 2 || r.f["decider"] != "bl" {
		return false
	}
	fields := strings.Fields(r.f["cmd"])
	return len(fields) > 0 && fields[0] == "python3" && strings.Contains(r.f["cmd"], backlogDir+"/"+itemsDir+"/")
}

// validExitExpect reports whether an expect is B6's `exit <code>`: the integer 0..255 form R8
// defines, checked under R37's tag.
func validExitExpect(expect string) bool {
	if !strings.HasPrefix(expect, "exit ") {
		return false
	}
	code := strings.TrimPrefix(expect, "exit ")
	n, err := strconv.Atoi(code)
	return err == nil && exitCodeRe.MatchString(code) && n <= 255
}

// shellMeta reports the first character outside quotes that makes a format-2 shell row more
// than one simple command (I89, B6), and whether there is one. It reads quotes and backslashes
// exactly as escapedPipe (R34) does: inside a single-quoted string everything is text; inside
// a double-quoted string a backslash escapes the next character and a quote of the other kind
// is text; outside quotes a backslash escapes the next character.
func shellMeta(cmd string) (string, bool) {
	var quote byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case c == '|' || c == ';' || c == '&' || c == '<' || c == '>' || c == '`' || c == '\n':
			return string(c), true
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
			return "$(", true
		}
	}
	return "", false
}

// testFile is what bl reads of a test file without running it (I89, B3, B4): its top-level
// defs and classes, the paths each def's `@_covers(...)` names, and the line of the
// module-level `if __name__ == "__main__":` block.
type testFile struct {
	defs     map[string]testDef
	classes  map[string]bool
	mainLine int
}

type testDef struct{ covers []string }

// testFileScript reads the file with python3's ast and prints one JSON object. It never runs
// the file: the run contract is the template's main block, not bl's.
const testFileScript = `import ast, json, sys

path = sys.argv[1]
try:
    with open(path, "rb") as fh:
        src = fh.read()
    tree = ast.parse(src, filename=path)
except SyntaxError as exc:
    print(json.dumps({"ok": False, "error": "%s (line %s)" % (exc.msg, exc.lineno)}))
    sys.exit(0)


def string_args(node):
    out = []
    for a in node.args:
        if isinstance(a, ast.Constant) and isinstance(a.value, str):
            out.append(a.value)
    return out


defs = []
classes = []
main_line = 0
lines = src.split(b"\n")
for node in tree.body:
    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
        covers = []
        for d in node.decorator_list:
            if isinstance(d, ast.Call) and isinstance(d.func, ast.Name) and d.func.id == "_covers":
                covers.extend(string_args(d))
        defs.append({"name": node.name, "line": node.lineno, "covers": covers})
    elif isinstance(node, ast.ClassDef):
        classes.append({"name": node.name, "line": node.lineno})
    elif isinstance(node, ast.If) and main_line == 0 and 0 < node.lineno <= len(lines):
        text = lines[node.lineno - 1].decode("utf-8", "replace").rstrip()
        if "#" in text:
            text = text.split("#", 1)[0].rstrip()
        if text == 'if __name__ == "__main__":':
            main_line = node.lineno
print(json.dumps({"ok": True, "defs": defs, "classes": classes, "main_line": main_line}))
`

// testFileReport is the JSON testFileScript prints.
type testFileReport struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Defs  []struct {
		Name   string   `json:"name"`
		Line   int      `json:"line"`
		Covers []string `json:"covers"`
	} `json:"defs"`
	Classes []struct {
		Name string `json:"name"`
		Line int    `json:"line"`
	} `json:"classes"`
	MainLine int `json:"main_line"`
}

// parseTestFile reads a test file with python3's ast, without running it (I89, B3, B4).
func parseTestFile(root, rel string) (testFile, error) {
	tf := testFile{defs: map[string]testDef{}, classes: map[string]bool{}}
	cmd := exec.Command("python3", "-c", testFileScript, filepath.Join(root, filepath.FromSlash(rel)))
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return tf, refuseRule("R37", "python3 could not read the test file %s: %v: %s", rel, err, strings.TrimSpace(errOut.String()))
	}
	var rep testFileReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		return tf, refuseRule("R37", "python3 returned no test-file report for %s: %v", rel, err)
	}
	if !rep.OK {
		return tf, refuseRule("R37", "the test file %s does not parse: %s", rel, rep.Error)
	}
	for _, d := range rep.Defs {
		tf.defs[d.Name] = testDef{covers: d.Covers}
	}
	for _, c := range rep.Classes {
		tf.classes[c.Name] = true
	}
	tf.mainLine = rep.MainLine
	return tf, nil
}

// mainLineOffset returns the byte offset of the start of line n (1-based) in data, which is
// where B8's insert-only amendment puts its text.
func mainLineOffset(data []byte, line int) (int, bool) {
	if line < 1 {
		return 0, false
	}
	off := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
	}
	return off, true
}

// checkInsertOnly is B8's amendment rule: the new test file is the frozen one with new text
// inserted at exactly one point, immediately before the frozen file's main-block line.
func checkInsertOnly(frozen, next []byte, mainLine int) error {
	off, ok := mainLineOffset(frozen, mainLine)
	if mainLine == 0 || !ok {
		return refuseRule("R37", "the frozen test file has no `if __name__ == \"__main__\":` line to insert before")
	}
	if !bytes.HasPrefix(next, frozen[:off]) || !bytes.HasSuffix(next, frozen[off:]) {
		return refuseRule("R37", "an amended test file may only insert text at one point, immediately before the frozen file's main-block line — it may not change or remove the frozen text")
	}
	return nil
}

// validateR37Rows applies R37's row rules to a list: B2 (a test row's exact form and its
// `exit 0` expect), B6 (a shell row is one simple command with an `exit <code>` expect), and
// the test-row attempt. In a format-1 list no row is a test row and none of this applies.
func validateR37Rows(it *Item, rows []checkRow, format int) error {
	for _, r := range rows {
		if format != 2 || r.f["decider"] != "bl" {
			continue
		}
		if _, ok := testRowName(format, it, r); ok {
			if r.f["expect"] != "exit 0" {
				return refuseRule("R37", "row %s: a test row's expect is exactly `exit 0`, not %q", r.f["id"], r.f["expect"])
			}
			continue
		}
		if testRowAttempt(format, it, r) {
			return refuseRule("R37", "row %s: a test row is exactly `python3 %s <name>` — single spaces, the item's test file, and a name matching [A-Za-z][A-Za-z0-9_]*, not %q", r.f["id"], testPath(it.ID, it.Attempt), r.f["cmd"])
		}
		if !validExitExpect(r.f["expect"]) {
			return refuseRule("R37", "row %s: a format-2 shell row's expect is exactly `exit <code>` for 0-255, not %q", r.f["id"], r.f["expect"])
		}
		if meta, ok := shellMeta(r.f["cmd"]); ok {
			return refuseRule("R37", "row %s: a format-2 shell row is one simple command — %q is outside quotes", r.f["id"], meta)
		}
	}
	return nil
}

// validateR37Presence is B9: a test row needs a frozen test file, and a frozen test file needs
// at least one test row. A format-2 list of judge and shell rows only carries no test file.
func validateR37Presence(it *Item, rows []checkRow, format int, willFreeze bool) error {
	n := 0
	for _, r := range rows {
		if _, ok := testRowName(format, it, r); ok {
			n++
		}
	}
	if n > 0 && !willFreeze {
		return refuseRule("R37", "%s: the list has a test row, so a test file must be frozen — pass --tests", it.ID)
	}
	if willFreeze && n == 0 {
		return refuseRule("R37", "%s: a frozen test file must be used by at least one test row — this list has none", it.ID)
	}
	return nil
}

// validateR37File applies B3 to a test file: every test row names a top-level def, no two rows
// name the same def, and every public top-level def or class is named by a row. A helper's
// name starts with `_`, and a helper is never named by a row.
func validateR37File(it *Item, rows []checkRow, format int, tf testFile) error {
	named := map[string]bool{}
	for _, r := range rows {
		name, ok := testRowName(format, it, r)
		if !ok {
			continue
		}
		if named[name] {
			return refuseRule("R37", "rows name the same test function %q twice", name)
		}
		if _, ok := tf.defs[name]; !ok {
			if tf.classes[name] {
				return refuseRule("R37", "row %s names %q, which is a class, not a top-level def", r.f["id"], name)
			}
			return refuseRule("R37", "row %s names %q, which is not a top-level def in the test file", r.f["id"], name)
		}
		named[name] = true
	}
	defs := make([]string, 0, len(tf.defs))
	for name := range tf.defs {
		defs = append(defs, name)
	}
	sort.Strings(defs)
	for _, name := range defs {
		if strings.HasPrefix(name, "_") || named[name] {
			continue
		}
		return refuseRule("R37", "the test file defines %q, which no test row names — a helper's name starts with _", name)
	}
	classes := make([]string, 0, len(tf.classes))
	for name := range tf.classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	for _, name := range classes {
		if strings.HasPrefix(name, "_") {
			continue
		}
		return refuseRule("R37", "the test file defines the public class %q — a helper's name starts with _", name)
	}
	return nil
}

// probeTests is B5: every test row of the list runs once on the current tree, exactly as
// `bl run` runs a row, and the tree and .backlog/ must not move. A test exits 0 when its
// assertion holds and 1 when it is false; any other exit, a timeout, or a missing
// `bl-test: <name>` line means the test itself is broken and the list is refused. The probe
// writes no run file and no event.
func probeTests(root string, it *Item, rows []checkRow, format int) error {
	var probe []checkRow
	for _, r := range rows {
		if _, ok := testRowName(format, it, r); ok {
			probe = append(probe, r)
		}
	}
	if len(probe) == 0 {
		return nil
	}
	treeBefore, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerBefore, err := backlogHash(root)
	if err != nil {
		return err
	}
	env, cleanup, err := execEnv(root)
	if err != nil {
		return err
	}
	defer cleanup()
	var rowErr error
	for _, r := range probe {
		name, _ := testRowName(format, it, r)
		rec := execRow(root, r, env, runTimeout)
		switch {
		case rec.TimedOut:
			rowErr = refuseRule("R37", "row %s: timeout — the test did not finish", r.f["id"])
		case rec.Exit != 0 && rec.Exit != 1:
			rowErr = refuseRule("R37", "row %s: exit %d — a test exits 0 or 1; any other code means the test itself is broken", r.f["id"], rec.Exit)
		case !hasTestLine(rec.Stdout, name):
			rowErr = refuseRule("R37", "row %s: no bl-test line — the test did not print `bl-test: %s`", r.f["id"], name)
		}
		if rowErr != nil {
			break
		}
	}
	// The state checks run after the last row that ran, in every case; the refusal names the
	// first failing condition: the row, then the tree, then .backlog/.
	treeAfter, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerAfter, err := backlogHash(root)
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if treeAfter != treeBefore {
		return refuseRule("R37", "the tree moved under the test probe: %s before, %s after — a test changed the tree it ran against", treeBefore, treeAfter)
	}
	if ledgerAfter != ledgerBefore {
		return refuseRule("R37", "the %s directory changed under the test probe: %s before, %s after — a test wrote to the ledger, the evidence or the item files", backlogDir, ledgerBefore, ledgerAfter)
	}
	return nil
}

// hasTestLine reports whether stdout holds the line `bl-test: <name>` the run contract prints
// as its first action (I89, B4, B5).
func hasTestLine(stdout, name string) bool {
	want := "bl-test: " + name
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			return true
		}
	}
	return false
}

// frozenTests is the test file a freeze writes (I89, B1): whether one is frozen at all, and
// the bytes the next `checks` or `rebaseline` event records a hash of.
type frozenTests struct {
	set  bool
	data []byte
}

// validateR37 applies R37's block to a list that is about to be frozen, in the order B5 gives:
// the row rules (B2, B6, B9), then the test file's parse and amendment checks (B3, B8), then
// the freeze probe (B5). It returns the test file the command will freeze, if one is frozen.
//
// supplied is the file given with --tests; suppliedSet says the flag was given, so an empty
// file is still supplied. carry says a list already frozen may lend its test file to the new
// event (an amendment or a re-baseline without --tests). insertOnly is the amendment rule of
// B8: a new test file may only insert text before the frozen file's main-block line.
func validateR37(root string, it *Item, rows []checkRow, format int, supplied []byte, suppliedSet, carry, insertOnly bool) (frozenTests, error) {
	var frozen frozenTests
	// B9: a test file is format 2's, and only a re-baseline can move a format-1 list to it.
	if suppliedSet && format == 1 {
		return frozen, refuseRule("R37", "%s: a test file needs a format-2 list — only `bl rebaseline` can move a list to format 2", it.ID)
	}
	// B8: an amendment may grow the frozen test file in one point, never introduce one.
	if suppliedSet && insertOnly && it.TestsSHA == "" {
		return frozen, refuseRule("R37", "%s has no frozen test file — an amendment may only grow one; only `bl rebaseline` can introduce one", it.ID)
	}
	if err := validateR37Rows(it, rows, format); err != nil {
		return frozen, err
	}
	willFreeze := format == 2 && (suppliedSet || (carry && it.TestsSHA != ""))
	if err := validateR37Presence(it, rows, format, willFreeze); err != nil {
		return frozen, err
	}
	if !willFreeze {
		return frozen, nil
	}
	rel := testPath(it.ID, it.Attempt)
	abs := filepath.Join(root, rel)
	before, readErr := os.ReadFile(abs)
	existed := readErr == nil
	// B8: the insertion point is the FROZEN file's main-block line, so it is read before the
	// new file replaces it.
	mainLine := 0
	if insertOnly && suppliedSet {
		tf, err := parseTestFile(root, rel)
		if err != nil {
			return frozen, err
		}
		mainLine = tf.mainLine
	}
	if suppliedSet {
		if err := atomicWrite(abs, string(supplied)); err != nil {
			return frozen, err
		}
	}
	restore := func() {
		if existed {
			_ = atomicWrite(abs, string(before))
		} else {
			_ = os.Remove(abs)
		}
	}
	// B3: the file that will be frozen after this command is the supplied one, or the frozen
	// one carried over.
	data := supplied
	if !suppliedSet {
		d, err := os.ReadFile(abs)
		if err != nil {
			restore()
			return frozen, refuseRule("R20", "the test file %s is missing", rel)
		}
		if shaHex(d) != it.TestsSHA {
			restore()
			return frozen, refuseRule("R20", "the test file %s no longer matches the hash recorded in the ledger", rel)
		}
		data = d
	}
	if insertOnly && suppliedSet {
		if err := checkInsertOnly(before, supplied, mainLine); err != nil {
			restore()
			return frozen, err
		}
	}
	tf, err := parseTestFile(root, rel)
	if err != nil {
		restore()
		return frozen, err
	}
	if err := validateR37File(it, rows, format, tf); err != nil {
		restore()
		return frozen, err
	}
	if err := validateR37BlockingTestCoverage(it, rows, format, tf); err != nil {
		restore()
		return frozen, err
	}
	if err := probeTests(root, it, rows, format); err != nil {
		restore()
		return frozen, err
	}
	frozen.set, frozen.data = true, data
	return frozen, nil
}

// validateR37BlockingTestCoverage refuses a blocking test row whose covers are only docs.
func validateR37BlockingTestCoverage(it *Item, rows []checkRow, format int, tf testFile) error {
	if format != 2 {
		return nil
	}
	for _, r := range rows {
		name, ok := testRowName(format, it, r)
		if !ok || r.f["severity"] != "blocking" {
			continue
		}
		def := tf.defs[name]
		if len(def.covers) == 0 {
			continue
		}
		docsOnly := true
		for _, path := range def.covers {
			if !strings.HasSuffix(path, ".md") {
				docsOnly = false
				break
			}
		}
		if docsOnly {
			return refuseRule("R37", "row %s: a blocking test row must exercise code; a docs clause is a decider: judge row or an advisory row", r.f["id"])
		}
	}
	return nil
}
