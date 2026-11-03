package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSupervisorPacketReadOnlyAndRestart(t *testing.T) {
	root, _ := loopFixture(t)
	if err := cmdFile(root, argsWith(map[string]string{"id": "S1", "text": "current intent"})); err != nil {
		t.Fatal(err)
	}
	for _, continuation := range []bool{false, true} {
		before := readFile(t, filepath.Join(root, ".backlog", "ledger.jsonl"))
		packet, err := loopSupervisorPacket(root, "S1", "/selected/brain.conf", "/tmp/report", continuation)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"current intent", "attempt: 1; stage: unopened", "--root", root, "/selected/brain.conf", "scripts/dispatch.sh", "CHAIN", "bl open S1", "unknown (not recorded in ledger)", "playbooks/item-loop/index.md", "Prefix EVERY Bash/tool shell call", "same call", "Use this packet for startup", "Consult only the relevant section", "without a specific missing fact", "bl show S1 --json on resume, external changes or a CLI refusal", "never dump the ledger or full history", "record the round before any other ledger write", "filing a follow-up", "already void window permits checks --amend"} {
			if !strings.Contains(packet, want) {
				t.Errorf("packet missing %q", want)
			}
		}
		if strings.Contains(packet, "HANDOFF") || strings.Contains(packet, "playbooks/item-loop.md") {
			t.Fatal("stale discovery guidance")
		}
		if continuation != strings.Contains(packet, "Continuation after context limit") {
			t.Fatal("continuation guidance mismatch")
		}
		if got := readFile(t, filepath.Join(root, ".backlog", "ledger.jsonl")); got != before {
			t.Fatal("packet mutated ledger")
		}
	}
	root = restartItem(t)
	if err := call(t, cmdRestart, root, "A1", "--reason", "bad-check", "--why", "invalid check", "--change", "repair premise"); err != nil {
		t.Fatal(err)
	}
	packet, err := loopSupervisorPacket(root, "A1", "conf", "out", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"attempt: 2; stage: unopened", ".backlog/items/A1/a2/brief.md", ".backlog/items/A1/a1-failure.md", "without scope flags to preserve the inherited lists", "replace both lists"} {
		if !strings.Contains(packet, want) {
			t.Errorf("restart packet missing %q", want)
		}
	}
}

func TestSupervisorNextCurrentAttempt(t *testing.T) {
	cases := []struct {
		item    Item
		stage   string
		command string
	}{
		{Item{ID: "X", Status: "open", Opened: true}, "brief", "bl brief X"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief"}, "checks", "--file <checks.tsv> --tests <tests.py>"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief", Checks: "checks"}, "claims", "bl claims X"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief", Checks: "checks", Claims: "claims"}, "run", "bl run X"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief", Checks: "checks", Claims: "claims", Runs: []Event{{Seq: 2}}}, "verification", "--rows <rows.tsv> --report <report.md>"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief", Checks: "checks", Claims: "claims", Runs: []Event{{Seq: 2}}, Rounds: []Event{{Seq: 3, Verdict: "FAIL", Cause: "code"}}}, "failed", "bl run X"},
		{Item{ID: "X", Status: "open", Opened: true, Brief: "brief", Checks: "checks", Claims: "claims", Rounds: []Event{{Seq: 3, Verdict: "PASS"}}}, "passed", "bl close X"},
	}
	for _, tc := range cases {
		stage, next := supervisorNext(&tc.item)
		if stage != tc.stage || !strings.Contains(next, tc.command) {
			t.Errorf("%s: %s %s", tc.stage, stage, next)
		}
	}
}

func TestBrainNativeCompactionConfig(t *testing.T) {
	// Inspect configuration only: never execute Claude or dispatch.
	data, err := os.ReadFile(filepath.Join("..", "..", "bl-loop.dispatch.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(data), "--autocompact 120000") != 1 {
		t.Fatal("brain native compaction window missing or duplicated")
	}
	loop, err := os.ReadFile("loop.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(loop), "14400, 130000, 5") {
		t.Fatal("independent context fallback changed")
	}
}
