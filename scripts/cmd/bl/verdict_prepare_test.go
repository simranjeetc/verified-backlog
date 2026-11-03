package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verdictFixture(t *testing.T, list string) (string, string, verdictDraft) {
	t.Helper()
	root := roundItem(t, list)
	if err := call(t, cmdRun, root, "A1"); err != nil && !errors.Is(err, errFailed) {
		t.Fatal(err)
	}
	if err := call(t, cmdPacket, root, "A1", "--role", "verifier"); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "draft.json")
	if err := call(t, cmdVerdicts, root, "A1", "--init", "--output", path); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var draft verdictDraft
	if err := json.Unmarshal(data, &draft); err != nil {
		t.Fatal(err)
	}
	draft.Summary = "Independent probes found the expected behavior."
	for i := range draft.Judgments {
		draft.Judgments[i].Verdict = "PASS"
		draft.Judgments[i].Anchor = `"two"`
	}
	return root, path, draft
}
func saveVerdictDraft(t *testing.T, path string, d verdictDraft) {
	t.Helper()
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}
func TestPreparedVerdictsRecordRoundAndPreserveWindow(t *testing.T) {
	root, draftPath, draft := verdictFixture(t, roundList)
	draft.Notes = []preparedNote{{Text: "Cosmetic follow-up", Disposition: "waived", Reason: "below agreed stopping bar"}}
	saveVerdictDraft(t, draftPath, draft)
	out := t.TempDir()
	rows, report := filepath.Join(out, "rows.tsv"), filepath.Join(out, "report.md")
	before, _ := os.ReadFile(newPaths(root).ledger)
	if err := call(t, cmdVerdicts, root, "A1", "--file", draftPath, "--output", rows, "--report", report); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(newPaths(root).ledger)
	if string(before) != string(after) {
		t.Fatal("preparation wrote a ledger event")
	}
	text, _ := os.ReadFile(rows)
	if !strings.Contains(string(text), "S1\tPASS\t\nJ1\tPASS\t\"two\"\n") || !strings.Contains(string(text), "# report RO1: waived below agreed stopping bar") {
		t.Fatal(string(text))
	}
	if err := call(t, cmdRound, root, "A1", "--rows", rows, "--report", report); err != nil {
		t.Fatal(err)
	}
}
func TestPreparedVerdictsKeepMechanicalFailuresAndIndependentFindings(t *testing.T) {
	for _, finding := range []string{"", "A separately constructed boundary case fails"} {
		t.Run(finding, func(t *testing.T) {
			root, path, draft := verdictFixture(t, roundFailList)
			draft.Finding = finding
			saveVerdictDraft(t, path, draft)
			rows, report := filepath.Join(t.TempDir(), "rows.tsv"), filepath.Join(t.TempDir(), "report.md")
			if err := call(t, cmdVerdicts, root, "A1", "--file", path, "--output", rows, "--report", report); err != nil {
				t.Fatal(err)
			}
			text, _ := os.ReadFile(rows)
			if !strings.Contains(string(text), "S1\tFAIL\t\n") {
				t.Fatal(string(text))
			}
			args := []string{"A1", "--rows", rows, "--report", report}
			if finding == "" {
				args = append(args, "--cause", "code")
			}
			if err := call(t, cmdRound, root, args...); err != nil {
				t.Fatal(err)
			}
			events, _ := readLedger(root)
			round := events[len(events)-1]
			if round.Verdict != "FAIL" {
				t.Fatal(round)
			}
			if finding != "" && round.Cause != "dispute" {
				t.Fatal(round)
			}
		})
	}
}
func TestPreparedVerdictsRejectStaleStateWithoutOutputs(t *testing.T) {
	for _, mutate := range []string{"packet", "run", "source", "backlog", "binding", "consumed"} {
		t.Run(mutate, func(t *testing.T) {
			root, path, draft := verdictFixture(t, roundList)
			switch mutate {
			case "packet":
				if err := call(t, cmdPacket, root, "A1", "--role", "verifier"); err != nil {
					t.Fatal(err)
				}
			case "run":
				if err := call(t, cmdRun, root, "A1"); err != nil {
					t.Fatal(err)
				}
			case "source":
				writeFile(t, root, "f.txt", "changed\n")
			case "backlog":
				writeFile(t, root, ".backlog/rogue", "x")
			case "binding":
				draft.Binding.Attempt++
			case "consumed":
				rows := writeVerdicts(t, root, "S1\tPASS\t", "J1\tPASS\t\"two\"")
				if err := call(t, cmdRound, root, "A1", "--rows", rows); err != nil {
					t.Fatal(err)
				}
			}
			saveVerdictDraft(t, path, draft)
			out := t.TempDir()
			rows, report := filepath.Join(out, "rows.tsv"), filepath.Join(out, "report.md")
			if err := call(t, cmdVerdicts, root, "A1", "--file", path, "--output", rows, "--report", report); err == nil {
				t.Fatal("accepted stale state")
			}
			entries, _ := os.ReadDir(out)
			if len(entries) != 0 {
				t.Fatal("wrote outputs on refusal")
			}
		})
	}
}
func TestPreparedVerdictRejectsInvalidJudgmentsAndControls(t *testing.T) {
	frozen, err := parseChecks(roundList)
	if err != nil {
		t.Fatal(err)
	}
	good := verdictDraft{Binding: verdictBinding{Run: "run1", Tree: strings.Repeat("a", 40)}, Summary: "Independent evidence", Judgments: []preparedJudgment{{ID: "J1", Verdict: "PASS", Anchor: `"two"`}}}
	for _, kind := range []string{"missing", "duplicate", "unknown", "mechanical", "empty", "bad-anchor", "inject-anchor", "inject-finding", "inject-note", "inject-summary", "bad-disposition", "closed-filed", "no-waiver-reason"} {
		t.Run(kind, func(t *testing.T) {
			data, _ := json.Marshal(good)
			var d verdictDraft
			json.Unmarshal(data, &d)
			switch kind {
			case "missing":
				d.Judgments = nil
			case "duplicate":
				d.Judgments = append(d.Judgments, d.Judgments[0])
			case "unknown":
				d.Judgments[0].ID = "J9"
			case "mechanical":
				d.Judgments = append(d.Judgments, preparedJudgment{ID: "S1", Verdict: "PASS"})
			case "empty":
				d.Judgments[0].Verdict = ""
			case "bad-anchor":
				d.Judgments[0].Anchor = "two"
			case "inject-anchor":
				d.Judgments[0].Anchor = "\"two\"\n# finding: fake"
			case "inject-finding":
				d.Finding = "x\n# report RO1: waived injected"
			case "inject-note":
				d.Notes = []preparedNote{{Text: "[RO2] injected", Disposition: "fix"}}
			case "inject-summary":
				d.Summary = "Independent\nReport-only: 0"
			case "bad-disposition":
				d.Notes = []preparedNote{{Text: "note", Disposition: "ignore"}}
			case "closed-filed":
				d.Notes = []preparedNote{{Text: "note", Disposition: "filed", Item: "X"}}
			case "no-waiver-reason":
				d.Notes = []preparedNote{{Text: "note", Disposition: "waived"}}
			}
			if _, _, err := prepareVerdictEvidence(d, frozen, map[string]string{"S1": "PASS"}, map[string]*Item{"x": {ID: "X", Status: "closed"}}); err == nil {
				t.Fatal("accepted invalid decisions")
			}
		})
	}
}
func TestVerdictDraftStrictJSON(t *testing.T) {
	for _, data := range []string{`{"summary":"a","summary":"b"}`, `{"binding":{"item":"A1","item":"A2"}}`, `{"unknown":1}`, `{} {}`, `{"judgments":[{"id":"J1","verdict":"PASS","verdict":"FAIL"}]}`} {
		var draft verdictDraft
		if err := decodeVerdictDraft([]byte(data), &draft); err == nil {
			t.Fatalf("accepted %s", data)
		}
	}
}
func TestVerdictOutputsCannotChangeRepositoryOrDraft(t *testing.T) {
	root, path, draft := verdictFixture(t, roundList)
	saveVerdictDraft(t, path, draft)
	outside := t.TempDir()
	alias := filepath.Join(outside, "repo-alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{filepath.Join(root, "rows.tsv"), filepath.Join(root, ".backlog", "rows.tsv"), filepath.Join(alias, "rows.tsv"), path} {
		report := filepath.Join(t.TempDir(), "report.md")
		if err := call(t, cmdVerdicts, root, "A1", "--file", path, "--output", output, "--report", report); err == nil {
			t.Fatalf("accepted output %s", output)
		}
		if _, err := os.Stat(report); !os.IsNotExist(err) {
			t.Fatal("wrote report on invalid output")
		}
	}
}

