package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleEvents() []Event {
	return []Event{
		{Seq: 1, At: "2026-10-20T09:00:00Z", T: "file", ID: "A1", Text: "first"},
		{Seq: 2, At: "2026-10-20T09:01:00Z", T: "file", ID: "A2", Text: "second", After: "A1"},
		{Seq: 3, At: "2026-10-20T09:02:00Z", T: "open", ID: "A1"},
		{Seq: 4, At: "2026-10-20T09:03:00Z", T: "round", ID: "A1", Verdict: "FAIL", Cause: "code", Rows: "e", RowsSHA: "x"},
		{Seq: 5, At: "2026-10-20T09:04:00Z", T: "round", ID: "A1", Verdict: "PASS", Rows: "e", RowsSHA: "x"},
		{Seq: 6, At: "2026-10-20T09:05:00Z", T: "close", ID: "A1", Commit: "abc1234"},
	}
}

func TestDeriveStates(t *testing.T) {
	order, byID, err := derive(sampleEvents())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if len(order) != 2 {
		t.Fatalf("want 2 items, got %d", len(order))
	}
	if got := byID["a1"].Status; got != "closed" {
		t.Errorf("A1 status = %q, want closed", got)
	}
	if got := byID["a1"].codeFails(); got != 1 {
		t.Errorf("A1 code fails = %d, want 1", got)
	}
	if got := byID["a2"].BlockedBy; got != "" {
		t.Errorf("A2 blocked by %q, want none after the dependency closed", got)
	}
}

func TestBlockedUntilDependencyCloses(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second", After: "A1"},
	}
	_, byID, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if got := byID["a2"].BlockedBy; got != "A1" {
		t.Errorf("BlockedBy = %q, want A1", got)
	}
}

func TestDeriveRejectsDuplicateFile(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "a1", Text: "again"},
	}
	if _, _, err := derive(events); err == nil {
		t.Fatal("expected corruption for a duplicate id")
	}
}

func TestPinnedRefusesTwo(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second"},
		{Seq: 3, T: "open", ID: "A1"},
		{Seq: 4, T: "open", ID: "A2"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	if _, err := pinned(order); err == nil {
		t.Fatal("expected corruption when two items are in flight")
	}
}

func TestEligibleSkipsBlockedAndParked(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "first"},
		{Seq: 2, T: "file", ID: "A2", Text: "second", After: "A1"},
		{Seq: 3, T: "file", ID: "O4", Text: "parked"},
		{Seq: 4, T: "defer", ID: "O4", Reason: "later"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := eligible(order)
	if len(got) != 1 || got[0].ID != "A1" {
		t.Fatalf("eligible = %v, want just A1", ids(got))
	}
}

func ids(items []*Item) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.ID
	}
	return out
}

func TestParseArgs(t *testing.T) {
	a, err := parseArgs([]string{"A1", "--verdict", "FAIL", "--cause=code", "--rows", "-"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.pos(0) != "A1" {
		t.Errorf("positional = %q", a.pos(0))
	}
	if a.get("verdict") != "FAIL" || a.get("cause") != "code" || a.get("rows") != "-" {
		t.Errorf("flags = %v", a.flags)
	}
	if _, err := parseArgs([]string{"--verdict"}); err == nil {
		t.Error("expected a refusal for a flag with no value")
	}
}

func TestParseSince(t *testing.T) {
	cases := map[string]time.Duration{"2d": 48 * time.Hour, "36h": 36 * time.Hour, "1w": 168 * time.Hour}
	for in, want := range cases {
		got, err := parseSince(in)
		if err != nil || got != want {
			t.Errorf("parseSince(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := parseSince("soon"); err == nil {
		t.Error("expected a refusal for an unparseable period")
	}
}

func TestRenderQueueGroups(t *testing.T) {
	order, _, err := derive(sampleEvents())
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	out := renderQueue("/tmp/pilot", order)
	for _, want := range []string{"## Queue (1)", "## Closed (1)", "- [x] A1 — first", "- [ ] A2 — second"} {
		if !strings.Contains(out, want) {
			t.Errorf("queue missing %q", want)
		}
	}
}

func TestRenderRecordsShowsDecisionNotVerdict(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "D1", Text: "cosmetic"},
		{Seq: 2, T: "drop", ID: "D1", Reason: "not worth it"},
	}
	order, _, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	out := renderRecords("/tmp/pilot", order)
	if !strings.Contains(out, "Decision: declined — not worth it (not verified)") {
		t.Fatalf("declined record must not look like a verdict:\n%s", out)
	}
	if strings.Contains(out, "Verdict: PASS") {
		t.Fatal("a decline must never render as a PASS")
	}
}

func TestParseLegacy(t *testing.T) {
	dir := t.TempDir()
	todo := filepath.Join(dir, "TODO.md")
	progress := filepath.Join(dir, "PROGRESS.md")
	os.WriteFile(todo, []byte("- [x] A1 first — **PASS**: done (commit abcdef1)\n- [ ] A2 second\n- [ ] no id\n"), 0o644)
	os.WriteFile(progress, []byte("### A1 — first — PASS\n\nVerdict: PASS\n\nclosed at abcdef1\n"), 0o644)

	items, findings, err := parseLegacy(todo, progress)
	if err != nil {
		t.Fatalf("parseLegacy: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("want 2 items, got %d", len(items))
	}
	if !items[0].Closed || items[0].Commit != "abcdef1" {
		t.Errorf("A1 = %+v, want closed at abcdef1", items[0])
	}
	if len(findings) != 1 || !strings.Contains(findings[0], "checkbox without a readable id") {
		t.Errorf("findings = %v", findings)
	}
}

func TestSchemaIsJSON(t *testing.T) {
	s := schemaJSON()
	if s["version"] != 1 {
		t.Errorf("version = %v", s["version"])
	}
	events := s["events"].(map[string]map[string][]string)
	if len(events) != 8 {
		t.Errorf("want 8 event kinds, got %d", len(events))
	}
}
