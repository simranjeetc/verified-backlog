package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func baselineFixture(t *testing.T) string {
	t.Helper()
	dir := gitItemWith(t, map[string]string{"code.go": "old\n", "config": "old\n", ".gitignore": ".backlog/\n"})
	// Re-open with explicit scope using supported commands, before checks/claims.
	if err := call(t, cmdDefer, dir, "A1", "--reason", "set scope"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdReopen, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdOpen, dir, "A1", "--scope", "code.go"); err != nil {
		t.Fatal(err)
	}
	legacyChecks(t, dir, oneRowList)
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatal(err)
	}
	return dir
}

func baselineItem(t *testing.T, dir string) *Item {
	t.Helper()
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	_, items, err := derive(events)
	if err != nil {
		t.Fatal(err)
	}
	return items["a1"]
}

func TestBaselinePreservesProducerDiffAndInvalidatesRun(t *testing.T) {
	dir := baselineFixture(t)
	original := baselineItem(t, dir)
	writeFile(t, dir, "config", "external\n")
	commitSeed(t, dir)
	writeFile(t, dir, "code.go", "producer\n")
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	prefix := len(events)
	index, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if err := call(t, cmdBaseline, dir, "A1", "--reason", "external config commit"); err != nil {
		t.Fatal(err)
	}
	it := baselineItem(t, dir)
	if it.Attempt != original.Attempt || it.ChecksSHA != original.ChecksSHA || it.ClaimsSHA != original.ClaimsSHA || len(it.Runs) != 1 {
		t.Fatal("recovery changed attempt or frozen evidence")
	}
	if _, ok := theRun(it); ok {
		t.Fatal("old run remains eligible")
	}
	old, _ := baselineEntries(dir, original.BeforeTree)
	next, _ := baselineEntries(dir, it.BeforeTree)
	if next["code.go"] != old["code.go"] || next["config"] == old["config"] {
		t.Fatal("wrong baseline entries")
	}
	afterIndex, _ := os.ReadFile(filepath.Join(dir, ".git", "index"))
	if string(index) != string(afterIndex) {
		t.Fatal("real index changed")
	}
	data, _ := os.ReadFile(filepath.Join(dir, "code.go"))
	if string(data) != "producer\n" {
		t.Fatal("producer work changed")
	}
	after, _ := readLedger(dir)
	if len(after) != prefix+1 || after[prefix].PrevBeforeTree != original.BeforeTree {
		t.Fatal("missing audit event")
	}
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if run, ok := theRun(baselineItem(t, dir)); !ok || run.Run != "run2" {
		t.Fatalf("fresh run missing: %+v", run)
	}
}

func TestBaselineRefusalsDoNotAppend(t *testing.T) {
	for _, scenario := range []string{"uncommitted", "none", "policy", "pass", "window", "damage"} {
		t.Run(scenario, func(t *testing.T) {
			dir := baselineFixture(t)
			writeFile(t, dir, "config", "external\n")
			if scenario != "uncommitted" {
				commitSeed(t, dir)
			}
			switch scenario {
			case "none":
				writeFile(t, dir, "config", "old\n")
				commitSeed(t, dir)
			case "policy":
				writeFile(t, dir, ".gitignore", ".backlog/\nignored\n")
				commitSeed(t, dir)
			case "pass":
				if _, err := appendEvent(dir, Event{T: "round", ID: "A1", Verdict: "PASS"}); err != nil {
					t.Fatal(err)
				}
			case "window":
				if err := call(t, cmdPacket, dir, "A1", "--role", "verifier"); err != nil {
					t.Fatal(err)
				}
			case "damage":
				it := baselineItem(t, dir)
				writeFile(t, dir, it.Claims, "damaged")
			}
			before, _ := readLedger(dir)
			err := call(t, cmdBaseline, dir, "A1", "--reason", "recover")
			if err == nil {
				t.Fatal("expected refusal")
			}
			after, _ := readLedger(dir)
			if len(after) != len(before) {
				t.Fatal("refusal appended")
			}
		})
	}
}

func TestBaselineCommittedDeletionModeAndAddition(t *testing.T) {
	dir := baselineFixture(t)
	if err := os.Remove(filepath.Join(dir, "config")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "new file", "new\n")
	if err := os.Chmod(filepath.Join(dir, "new file"), 0755); err != nil {
		t.Fatal(err)
	}
	commitSeed(t, dir)
	// Even a committed producer edit must remain in the item's diff.
	writeFile(t, dir, "code.go", "committed producer\n")
	commitSeed(t, dir)
	if err := call(t, cmdBaseline, dir, "A1", "--reason", "external entries"); err != nil {
		t.Fatal(err)
	}
	it := baselineItem(t, dir)
	entries, err := baselineEntries(dir, it.BeforeTree)
	if err != nil {
		t.Fatal(err)
	}
	if entries["config"] != "" || !strings.HasPrefix(entries["new file"], "100755 ") {
		t.Fatal(entries)
	}
	body, code := git(dir, "show", it.BeforeTree+":code.go")
	if code != 0 || body != "old\n" {
		t.Fatalf("absorbed producer diff: %q", body)
	}
}

func TestBaselineVoidWindowRequiresFreshPacketAndRun(t *testing.T) {
	dir := baselineFixture(t)
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdPacket, dir, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "config", "external\n")
	commitSeed(t, dir)
	// The native run event voids the old packet; no invented harness round is needed.
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdBaseline, dir, "A1", "--reason", "external config"); err != nil {
		t.Fatal(err)
	}
	events, _ := readLedger(dir)
	it := baselineItem(t, dir)
	if openVerifierPacket(events, it) != 0 {
		t.Fatal("old verifier packet survived")
	}
	if _, ok := theRun(it); ok {
		t.Fatal("old run survived")
	}
	if err := call(t, cmdRun, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdPacket, dir, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	events, _ = readLedger(dir)
	it = baselineItem(t, dir)
	if openVerifierPacket(events, it) <= it.BaselineSeq {
		t.Fatal("fresh packet missing")
	}
	if err := verifyPacketWindow(dir, events, it); err != nil {
		t.Fatal(err)
	}
}

func TestBaselineRefusesAncestorCollision(t *testing.T) {
	dir := gitItemWith(t, map[string]string{".gitignore": ".backlog/\n"})
	if err := call(t, cmdDefer, dir, "A1", "--reason", "fixture baseline"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "foo"), 0755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo/a", "original\n")
	commitSeed(t, dir)
	if err := call(t, cmdReopen, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdOpen, dir, "A1", "--scope", "foo/a"); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "foo/a")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "foo")); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "foo", "replacement\n")
	commitSeed(t, dir)
	before, _ := readLedger(dir)
	err := call(t, cmdBaseline, dir, "A1", "--reason", "replacement")
	if err == nil {
		t.Fatal("accepted erasure of scoped baseline")
	}
	after, _ := readLedger(dir)
	if len(after) != len(before) {
		t.Fatal("refusal appended")
	}
}
