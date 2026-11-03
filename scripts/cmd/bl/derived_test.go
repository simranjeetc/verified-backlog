package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestChangedPathsUsesNulDelimitedNamesAndExemptsTests(t *testing.T) {
	root := t.TempDir()
	command := func(args ...string) {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		if b, e := c.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s: %v", args, b, e)
		}
	}
	command("init", "-q")
	command("config", "user.email", "test@example.com")
	command("config", "user.name", "Test")
	if err := writeDerivedFile(filepath.Join(root, "with space.go"), []byte("package p\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeDerivedFile(filepath.Join(root, "app_test.go"), []byte("package p\n")); err != nil {
		t.Fatal(err)
	}
	command("add", ".")
	command("commit", "-qm", "base")
	before, _ := git(root, "rev-parse", "HEAD")
	if err := writeDerivedFile(filepath.Join(root, "with space.go"), []byte("package p\n// changed\n")); err != nil {
		t.Fatal(err)
	}
	if err := writeDerivedFile(filepath.Join(root, "app_test.go"), []byte("package p\n// changed\n")); err != nil {
		t.Fatal(err)
	}
	after, err := verifiedTreeWriting(root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := changedPaths(root, strings.TrimSpace(before), after)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0].Path != "app_test.go" || paths[0].Verdict != "exempt" || paths[1].Path != "with space.go" {
		t.Fatalf("paths = %+v", paths)
	}
}

func writeDerivedFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

func TestDerivedRecoveryNamesJudgeAmendment(t *testing.T) {
	hint := derivedRecovery("A1", "format.sh")
	for _, want := range []string{"decider: judge", "bl checks A1 --amend", "revert stays green"} {
		if !strings.Contains(hint, want) {
			t.Fatalf("hint %q missing %q", hint, want)
		}
	}
}

func TestDeriveCoverageMarksModifiedAndAddedPaths(t *testing.T) {
	t.Run("modified revert turns gate red", func(t *testing.T) {
		root, before := derivedFixture(t, map[string]string{"app.go": "before\n"})
		writeDerivedFile(filepath.Join(root, "app.go"), []byte("after\n"))
		d := runDerivedFixture(t, root, before, checkRow{f: map[string]string{"id": "C1", "decider": "bl", "severity": "blocking", "cmd": "grep -q after app.go", "expect": "exit 0"}})
		if d.Control.Verdict != "PASS" || len(d.Paths) != 1 || d.Paths[0].Verdict != "red" || d.Paths[0].KilledBy != "C1" || d.Paths[0].Weak {
			t.Fatalf("derived result = %+v", d)
		}
	})
	t.Run("removing added file turns gate red weakly", func(t *testing.T) {
		root, before := derivedFixture(t, nil)
		writeDerivedFile(filepath.Join(root, "lib.go"), []byte("marker\n"))
		d := runDerivedFixture(t, root, before, checkRow{f: map[string]string{"id": "C1", "decider": "bl", "severity": "blocking", "cmd": "grep -q marker lib.go", "expect": "exit 0"}})
		if d.Control.Verdict != "PASS" || len(d.Paths) != 1 || d.Paths[0].Kind != "added" || d.Paths[0].Verdict != "red" || !d.Paths[0].Weak {
			t.Fatalf("derived result = %+v", d)
		}
	})
}

func TestDeriveCoverageMarksDeletedPathAndControlFailure(t *testing.T) {
	t.Run("restoring deleted file turns gate red", func(t *testing.T) {
		root, before := derivedFixture(t, map[string]string{"old.go": "old\n"})
		if err := os.Remove(filepath.Join(root, "old.go")); err != nil {
			t.Fatal(err)
		}
		d := runDerivedFixture(t, root, before, checkRow{f: map[string]string{"id": "C1", "decider": "bl", "severity": "blocking", "cmd": "test ! -e old.go", "expect": "exit 0"}})
		if d.Control.Verdict != "PASS" || len(d.Paths) != 1 || d.Paths[0].Kind != "deleted" || d.Paths[0].Verdict != "red" || d.Paths[0].Weak {
			t.Fatalf("derived result = %+v", d)
		}
	})
	t.Run("red control makes code path unusable", func(t *testing.T) {
		root, before := derivedFixture(t, map[string]string{"app.go": "before\n", ".gitignore": "config.local\n"})
		writeDerivedFile(filepath.Join(root, "app.go"), []byte("after\n"))
		if err := writeDerivedFile(filepath.Join(root, "config.local"), []byte("local\n")); err != nil {
			t.Fatal(err)
		}
		row := checkRow{f: map[string]string{"id": "C1", "decider": "bl", "severity": "blocking", "cmd": "test -e config.local", "expect": "exit 0"}}
		d := runDerivedFixture(t, root, before, row)
		if d.Control.Verdict != "FAIL" || d.Control.FailedRow != "C1" || len(d.Paths) != 1 || d.Paths[0].Verdict != "unusable" {
			t.Fatalf("derived result = %+v", d)
		}
	})
}

