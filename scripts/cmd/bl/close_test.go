package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ── R36: the restart ────────────────────────────────────────────────────────

// restartItem is an item in attempt 1 with claims and one code FAIL round: a restart has an
// attempt to end and something to learn from.
func restartItem(t *testing.T) string {
	t.Helper()
	dir := roundItem(t, roundFailList)
	failRound(t, dir, roundFail...)
	return dir
}

// claimsOK records the item's claims from a file outside the repository.
func claimsOK(t *testing.T, dir string) {
	t.Helper()
	if err := call(t, cmdClaims, dir, "A1", "--file", writeFile(t, t.TempDir(), "claims.md", "CLAIMS:\n- done\n")); err != nil {
		t.Fatalf("claims: %v", err)
	}
}

// failRound runs the item's current list and records a code FAIL round with the given verdicts.
func failRound(t *testing.T, dir string, rows ...string) {
	t.Helper()
	path := runAndVerdicts(t, dir, rows...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "FAIL", "--cause", "code", "--rows", path); err != nil {
		t.Fatalf("FAIL round: %v", err)
	}
}

// passRound runs the item's current list and records a PASS round.
func passRound(t *testing.T, dir string) {
	t.Helper()
	path := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdRound, dir, "A1", "--verdict", "PASS", "--rows", path); err != nil {
		t.Fatalf("PASS round: %v", err)
	}
}

// restartOK restarts the item as a bad-check and returns the event it wrote.
func restartOK(t *testing.T, dir, why, change string) Event {
	t.Helper()
	var err error
	capture(t, func() {
		err = call(t, cmdRestart, dir, "A1", "--reason", "bad-check", "--why", why, "--change", change)
	})
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if ev.T != "restart" {
		t.Fatalf("the restart wrote %q, not a restart event", ev.T)
	}
	return ev
}

// nextAttempt opens the item's next attempt and freezes a list and the claims for it.
func nextAttempt(t *testing.T, dir, list string) {
	t.Helper()
	if err := call(t, cmdOpen, dir, "A1"); err != nil {
		t.Fatalf("open: %v", err)
	}
	checksOK(t, dir, list)
	claimsOK(t, dir)
}

// showText is what `bl show` prints for the item.
func showText(t *testing.T, dir string) string {
	t.Helper()
	var err error
	out := capture(t, func() { err = call(t, cmdShow, dir, "A1") })
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	return out
}

// nextText is what `bl next` prints.
func nextText(t *testing.T, dir string) string {
	t.Helper()
	var err error
	out := capture(t, func() { err = call(t, cmdNext, dir) })
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	return out
}

