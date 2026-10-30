package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderedPromptDeterministicAndBlind(t *testing.T) {
	root := openItem(t)
	// The renderer consumes only the already filtered packet, never ledger claims/checks.
	out := t.TempDir()
	out, _ = filepath.EvalSymlinks(out)
	workspace := t.TempDir() + "/space ' directory"
	if err := os.Mkdir(workspace, 0o755); err != nil {
		t.Fatal(err)
	}
	p := packet{item: &Item{ID: "A1", Attempt: 2, Opened: true, Brief: "recorded", Scope: []string{"src/a.go"}}, role: "producer", brief: "B1: implement intent"}
	a, err := parseArgs([]string{"A1", "--role", "producer", "--render", "--workspace", workspace, "--output-dir", out})
	if err != nil {
		t.Fatal(err)
	}
	first, dest, err := renderRolePrompt(root, a, p)
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := renderRolePrompt(root, a, p)
	if err != nil || first != second {
		t.Fatalf("nondeterministic: %v", err)
	}
	if dest != filepath.Join(out, "A1-a2-producer-prompt.md") {
		t.Fatal(dest)
	}
	for _, want := range []string{"Role prompt v1", "CODE QUALITY", supervisorQuote(func() string { p, _ := filepath.EvalSymlinks(workspace); return p }()), "B1: implement intent", "src/a.go", "A1-a2-evidence.md"} {
		if !strings.Contains(first, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"## Claims", "## Run", "{{", "<evidence dir>", "<item text>", "run the full suites once"} {
		if strings.Contains(first, bad) {
			t.Errorf("unexpected %q", bad)
		}
	}
}

func TestRenderedPacketOutputMatchesStdout(t *testing.T) {
	root := openItem(t)
	out := t.TempDir()
	var gotErr error
	stdout := capture(t, func() {
		gotErr = call(t, cmdPacket, root, "A1", "--role", "checks", "--render", "--workspace", root, "--output-dir", out)
	})
	if gotErr != nil {
		t.Fatal(gotErr)
	}
	data, err := os.ReadFile(filepath.Join(out, "A1-a1-checks-prompt.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != stdout {
		t.Fatal("stdout and prompt artifact differ")
	}
	if !strings.Contains(stdout, "A1-a1-checks.tsv") || !strings.Contains(stdout, "A1-a1-tests.py") {
		t.Fatal(stdout)
	}
}

func TestPromptRejectsUnsafeResourcesBeforeVerifierEvent(t *testing.T) {
	root := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tfirst row\n")
	ledger := filepath.Join(root, ".backlog", "ledger.jsonl")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(filepath.Join(root, ".backlog"), alias); err != nil {
		t.Fatal(err)
	}
	for _, extra := range [][]string{
		{"--output-dir", alias},
		{"--output-dir", filepath.Join(t.TempDir(), "missing")},
		{"--output-dir", t.TempDir(), "--required-cases-file", filepath.Join(t.TempDir(), "missing")},
		{"--output-dir", t.TempDir(), "--typo", "value"},
	} {
		argv := append([]string{"A1", "--role", "verifier", "--render", "--workspace", root}, extra...)
		if err := call(t, cmdPacket, root, argv...); err == nil {
			t.Fatalf("accepted %v", argv)
		}
		after, err := os.ReadFile(ledger)
		if err != nil || string(after) != string(before) {
			t.Fatalf("invalid render recorded verifier event: %v", err)
		}
	}
}

func TestRenderedVerifierRetainsWindowAndClaims(t *testing.T) {
	root := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tfirst row\n")
	if err := call(t, cmdRun, root, "A1"); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var gotErr error
	stdout := capture(t, func() {
		gotErr = call(t, cmdPacket, root, "A1", "--role", "verifier", "--render", "--workspace", root, "--output-dir", out)
	})
	if gotErr != nil {
		t.Fatal(gotErr)
	}
	for _, want := range []string{"## Claims", "## Run", "R39", "A1-a1-verify-r1-p"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q", want)
		}
	}
	events, err := readLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	_, items, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	it, err := lookup(items, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].T != "packet" {
		t.Fatal("no verifier event")
	}
	if err := verifyPacketWindow(root, events, it); err != nil {
		t.Fatalf("render invalidated its window: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".backlog", "changed"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := verifyPacketWindow(root, events, it); err == nil {
		t.Fatal("rendered verifier did not retain R39 protection")
	}
}

func TestPromptRequiredCasesRefusesBlindRolesBeforeResources(t *testing.T) {
	root := openItem(t)
	ledger := filepath.Join(root, ".backlog", "ledger.jsonl")
	before, err := os.ReadFile(ledger)
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"producer", "checks"} {
		out := t.TempDir()
		// The missing cases file must never be opened: refusal identifies the role boundary.
		err := call(t, cmdPacket, root, "A1", "--role", role, "--render", "--workspace", root, "--output-dir", out, "--required-cases-file", filepath.Join(out, "missing"))
		if err == nil || !strings.Contains(err.Error(), "restricted to the verifier role") {
			t.Fatalf("%s: %v", role, err)
		}
		entries, err := os.ReadDir(out)
		if err != nil || len(entries) != 0 {
			t.Fatalf("%s wrote artifacts: %v %v", role, entries, err)
		}
		after, err := os.ReadFile(ledger)
		if err != nil || string(after) != string(before) {
			t.Fatalf("%s changed ledger: %v", role, err)
		}
	}
}

func TestVerifierPromptOutputContractAndImportIsolation(t *testing.T) {
	root := runItem(t, checksHeader+"S1\tbl\tblocking\tB1\techo hi\texit 0\t\t\tfirst row\n")
	if err := call(t, cmdRun, root, "A1"); err != nil {
		t.Fatal(err)
	}
	var gotErr error
	out := capture(t, func() {
		gotErr = call(t, cmdPacket, root, "A1", "--role", "verifier", "--render", "--workspace", root, "--output-dir", t.TempDir())
	})
	if gotErr != nil {
		t.Fatal(gotErr)
	}
	for _, want := range []string{"verdicts", "--init --output", "verify-r1-p", ".json", ".tsv", ".md", "--rows <rows TSV> --report <report>", "sys.dont_write_bytecode = True", "python3 -B", "PYTHONDONTWRITEBYTECODE=1", "Do not add mechanical rows", "Do not type RO tags", "@_covers"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	for _, bad := range []string{"OUTPUT: | check |", "no difference against the recorded before-tree", "a row that covers a change puts"} {
		if strings.Contains(out, bad) {
			t.Errorf("stale instruction %q", bad)
		}
	}
	// Execute the instructed import mode on a real frozen-path module and ensure
	// the evidence window remains valid, rather than just checking prompt text.
	module := writeFile(t, root, ".backlog/items/A1/probe.py", "VALUE = 7\n")
	if err := call(t, cmdPacket, root, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	script := "import importlib.util, sys; spec=importlib.util.spec_from_file_location('probe', sys.argv[1]); m=importlib.util.module_from_spec(spec); spec.loader.exec_module(m); assert m.VALUE == 7"
	if data, err := exec.Command("python3", "-B", "-c", script, module).CombinedOutput(); err != nil {
		t.Fatalf("probe: %v %s", err, data)
	}
	events, err := readLedger(root)
	if err != nil {
		t.Fatal(err)
	}
	_, items, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	it, err := lookup(items, "A1")
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPacketWindow(root, events, it); err != nil {
		t.Fatalf("import changed frozen evidence: %v", err)
	}
	// Child interpreters must inherit the protection; -B alone is not inherited.
	child := exec.Command("python3", "-c", "import subprocess, sys; subprocess.run([sys.executable, '-c', sys.argv[1], sys.argv[2]], check=True)", script, module)
	child.Env = append(os.Environ(), "PYTHONDONTWRITEBYTECODE=1")
	if data, err := child.CombinedOutput(); err != nil {
		t.Fatalf("child import: %v %s", err, data)
	}
	if err := verifyPacketWindow(root, events, it); err != nil {
		t.Fatalf("child import changed evidence: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(module), "__pycache__")); !os.IsNotExist(err) {
		t.Fatalf("cache created or stat failed: %v", err)
	}
}

func TestChecksPromptDistinguishesCoverageAndOptionalTests(t *testing.T) {
	root := openItem(t)
	var gotErr error
	out := capture(t, func() {
		gotErr = call(t, cmdPacket, root, "A1", "--role", "checks", "--render", "--workspace", root, "--output-dir", t.TempDir())
	})
	if gotErr != nil {
		t.Fatal(gotErr)
	}
	for _, want := range []string{"@_covers(...)", "a shell row names the covered path", "judge row anchors the changed hunk", "path-only change", "omit `--tests`", "An amendment cannot introduce a test file", "focused simple command", strings.TrimSpace(checksHeader)} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "kept only for a whole-suite") {
		t.Fatal("targeted amendment instruction contradicts whole-suite restriction")
	}
}
