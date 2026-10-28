package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ── the order of work (I79) ──────────────────────────────────────────────────

// orderLedger initialises a ledger in a temp dir and runs each argv against it.
func orderLedger(t *testing.T, argvs ...[]string) string {
	t.Helper()
	dir := t.TempDir()
	for _, argv := range append([][]string{{"init"}}, argvs...) {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	return dir
}

func nextID(t *testing.T, dir string) string {
	t.Helper()
	var err error
	out := capture(t, func() { err = call(t, cmdNext, dir) })
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	id, _, _ := strings.Cut(strings.TrimSpace(out), "\t")
	return id
}

// Tier 1 is every bump newest first, then tier 2 is the latest ranking, then tier 3 is every
// other item, the ones the most open items wait on first, then in filing order.
func TestWorkOrderIsTierOneThenTwoThenThree(t *testing.T) {
	events := []Event{
		{Seq: 1, T: "file", ID: "A1", Text: "x"},
		{Seq: 2, T: "file", ID: "A2", Text: "x"},
		{Seq: 3, T: "file", ID: "A3", Text: "x"},
		{Seq: 4, T: "file", ID: "A4", Text: "x"},
		{Seq: 5, T: "file", ID: "A5", Text: "x"},
		{Seq: 6, T: "file", ID: "A6", Text: "x", After: "A5"},
		{Seq: 7, T: "file", ID: "A7", Text: "x", After: "A5"},
		{Seq: 8, T: "file", ID: "A8", Text: "x", After: "A4"},
		{Seq: 9, T: "bump", IDs: []string{"A2", "A1"}, By: "human"},
		{Seq: 10, T: "bump", IDs: []string{"A1"}, By: "human"},
		{Seq: 11, T: "rank", Ranking: []Ranked{{"A6", "r"}, {"A4", "r"}}, By: "loop"},
		{Seq: 12, T: "rank", Ranking: []Ranked{{"A3", "r"}}, By: "loop"},
	}
	order, byID, err := derive(events)
	if err != nil {
		t.Fatalf("derive: %v", err)
	}
	got := strings.Join(ids(workOrder(events, order, byID)), " ")
	if want := "A1 A2 A3 A5 A4 A6 A7 A8"; got != want {
		t.Fatalf("work order = %s, want %s", got, want)
	}
	if got := strings.Join(ids(eligible(workOrder(events, order, byID))), " "); got != "A1 A2 A3 A5 A4" {
		t.Fatalf("eligible work order = %s", got)
	}
}

func TestUnbumpTakesAnIdOutOfTierOneAndABumpPutsItBack(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"file", "--id", "A2", "--text", "x"},
		[]string{"bump", "A2"},
	)
	if got := nextID(t, dir); got != "A2" {
		t.Fatalf("after bump A2, next = %s", got)
	}
	if code := run([]string{"--root", dir, "unbump", "A2"}); code != 0 {
		t.Fatalf("unbump: exit %d", code)
	}
	if got := nextID(t, dir); got != "A1" {
		t.Fatalf("after unbump A2, next = %s, want A1", got)
	}
	if code := run([]string{"--root", dir, "bump", "A2"}); code != 0 {
		t.Fatalf("bump: exit %d", code)
	}
	if got := nextID(t, dir); got != "A2" {
		t.Fatalf("after bump A2 again, next = %s, want A2", got)
	}
}

// `bl file --urgent` puts the item at the front of tier 1, ahead of an earlier bump.
func TestFileUrgentGoesToTheFrontOfTierOne(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"bump", "A1"},
		[]string{"file", "--id", "U1", "--text", "x", "--urgent"},
	)
	if got := nextID(t, dir); got != "U1" {
		t.Fatalf("next = %s, want the urgent U1", got)
	}
	events, _ := readLedger(dir)
	if last := events[len(events)-1]; last.T != "file" || !last.Urgent || last.By != "human" {
		t.Fatalf("urgent file event = %+v", last)
	}
}

