package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIntakeCheckAcceptsAValidIntake(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "intake.jsonl")
	body := "{\"t\":\"decision\",\"id\":\"D1\",\"text\":\"decision text\",\"by\":\"human\"}\n{\"t\":\"item\",\"id\":\"I1\",\"title\":\"first\",\"after\":[],\"scope\":[],\"decisions\":[\"D1\"]}\n{\"t\":\"clause\",\"item\":\"I1\",\"id\":\"B1\",\"observe\":\"x\",\"expect\":\"ok\",\"example\":\"x ok\"}\n"
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	old := os.Stdout
	f, err := os.CreateTemp(dir, "out")
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(f.Name())
	os.Stdout = f
	err = cmdIntake(dir, args{flags: map[string]string{"file": path}, boolFlags: map[string]bool{"check": true}})
	f.Close()
	os.Stdout = old
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(f.Name())
	out.Write(b)
	if !strings.Contains(out.String(), "1 items") || !strings.Contains(out.String(), "1 clauses") {
		t.Fatalf("output %q", out.String())
	}
}

func TestIntakeCheckReportsEveryError(t *testing.T) {
	rs, es := parseIntake("in.jsonl", "{\"t\":\"clause\",\"item\":\"I9\",\"id\":\"B1\",\"observe\":\"x\",\"expect\":\"y\",\"example\":\"\"}\n")
	es = append(es, validateIntake("in.jsonl", rs, map[string]bool{})...)
	if len(es) < 2 {
		t.Fatalf("got %v", es)
	}
}

func TestIntakeRender(t *testing.T) {
	rs := []intakeRecord{{T: "decision", ID: "D1", Text: "full decision", By: "human", line: 1}, {T: "item", ID: "I1", Title: "title", Decisions: []string{"D1"}, line: 2}, {T: "clause", Item: "I1", ID: "B1", Observe: "x", Expect: "ok", Example: "x ok", line: 3}}
	if es := validateIntake("x", rs, map[string]bool{}); len(es) != 0 {
		t.Fatalf("fixture invalid: %v", es)
	}
}