func derivedFixture(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	gitFixture := func(args ...string) string {
		t.Helper()
		c := exec.Command("git", args...)
		c.Dir = root
		b, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("git %v: %s: %v", args, b, e)
		}
		return strings.TrimSpace(string(b))
	}
	gitFixture("init", "-q")
	gitFixture("config", "user.email", "test@example.com")
	gitFixture("config", "user.name", "Test")
	for path, body := range files {
		if err := writeDerivedFile(filepath.Join(root, path), []byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if len(files) == 0 {
		if err := writeDerivedFile(filepath.Join(root, ".fixture"), []byte("fixture\n")); err != nil {
			t.Fatal(err)
		}
	}
	gitFixture("add", "-A")
	gitFixture("commit", "-qm", "before")
	return root, gitFixture("rev-parse", "HEAD")
}

func runDerivedFixture(t *testing.T, root, before string, row checkRow) *derivedRun {
	t.Helper()
	after, err := verifiedTreeWriting(root)
	if err != nil {
		t.Fatal(err)
	}
	item := &Item{ID: "A1", BeforeTree: before}
	rows := []checkRow{row}
	records := []runRow{{ID: row.f["id"], Verdict: "PASS"}}
	d, err := deriveCoverage(root, item, rows, records, []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		t.Fatal("derived result is nil")
	}
	return d
}

func TestScratchTreeIncludesOnlyItemFilesAndIsCleanGit(t *testing.T) {
	root, before := derivedFixture(t, map[string]string{"app.go": "source\n"})
	if err := os.MkdirAll(filepath.Join(root, ".backlog", "items", "A1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, ".backlog", "items", "A2"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".backlog", "items", "A1", "tests.py"), []byte("frozen\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".backlog", "items", "A2", "other"), []byte("other\n"), 0644); err != nil {
		t.Fatal(err)
	}
	dir, cleanup, err := scratchTree(root, before, "A1")
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "tests.py")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A2")); !os.IsNotExist(err) {
		t.Fatalf("other backlog item present: %v", err)
	}
	if out, code := git(dir, "status", "--porcelain"); code != 0 || out != "" {
		t.Fatalf("scratch git status = %q (%d)", out, code)
	}
	if out, code := git(dir, "rev-parse", "--show-toplevel"); code != 0 || !sameResolvedPath(strings.TrimSpace(out), dir) {
		t.Fatalf("scratch root = %q (%d)", out, code)
	}
}

func TestDerivedIDsAreStableForSortedPaths(t *testing.T) {
	root, before := derivedFixture(t, nil)
	writeDerivedFile(filepath.Join(root, "z.go"), []byte("z\n"))
	writeDerivedFile(filepath.Join(root, "a.go"), []byte("a\n"))
	after, err := verifiedTreeWriting(root)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := changedPaths(root, before, after)
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || paths[0].ID != "D1" || paths[0].Path != "a.go" || paths[1].ID != "D2" || paths[1].Path != "z.go" {
		t.Fatalf("sorted derived paths: %s", fmt.Sprint(paths))
	}
}