// Tier 3: the item the most open items wait on comes first.
func TestTierThreePutsTheMostWaitedOnFirst(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"file", "--id", "A2", "--text", "x"},
		[]string{"file", "--id", "A3", "--text", "x", "--after", "A2"},
	)
	if got := nextID(t, dir); got != "A2" {
		t.Fatalf("next = %s, want A2 (A3 waits on it)", got)
	}
}

// A blocked or parked tier-1 item is never named, and does not stop the next one.
func TestABlockedOrParkedTierOneItemIsSkipped(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"file", "--id", "A2", "--text", "x", "--after", "A1"},
		[]string{"file", "--id", "A3", "--text", "x"},
		[]string{"file", "--id", "A4", "--text", "x"},
		[]string{"defer", "A3", "--reason", "later"},
		[]string{"bump", "A2", "A3", "A4"},
	)
	if got := nextID(t, dir); got != "A4" {
		t.Fatalf("next = %s, want A4", got)
	}
}

func TestPriorityEventsRecordWho(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"file", "--id", "A2", "--text", "x"},
		[]string{"bump", "A2", "a1"},
		[]string{"unbump", "A1"},
		[]string{"rank", "--file", writeFile(t, t.TempDir(), "r.tsv", "A1\tunblocks the rest\n")},
	)
	events, _ := readLedger(dir)
	want := []struct{ t, by, ids string }{{"bump", "human", "A2 A1"}, {"unbump", "human", "A1"}, {"rank", "loop", "A1"}}
	tail := events[len(events)-3:]
	for i, w := range want {
		if tail[i].T != w.t || tail[i].By != w.by || strings.Join(namedIDs(tail[i]), " ") != w.ids {
			t.Errorf("event %d = %+v, want %s by %s naming %s", i, tail[i], w.t, w.by, w.ids)
		}
	}
	if tail[2].Ranking[0].Reason != "unblocks the rest" {
		t.Errorf("rank event lost its reason: %+v", tail[2].Ranking)
	}
	var err error
	capture(t, func() { err = call(t, cmdCheck, dir) })
	if err != nil {
		t.Fatalf("check over the new event kinds: %v", err)
	}
}

// Every refusal exits 2 and writes nothing.
func TestOrderRefusals(t *testing.T) {
	dir := orderLedger(t,
		[]string{"file", "--id", "A1", "--text", "x"},
		[]string{"file", "--id", "A2", "--text", "x"},
		[]string{"file", "--id", "D1", "--text", "x"},
		[]string{"drop", "D1", "--reason", "no"},
		[]string{"file", "--id", "C1", "--text", "x"},
		[]string{"bump", "A1"},
	)
	// a closed item: the close event is appended directly, since a real close needs git
	if _, err := appendEvent(dir, Event{T: "close", ID: "C1", Commit: "abc1234"}); err != nil {
		t.Fatal(err)
	}
	rank := func(text string) []string {
		return []string{"rank", "--file", writeFile(t, t.TempDir(), "r.tsv", text)}
	}
	cases := [][]string{
		{"bump", "Z9"}, {"bump", "D1"}, {"bump", "C1"}, {"bump", "A2", "a2"},
		{"unbump", "Z9"}, {"unbump", "D1"}, {"unbump", "C1"}, {"unbump", "A2", "A2"},
		rank("A1\tbumped\n"), rank("Z9\tunknown\n"), rank("D1\tdropped\n"), rank("C1\tclosed\n"),
		rank("A2\tonce\nA2\ttwice\n"), rank("A2\t  \n"), rank("A2\n"),
	}
	before, _ := os.ReadFile(filepath.Join(dir, ".backlog", "ledger.jsonl"))
	for _, argv := range cases {
		var code int
		captureStderr(t, func() { code = run(append([]string{"--root", dir}, argv...)) })
		if code != 2 {
			t.Errorf("bl %v: exit %d, want 2", argv, code)
		}
	}
	after, _ := os.ReadFile(filepath.Join(dir, ".backlog", "ledger.jsonl"))
	if string(before) != string(after) {
		t.Fatal("a refused call wrote to the ledger")
	}
}
