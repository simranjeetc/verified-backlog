package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
