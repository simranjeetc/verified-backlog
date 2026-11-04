package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAgentFileRequiresProofAndBucket(t *testing.T) {
	dir := t.TempDir()
	if err := call(t, cmdInit, dir); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args []string
		want string
	}{
		{[]string{"--id", "D1", "--text", "x", "--source", "Verifier:A1/r1", "--bucket", "later"}, "--proof"},
		{[]string{"--id", "D1", "--text", "x", "--source", "producer:A1", "--proof", "go test"}, "fix-now, batch or later"},
	} {
		err := call(t, cmdFile, dir, tc.args...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("error = %v, want %q", err, tc.want)
		}
		if events, readErr := readLedger(dir); readErr != nil || len(events) != 0 {
			t.Fatalf("refusal appended events: %v, %v", events, readErr)
		}
	}
	if err := call(t, cmdFile, dir, "--id", "D1", "--text", "x", "--source", "loop-run", "--proof", "bl round A1 -> exit 0", "--bucket", "later"); err != nil {
		t.Fatal(err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	if it := byID["d1"]; it.Status != "parked" || it.Reason != "later" || it.Proof != "bl round A1 -> exit 0" || it.Bucket != "later" {
		t.Fatalf("item = %+v", it)
	}
}

func TestDropParkedItems(t *testing.T) {
	for _, tc := range []struct {
		name string
		park func(*testing.T, string)
	}{
		{"defer", func(t *testing.T, dir string) {
			if err := call(t, cmdFile, dir, "--id", "A1", "--text", "x", "--source", "human"); err != nil {
				t.Fatal(err)
			}
			if err := call(t, cmdDefer, dir, "A1", "--reason", "later"); err != nil {
				t.Fatal(err)
			}
		}},
		{"file later", func(t *testing.T, dir string) {
			if err := call(t, cmdFile, dir, "--id", "A1", "--text", "x", "--source", "human", "--bucket", "later"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := call(t, cmdInit, dir); err != nil {
				t.Fatal(err)
			}
			tc.park(t, dir)
			if err := call(t, cmdDrop, dir, "A1", "--reason", "declined"); err != nil {
				t.Fatalf("drop: %v", err)
			}
			out := capture(t, func() { _ = call(t, cmdStatus, dir) })
			if !strings.Contains(out, "declined 1") {
				t.Fatalf("status = %q, want declined count", out)
			}
			if err := call(t, cmdCheck, dir); err != nil {
				t.Fatalf("check: %v", err)
			}
		})
	}
}

func TestDropRefusesClosedAndDeclined(t *testing.T) {
	for _, status := range []string{"closed", "declined"} {
		t.Run(status, func(t *testing.T) {
			dir := t.TempDir()
			if err := call(t, cmdInit, dir); err != nil {
				t.Fatal(err)
			}
			if err := call(t, cmdFile, dir, "--id", "A1", "--text", "x", "--source", "human"); err != nil {
				t.Fatal(err)
			}
			if status == "closed" {
				if _, err := appendEvent(dir, Event{T: "close", ID: "A1", Commit: "abcdef0"}); err != nil {
					t.Fatal(err)
				}
			} else if err := call(t, cmdDrop, dir, "A1", "--reason", "first decline"); err != nil {
				t.Fatal(err)
			}
			err := call(t, cmdDrop, dir, "A1", "--reason", "again")
			if err == nil || !strings.Contains(err.Error(), "A1 is "+status+", not open") {
				t.Fatalf("drop error = %v", err)
			}
		})
	}
}

func TestNonAgentFileCanOmitProofAndBucket(t *testing.T) {
	dir := t.TempDir()
	if err := call(t, cmdInit, dir); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdFile, dir, "--id", "A1", "--text", "x", "--source", "human"); err != nil {
		t.Fatal(err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	if it := byID["a1"]; it.Status != "open" {
		t.Fatalf("status = %s, want open", it.Status)
	}
}

func TestAgentFileProofAndBucketPassCheck(t *testing.T) {
	dir := t.TempDir()
	if err := call(t, cmdInit, dir); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdFile, dir, "--id", "D1", "--text", "recover the failed job", "--source", "producer:A1", "--proof", "go test ./... -> identified failed job A1", "--bucket", "fix-now"); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{}, {"--views"}} {
		if err := call(t, cmdCheck, dir, argv...); err != nil {
			t.Fatalf("bl check %v: %v", argv, err)
		}
	}
}

func openResumeItem(t *testing.T) string {
	t.Helper()
	dir := openItem(t)
	checksOK(t, dir, goodList)
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"defer", "A1", "--reason", "later"}, {"reopen", "A1"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	for _, argv := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(".backlog/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"add", "-A", "."}, {"commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	return dir
}

func TestOpenResumeKeepsBrief(t *testing.T) {
	dir := openResumeItem(t)
	brief := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	before, err := os.ReadFile(brief)
	if err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	var first Event
	for _, ev := range events {
		if ev.T == "open" {
			first = ev
			break
		}
	}
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(brief)
	if string(after) != string(before) {
		t.Fatal("resume rewrote brief")
	}
	events, _ = readLedger(dir)
	last := events[len(events)-1]
	current, err := verifiedTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if last.T != "open" || last.Brief != first.Brief || last.BriefSHA != first.BriefSHA || !sameStrings(last.Scope, first.Scope) || last.BeforeTree != current {
		t.Fatalf("resume event did not preserve frozen open data: first=%+v last=%+v", first, last)
	}
}

func TestOpenResumeTakesTheCurrentTree(t *testing.T) {
	dir := openResumeItem(t)
	events, _ := readLedger(dir)
	var first Event
	for _, ev := range events {
		if ev.T == "open" {
			first = ev
			break
		}
	}
	writeFile(t, dir, "between.go", "package between\n")
	for _, argv := range [][]string{{"add", "between.go"}, {"commit", "-qm", "other item"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	events, _ = readLedger(dir)
	last := events[len(events)-1]
	current, err := verifiedTree(dir)
	if err != nil {
		t.Fatal(err)
	}
	if last.BeforeTree != current || last.BeforeTree == first.BeforeTree {
		t.Fatalf("resume before-tree = %s; want current %s and different from first %s", last.BeforeTree, current, first.BeforeTree)
	}
}

func TestOpenResumeRefusesDirtyTree(t *testing.T) {
	dir := openResumeItem(t)
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a // modified\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	before := len(events)
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R5" || !strings.Contains(r.msg, "commit or stash") {
		t.Fatalf("want R5 refusal mentioning commit or stash, got %s: %s", r.rule, r.msg)
	}
	events, _ = readLedger(dir)
	if len(events) != before {
		t.Fatal("refused dirty resume wrote an event")
	}
}

func TestOpenRefusesDirtyTree(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{{"init"}, {"file", "--id", "A1", "--text", "brief"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	writeFile(t, dir, "a.go", "package a // clean\n")
	writeFile(t, dir, ".gitignore", ".backlog/\n")
	for _, argv := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"}, {"add", "a.go", ".gitignore"}, {"commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	writeFile(t, dir, "a.go", "package a // modified\n")
	before, _ := readLedger(dir)
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R5" || !strings.Contains(r.msg, "commit or stash") || !strings.Contains(r.msg, "a.go") {
		t.Fatalf("want R5 dirty-tree refusal naming a.go and commit or stash, got %s: %s", r.rule, r.msg)
	}
	if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "brief.md")); !os.IsNotExist(err) {
		t.Fatalf("refused fresh open wrote brief: %v", err)
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatal("refused fresh open wrote an event")
	}
}

func TestOpenDirtyCheckReadsRealNames(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{{"init"}, {"file", "--id", "A1", "--text", "brief"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	writeFile(t, dir, "a.go", "package a\n")
	for _, argv := range [][]string{{"init", "-q"}, {"config", "user.email", "a@b.c"}, {"config", "user.name", "a"}, {"add", "a.go"}, {"commit", "-qm", "base"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	writeFile(t, dir, "my file.go", "package m\n")
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if !strings.Contains(r.msg, "my file.go") || strings.Contains(r.msg, `"my file.go"`) {
		t.Fatalf("dirty path was not reported unquoted: %s", r.msg)
	}
}

func TestOpenRefusesInvalidScopeBeforeWriting(t *testing.T) {
	for _, scope := range []string{"a.go,b.go", "nosuch/x.go", "", " "} {
		t.Run(scope, func(t *testing.T) {
			dir := unopenedItem(t)
			before, err := readLedger(dir)
			if err != nil {
				t.Fatal(err)
			}
			r := wantRefusal(t, call(t, cmdOpen, dir, "A1", "--scope", scope))
			if !strings.Contains(r.msg, `"`+scope+`"`) || !strings.Contains(r.msg, "--scope is repeated once per path") {
				t.Fatalf("refusal did not explain invalid scope %q: %s", scope, r.msg)
			}
			after, err := readLedger(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatal("refused open wrote an event")
			}
			if _, err := os.Stat(filepath.Join(dir, ".backlog", "items", "A1", "brief.md")); !os.IsNotExist(err) {
				t.Fatalf("refused open wrote brief: %v", err)
			}
		})
	}
}

func TestOpenAcceptsExistingAndNewScopePaths(t *testing.T) {
	dir := unopenedItem(t)
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "src/main.go", "package main\n")
	args := []string{"A1", "--scope", "src/", "--scope", "src/main.go", "--scope", "src/new.go", "--scope", "NEW.md"}
	if err := call(t, cmdOpen, dir, args...); err != nil {
		t.Fatal(err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := events[len(events)-1].Scope
	want := []string{"src/", "src/main.go", "src/new.go", "NEW.md"}
	if !sameStrings(got, want) {
		t.Fatalf("scope = %#v; want %#v", got, want)
	}
}

func unopenedItem(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range [][]string{{"init"}, {"file", "--id", "A1", "--text", "brief"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	return dir
}

func TestOpenResumeRefusesDifferentScope(t *testing.T) {
	dir := openResumeItem(t)
	events, _ := readLedger(dir)
	before := len(events)
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1", "--scope", "b.go"))
	if r.rule != "R5" {
		t.Fatalf("want R5, got %s: %s", r.rule, r.msg)
	}
	events, _ = readLedger(dir)
	if len(events) != before {
		t.Fatal("refused resume wrote an event")
	}
}

func TestOpenResumeRefusesTamperedBrief(t *testing.T) {
	dir := openResumeItem(t)
	brief := filepath.Join(dir, ".backlog", "items", "A1", "brief.md")
	data, _ := os.ReadFile(brief)
	if err := os.WriteFile(brief, []byte(strings.TrimSpace(string(data))+" tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	before := len(events)
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R20" {
		t.Fatalf("want R20, got %s: %s", r.rule, r.msg)
	}
	events, _ = readLedger(dir)
	if len(events) != before {
		t.Fatal("refused resume wrote an event")
	}
}

// R20 at resume: every frozen artefact is re-hashed, not only the brief — a claims file edited
// while the item was parked is refused before the open event is written.
func TestOpenResumeRefusesTamperedClaims(t *testing.T) {
	dir := openResumeItem(t)
	claims := filepath.Join(dir, ".backlog", "items", "A1", "claims.md")
	if err := os.WriteFile(claims, []byte("CLAIMS:\n- tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	before := len(events)
	r := wantRefusal(t, call(t, cmdOpen, dir, "A1"))
	if r.rule != "R20" {
		t.Fatalf("want R20, got %s: %s", r.rule, r.msg)
	}
	events, _ = readLedger(dir)
	if len(events) != before {
		t.Fatal("refused resume wrote an event")
	}
}
