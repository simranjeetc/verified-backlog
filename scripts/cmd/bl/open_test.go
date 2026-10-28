package main

import (
	"os"
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
	if last.T != "open" || last.Brief != first.Brief || last.BriefSHA != first.BriefSHA || !sameStrings(last.Scope, first.Scope) || last.BeforeTree != first.BeforeTree {
		t.Fatalf("resume event did not preserve frozen open data: first=%+v last=%+v", first, last)
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