// ledgerBytes is the ledger as it is on disk: a refusal must leave it byte-identical, not merely
// the same length.
func ledgerBytes(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, ".backlog", "ledger.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// closeItem commits the working tree and closes the item on that commit.
func closeItem(t *testing.T, dir string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte(backlogDir+"/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{{"add", "-A", "."}, {"commit", "--allow-empty", "-qm", "work"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	sha := strings.TrimSpace(string(mustOutput(t, exec.Command("git", "-C", dir, "rev-parse", "HEAD"))))
	if err := call(t, cmdClose, dir, "A1", "--commit", sha); err != nil {
		t.Fatalf("close: %v", err)
	}
	return sha
}

func commitWork(t *testing.T, dir string) string {
	t.Helper()
	for _, argv := range [][]string{{"add", "-A", "."}, {"commit", "-qm", "work"}} {
		if out, err := exec.Command("git", append([]string{"-C", dir}, argv...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", argv, err, out)
		}
	}
	return strings.TrimSpace(string(mustOutput(t, exec.Command("git", "-C", dir, "rev-parse", "HEAD"))))
}

func pinnedChangeItem(t *testing.T) (string, string, string) {
	t.Helper()
	dir := gitItemWith(t, map[string]string{".gitignore": backlogDir + "/\n"})
	list := strings.ReplaceAll(roundList, "echo two", "echo two a.go")
	legacyChecks(t, dir, list)
	claimsOK(t, dir)
	base := commitWork(t, dir)
	writeFile(t, dir, "a.go", "package a\n")
	passRound(t, dir)
	change := commitWork(t, dir)
	return dir, base, change
}

func TestCloseRefusesCommitMissingPinnedChange(t *testing.T) {
	dir, base, _ := pinnedChangeItem(t)
	before := ledgerBytes(t, dir)
	err := call(t, cmdClose, dir, "A1", "--commit", base)
	wantRule(t, err, "R18")
	if !strings.Contains(err.Error(), base) {
		t.Fatalf("refusal does not name %s: %v", base, err)
	}
	if after := ledgerBytes(t, dir); after != before {
		t.Fatal("refused close changed the ledger")
	}
}

func TestCloseAcceptsPinnedChangeWithDirtyArtifactsAndRecordsTree(t *testing.T) {
	dir, _, change := pinnedChangeItem(t)
	last := mustLastRound(t, dir)
	writeFile(t, dir, "TODO.md", "view changed\n")
	writeFile(t, dir, "PROGRESS.md", "view changed\n")
	if err := call(t, cmdClose, dir, "A1", "--commit", change); err != nil {
		t.Fatalf("close: %v", err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	ev := events[len(events)-1]
	if ev.T != "close" || ev.Commit != change || ev.Tree != last.Tree {
		t.Fatalf("close event = %+v; want commit %s and tree %s", ev, change, last.Tree)
	}
}

func TestCloseRefusesCommitAfterVerifiedTreeChanged(t *testing.T) {
	dir, _, _ := pinnedChangeItem(t)
	writeFile(t, dir, "a.go", "package a\n// changed after pass\n")
	later := commitWork(t, dir)
	err := call(t, cmdClose, dir, "A1", "--commit", later)
	wantRule(t, err, "R18")
}

func TestCloseRefusesTamperedEvidence(t *testing.T) {
	dir, _, change := pinnedChangeItem(t)
	last := mustLastRound(t, dir)
	evidence := filepath.Join(dir, last.Rows)
	if err := os.WriteFile(evidence, []byte("tampered evidence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := ledgerBytes(t, dir)
	err := call(t, cmdClose, dir, "A1", "--commit", change)
	wantRule(t, err, "R20")
	if !strings.Contains(err.Error(), filepath.Base(last.Rows)) {
		t.Fatalf("refusal does not name evidence file %s: %v", last.Rows, err)
	}
	if after := ledgerBytes(t, dir); after != before {
		t.Fatal("refused close appended an event")
	}
}

func TestCloseRefusesDeletedEvidence(t *testing.T) {
	dir, _, change := pinnedChangeItem(t)
	last := mustLastRound(t, dir)
	if err := os.Remove(filepath.Join(dir, last.Rows)); err != nil {
		t.Fatal(err)
	}
	before := ledgerBytes(t, dir)
	err := call(t, cmdClose, dir, "A1", "--commit", change)
	wantRule(t, err, "R20")
	if !strings.Contains(err.Error(), filepath.Base(last.Rows)) {
		t.Fatalf("refusal does not name evidence file %s: %v", last.Rows, err)
	}
	if after := ledgerBytes(t, dir); after != before {
		t.Fatal("refused close appended an event")
	}
}

func TestCloseAcceptsUntouchedEvidence(t *testing.T) {
	dir, _, change := pinnedChangeItem(t)
	if err := call(t, cmdClose, dir, "A1", "--commit", change); err != nil {
		t.Fatalf("close with untouched evidence: %v", err)
	}
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if events[len(events)-1].T != "close" {
		t.Fatalf("last event = %q, want close", events[len(events)-1].T)
	}
}

func mustLastRound(t *testing.T, dir string) Event {
	t.Helper()
	events, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].T == "round" {
			return events[i]
		}
	}
	t.Fatal("no round event")
	return Event{}
}

func mustOutput(t *testing.T, cmd *exec.Cmd) []byte {
	t.Helper()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("%v: %v", cmd.Args, err)
	}
	return out
}
