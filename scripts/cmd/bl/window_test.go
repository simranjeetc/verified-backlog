package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPacketWindowRefusesChangedLedgerPrefix(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, backlogDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := []byte("{\"seq\":1,\"t\":\"file\",\"id\":\"A1\"}\n")
	if err := os.WriteFile(filepath.Join(base, ledgerName), ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, runlogName), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := backlogDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	packet := Event{T: "packet", ID: "A1", Seq: 2, LedgerLen: intRef(len(ledger)), LedgerSHA: shaHex(ledger), RunlogSHA: shaHex(nil), BacklogSHA: digest}
	ledger[0] = '['
	if err := os.WriteFile(filepath.Join(base, ledgerName), ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	err = verifyPacketWindow(dir, []Event{{Seq: 1, T: "file", ID: "A1"}, packet}, &Item{ID: "A1"})
	if r, ok := err.(Refusal); !ok || r.rule != "R39" {
		t.Fatalf("window error = %v, want R39", err)
	}
}

func TestPacketWindowAllowsNotesAndPacketEvents(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, backlogDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := []byte("prefix\n")
	if err := os.WriteFile(filepath.Join(base, ledgerName), ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	runlog := []byte("logged\n")
	if err := os.WriteFile(filepath.Join(base, runlogName), runlog, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := backlogDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	packet := Event{T: "packet", ID: "A1", Seq: 2, LedgerLen: intRef(len(ledger)), LedgerSHA: shaHex(ledger), RunlogLen: intRef(len(runlog)), RunlogSHA: shaHex(runlog), BacklogSHA: digest}
	if err := os.WriteFile(filepath.Join(base, ledgerName), []byte("prefix\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(base, runlogName), []byte("logged\nmore\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := []Event{{Seq: 1, T: "file", ID: "A1"}, packet, {Seq: 3, T: "note", ID: "A1"}, {Seq: 4, T: "packet", ID: "Z9"}}
	if err := verifyPacketWindow(dir, events, &Item{ID: "A1"}); err != nil {
		t.Fatalf("legitimate window refused: %v", err)
	}
}

func TestPacketWindowNamesUnexpectedEvent(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, backlogDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	ledger := []byte("prefix\n")
	if err := os.WriteFile(filepath.Join(base, ledgerName), ledger, 0o644); err != nil {
		t.Fatal(err)
	}
	digest, err := backlogDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	packet := Event{T: "packet", ID: "A1", Seq: 2, LedgerLen: intRef(len(ledger)), LedgerSHA: shaHex(ledger), RunlogSHA: shaHex(nil), BacklogSHA: digest}
	if err := os.WriteFile(filepath.Join(base, ledgerName), []byte("prefix\nnext\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err = verifyPacketWindow(dir, []Event{{Seq: 1, T: "file", ID: "A1"}, packet, {Seq: 3, T: "file", ID: "Z9"}}, &Item{ID: "A1"})
	if err == nil || !strings.Contains(err.Error(), "seq 3 type file") {
		t.Fatalf("window error = %v, want seq and type", err)
	}
}

func TestRoundRefusesBacklogChangeAfterVerifierPacket(t *testing.T) {
	dir := runItem(t, oneRowList)
	if err := call(t, cmdPacket, dir, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, backlogDir, "loop.lock"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	before, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	r := wantRefusal(t, call(t, cmdRound, dir, "A1", "--cause", "harness", "--verdict", "FAIL"))
	if r.rule != "R39" {
		t.Fatalf("refusal rule = %q, want R39", r.rule)
	}
	after, err := readLedger(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(before) {
		t.Fatalf("refused round wrote an event: before %d, after %d", len(before), len(after))
	}
}

func TestRoundAllowsWindowNotesAndReadOnlyCommands(t *testing.T) {
	dir := roundItem(t, roundList)
	rows := runAndVerdicts(t, dir, roundPass...)
	if err := call(t, cmdPacket, dir, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdShow, dir, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdNote, dir, "A1", "--text", "window note"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdStatus, dir); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdRound, dir, "A1", "--rows", rows); err != nil {
		t.Fatalf("legitimate packet window refused: %v", err)
	}
}

func TestBacklogDigestCountsLoopFilesByPresence(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(dir, backlogDir)
	if err := os.MkdirAll(base, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(base, "loop.json")
	if err := os.WriteFile(path, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	first, err := backlogDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("second"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := backlogDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("loop.json contents changed the digest: %s != %s", second, first)
	}
}