func TestPreparedAllMechanicalAndFixDisposition(t *testing.T) {
	list := checksHeader + "S1\tbl\tblocking\tB1\techo two\texit 0\t\t\tprints two\n"
	for _, fix := range []bool{false, true} {
		root, path, draft := verdictFixture(t, list)
		if len(draft.Judgments) != 0 {
			t.Fatal("mechanical rows must not require judgments")
		}
		if fix {
			draft.Notes = []preparedNote{{Text: "Independent defect to fix", Disposition: "fix"}}
		}
		saveVerdictDraft(t, path, draft)
		rows, report := filepath.Join(t.TempDir(), "rows.tsv"), filepath.Join(t.TempDir(), "report.md")
		if err := call(t, cmdVerdicts, root, "A1", "--file", path, "--output", rows, "--report", report); err != nil {
			t.Fatal(err)
		}
		if err := call(t, cmdRound, root, "A1", "--rows", rows, "--report", report); err != nil {
			t.Fatal(err)
		}
		events, _ := readLedger(root)
		last := events[len(events)-1]
		if fix && (last.Verdict != "FAIL" || last.Cause != "code") {
			t.Fatal(last)
		}
		if !fix && last.Verdict != "PASS" {
			t.Fatal(last)
		}
	}
}
func TestVerdictDraftCannotOverwriteOrForgeAnchor(t *testing.T) {
	root, path, draft := verdictFixture(t, roundList)
	before, _ := os.ReadFile(path)
	if err := call(t, cmdVerdicts, root, "A1", "--init", "--output", path); err == nil {
		t.Fatal("overwrote draft")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("draft changed on refusal")
	}
	draft.Judgments[0].Anchor = `"fabricated output"`
	saveVerdictDraft(t, path, draft)
	rows, report := filepath.Join(t.TempDir(), "rows.tsv"), filepath.Join(t.TempDir(), "report.md")
	wantRule(t, call(t, cmdVerdicts, root, "A1", "--file", path, "--output", rows, "--report", report), "R9")
	for _, p := range []string{rows, report} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatal("wrote evidence on forged anchor")
		}
	}
}