func TestRunRefusesDerivationWriteOutsideScratch(t *testing.T) {
	dir := gitItem(t)
	list := checksHeader + "S1\tbl\tblocking\tB1\tif [ ! -d .backlog/evidence ]; then printf x >> '" + filepath.Join(dir, ".backlog/items/A1/claims.md") + "'; fi; grep -q marker app.go\texit 0\t\t\tderivation write probe\n"
	legacyChecks(t, dir, list)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatal(err)
	}
	writeDerivedFile(filepath.Join(dir, "app.go"), []byte("marker\n"))
	if err := call(t, cmdRun, dir, "A1"); err == nil || !strings.Contains(err.Error(), "directory changed under derivation") {
		t.Fatalf("run error = %v, want R27 refusal from the post-derivation check", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog/evidence/A1-run1.json")); !os.IsNotExist(err) {
		t.Fatalf("refused derivation run wrote a run file: %v", err)
	}
}

func TestRunRefusesDerivationWriteToTheRealTree(t *testing.T) {
	dir := gitItem(t)
	list := checksHeader + "S1\tbl\tblocking\tB1\tif [ ! -d .backlog/evidence ]; then printf x >> '" + filepath.Join(dir, "app.go") + "'; fi; grep -q marker app.go\texit 0\t\t\tderivation tree write probe\n"
	legacyChecks(t, dir, list)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatal(err)
	}
	writeDerivedFile(filepath.Join(dir, "app.go"), []byte("marker\n"))
	if err := call(t, cmdRun, dir, "A1"); err == nil || !strings.Contains(err.Error(), "tree moved under derivation") {
		t.Fatalf("run error = %v, want R26 refusal from the post-derivation check", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog/evidence/A1-run1.json")); !os.IsNotExist(err) {
		t.Fatalf("refused derivation run wrote a run file: %v", err)
	}
}

func TestDerivedJudgeAnchorInOneHunkCoversWholePath(t *testing.T) {
	vf := &verdictsFile{rows: map[string]verdictRow{"J1": {id: "J1", verdict: "PASS", anchor: `app.go:2 "anchor"`}}}
	rows := []checkRow{{f: map[string]string{"id": "J1", "decider": "judge", "severity": "blocking", "cmd": "inspect"}}}
	d := &derivedRun{Paths: []derivedPath{{Path: "app.go", Verdict: "green"}}}
	err := checkCoverage("A1", vf, rows, []hunk{{file: "app.go", start: 2, count: 1}, {file: "app.go", start: 20, count: 2}}, nil, nil, func(string) string { return "" }, d)
	if err != nil {
		t.Fatalf("coverage should accept an anchor in one hunk for the whole path: %v", err)
	}
}

func TestDerivedJudgeAnchorOutsideEveryHunkIsRefused(t *testing.T) {
	vf := &verdictsFile{rows: map[string]verdictRow{"J1": {id: "J1", verdict: "PASS", anchor: `app.go:90 "anchor"`}}}
	rows := []checkRow{{f: map[string]string{"id": "J1", "decider": "judge", "severity": "blocking", "cmd": "inspect"}}}
	d := &derivedRun{Paths: []derivedPath{{Path: "app.go", Verdict: "green"}}}
	err := checkCoverage("A1", vf, rows, []hunk{{file: "app.go", start: 2, count: 1}}, nil, nil, func(string) string { return "" }, d)
	wantRule(t, err, "R10")
}

func TestDerivedJudgeCmdMustNameThePathAsAWord(t *testing.T) {
	d := &derivedRun{Paths: []derivedPath{{Path: "a.go", Verdict: "green"}}}
	hunks := []hunk{{file: "a.go", start: 2, count: 1}}
	check := func(cmd string) error {
		vf := &verdictsFile{rows: map[string]verdictRow{"J1": {id: "J1", verdict: "PASS"}}}
		rows := []checkRow{{f: map[string]string{"id": "J1", "decider": "judge", "severity": "blocking", "cmd": cmd}}}
		return checkCoverage("A1", vf, rows, hunks, nil, nil, func(string) string { return "" }, d)
	}
	wantRule(t, check("inspect data.go"), "R10")
	for _, cmd := range []string{"cat a.go", "cat ./a.go", `cat "a.go"`} {
		if err := check(cmd); err != nil {
			t.Errorf("cmd %q names a.go: %v", cmd, err)
		}
	}
}

func sameResolvedPath(a, b string) bool {
	ra, ea := filepath.EvalSymlinks(a)
	rb, eb := filepath.EvalSymlinks(b)
	return ea == nil && eb == nil && filepath.Clean(ra) == filepath.Clean(rb)
}
