package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baselineRule(err error) string {
	var r Refusal
	if errors.As(err, &r) {
		return r.rule
	}
	return ""
}

func TestOptionalBaselineColumnAndDeclarations(t *testing.T) {
	old := checksHeader + "S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\tc\n"
	if _, err := parseChecks(old); err != nil {
		t.Fatalf("legacy list: %v", err)
	}
	header := strings.TrimSuffix(checksHeader, "\n") + "\tbaseline\n"
	rows, err := parseChecks(header + "S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\tc\tmaybe\n")
	if err != nil {
		t.Fatal(err)
	}
	err = validateR1(rows)
	if baselineRule(err) != "R1" || !strings.Contains(err.Error(), `row S1: baseline "maybe"`) {
		t.Fatalf("bad declaration refusal = %v", err)
	}
	rows, err = parseChecks(header + "S1\tbl\tblocking\tB1\ttrue\texit 0\t\t\tc\t\n")
	if err != nil {
		t.Fatal(err)
	}
	if baselineRule(validateBaselinePresence(rows)) != "R40" {
		t.Fatal("missing baseline declaration was not refused under R40")
	}
}

func TestBaselineRowsJudgeCellMustBeEmpty(t *testing.T) {
	header := strings.TrimSuffix(checksHeader, "\n") + "\tbaseline\n"
	rows, err := parseChecks(header + "J1\tjudge\tblocking\tB1\techo x\t\tit fails\tprose requires judgment\tc\tkeep\n")
	if err != nil {
		t.Fatal(err)
	}
	err = validateR1(rows)
	if baselineRule(err) != "R1" || !strings.Contains(err.Error(), "judge row must have an empty baseline") {
		t.Fatalf("judge declaration refusal = %v", err)
	}
}

func TestBaselineRowsRunAgainstBeforeTreeAndEnforceDeclaration(t *testing.T) {
	dir := gitItemWith(t, map[string]string{"code.go": "old\n", ".gitignore": ".backlog/\n"})
	it := baselineItem(t, dir)
	rows := []checkRow{{f: map[string]string{"id": "C1", "decider": "bl", "baseline": "new", "cmd": "grep new code.go", "expect": "exit 0"}}}
	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	ran, baselineErr := validateBaselineRows(dir, it, rows, false, nil, false)
	_ = writer.Close()
	os.Stdout = oldStdout
	output, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if baselineErr != nil || !ran {
		t.Fatalf("new red row: ran=%v err=%v", ran, baselineErr)
	}
	if !strings.Contains(string(output), "bl: A1 baseline: 1 new red, 0 keep green") {
		t.Fatalf("baseline summary = %q", output)
	}
	rows[0].f["cmd"] = "grep old code.go"
	if _, err := validateBaselineRows(dir, it, rows, false, nil, false); baselineRule(err) != "R40" || !strings.Contains(err.Error(), "declared new but it passes") {
		t.Fatalf("green new row = %v", err)
	}
	rows[0].f["baseline"], rows[0].f["expect"] = "keep", "exit 0"
	if ran, err := validateBaselineRows(dir, it, rows, false, nil, false); err != nil || !ran {
		t.Fatalf("keep green row: ran=%v err=%v", ran, err)
	}
}

func TestBaselineRowsRefuseChangesToTheRealTree(t *testing.T) {
	dir := gitItemWith(t, map[string]string{"code.go": "old\n", ".gitignore": ".backlog/\n"})
	it := baselineItem(t, dir)
	command := "printf dirty >> " + filepath.Join(dir, "code.go")
	rows := []checkRow{{f: map[string]string{"id": "C1", "decider": "bl", "baseline": "keep", "cmd": command, "expect": "exit 0"}}}
	_, err := validateBaselineRows(dir, it, rows, false, nil, false)
	_ = os.WriteFile(filepath.Join(dir, "code.go"), []byte("old\n"), 0644)
	if baselineRule(err) != "R40" || !strings.Contains(err.Error(), "changed the repository tree") {
		t.Fatalf("real tree mutation = %v", err)
	}
}

func TestBaselineWithoutColumnWarnsAndSkips(t *testing.T) {
	dir := gitItemWith(t, map[string]string{"code.go": "old\n", ".gitignore": ".backlog/\n"})
	it := baselineItem(t, dir)
	rows := []checkRow{{f: map[string]string{"id": "C1", "decider": "bl", "cmd": "true", "expect": "exit 0"}}}
	oldStderr := os.Stderr
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	ran, baselineErr := validateBaselineRows(dir, it, rows, false, nil, false)
	_ = writer.Close()
	os.Stderr = oldStderr
	warning, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatal(err)
	}
	if baselineErr != nil || ran || !strings.Contains(string(warning), "no baseline check") {
		t.Fatalf("legacy baseline result: ran=%v err=%v stderr=%q", ran, baselineErr, warning)
	}
}

func TestChecksPromptRequiresBaselineDeclarations(t *testing.T) {
	root := openItem(t)
	var callErr error
	prompt := capture(t, func() {
		callErr = call(t, cmdPacket, root, "A1", "--role", "checks", "--render", "--workspace", root, "--output-dir", t.TempDir())
	})
	if callErr != nil {
		t.Fatal(callErr)
	}
	for _, text := range []string{"criterion\tbaseline", "baseline` field to `new` or `keep`", "Judge rows leave the field empty"} {
		if !strings.Contains(prompt, text) {
			t.Errorf("checks prompt missing %q", text)
		}
	}
}