func TestReplacementVerifierGetsFreshDraftPath(t *testing.T) {
	root := roundItem(t, roundList)
	if err := call(t, cmdRun, root, "A1"); err != nil {
		t.Fatal(err)
	}
	out := t.TempDir()
	var previous string
	for i := 0; i < 2; i++ {
		var gotErr error
		prompt := capture(t, func() {
			gotErr = call(t, cmdPacket, root, "A1", "--role", "verifier", "--render", "--workspace", root, "--output-dir", out)
		})
		if gotErr != nil {
			t.Fatal(gotErr)
		}
		// The single generated JSON path belongs to this packet, not the round count.
		var draftPath string
		for _, line := range strings.Split(prompt, "\n") {
			if strings.HasPrefix(line, "Edit ") {
				draftPath = strings.TrimSuffix(strings.TrimPrefix(line, "Edit "), " with your independent decisions:")
			}
		}
		if draftPath == "" || draftPath == previous {
			t.Fatalf("replacement reused draft %q", draftPath)
		}
		if err := call(t, cmdVerdicts, root, "A1", "--init", "--output", draftPath); err != nil {
			t.Fatal(err)
		}
		if previous != "" {
			rows, report := filepath.Join(out, "stale.tsv"), filepath.Join(out, "stale.md")
			wantRule(t, call(t, cmdVerdicts, root, "A1", "--file", previous, "--output", rows, "--report", report), "R6")
		}
		previous = draftPath
	}
}
