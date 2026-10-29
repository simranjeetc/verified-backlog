package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoopTokenUsageStreaming(t *testing.T) {
	a := `{"type":"assistant","message":{"id":"a","usage":{"input_tokens":2,"cache_creation_input_tokens":10,"cache_read_input_tokens":100,"output_tokens":1}}}`
	updated := `{"type":"assistant","message":{"id":"a","usage":{"output_tokens":40}}}`
	b := `{"type":"assistant","message":{"id":"b","usage":{"input_tokens":3,"cache_creation_input_tokens":20,"cache_read_input_tokens":120,"output_tokens":5}}}`
	checkpoint := `{"type":"result","usage":{"output_tokens":999999},"modelUsage":{"opus":{"inputTokens":2,"cacheCreationInputTokens":10,"cacheReadInputTokens":100,"outputTokens":40}}}`
	final := `{"type":"result","modelUsage":{"opus":{"inputTokens":5,"cacheCreationInputTokens":30,"cacheReadInputTokens":220,"outputTokens":70}}}`
	cases := []struct {
		name, log string
		want      loopTokenUsage
		peak      int
	}{
		{"partial duplicate and stale", strings.Join([]string{a, updated, a, a}, "\n"), loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 40, Provisional: true}, 112},
		{"distinct messages", a + "\n" + b, loopTokenUsage{Input: 5, CacheWrite: 30, CacheRead: 220, Output: 6, Provisional: true}, 143},
		{"result finalizes missing output", a + "\n" + checkpoint, loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 40}, 112},
		{"result repeated not added", a + "\n" + checkpoint + "\n" + checkpoint, loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 40}, 112},
		{"work after result", a + "\n" + checkpoint + "\n" + b, loopTokenUsage{Input: 5, CacheWrite: 30, CacheRead: 220, Output: 45, Provisional: true}, 143},
		{"later cumulative result", strings.Join([]string{a, checkpoint, b, final, final}, "\n"), loopTokenUsage{Input: 5, CacheWrite: 30, CacheRead: 220, Output: 70}, 143},
		{"old duplicate after result", strings.Join([]string{a, checkpoint, a, updated}, "\n"), loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 40}, 112},
		{"malformed and unrelated", strings.Join([]string{`not json`, `{"type":"user","message":{"id":"x","usage":{"input_tokens":999}}}`, a, `{"type":"assistant","message":`}, "\n"), loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 1, Provisional: true}, 112},
		{"incomplete checkpoint ignored", a + "\n" + `{"type":"result","modelUsage":{"opus":{"outputTokens":40}}}`, loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 1, Provisional: true}, 112},
	}
	cases = append(cases, struct {
		name, log string
		want      loopTokenUsage
		peak      int
	}{"stale result preserves pending", strings.Join([]string{a, checkpoint, b, checkpoint}, "\n"), loopTokenUsage{Input: 5, CacheWrite: 30, CacheRead: 220, Output: 45, Provisional: true}, 143})
	cases = append(cases, struct {
		name, log string
		want      loopTokenUsage
		peak      int
	}{"older result ignored", strings.Join([]string{a, checkpoint, b, final, checkpoint}, "\n"), loopTokenUsage{Input: 5, CacheWrite: 30, CacheRead: 220, Output: 70}, 143})
	cases = append(cases, struct {
		name, log string
		want      loopTokenUsage
		peak      int
	}{"nested request excluded", a + "\n" + `{"type":"assistant","parent_tool_use_id":"child","message":{"id":"nested","usage":{"input_tokens":999999}}}`, loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 1, Provisional: true}, 112})
	cases = append(cases, struct {
		name, log string
		want      loopTokenUsage
		peak      int
	}{"multiple models", a + "\n" + `{"type":"result","modelUsage":{"opus":{"inputTokens":2,"cacheCreationInputTokens":10,"cacheReadInputTokens":100,"outputTokens":40},"haiku":{"inputTokens":1,"cacheCreationInputTokens":2,"cacheReadInputTokens":3,"outputTokens":4}}}`, loopTokenUsage{Input: 3, CacheWrite: 12, CacheRead: 103, Output: 44}, 112})

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, peak, err := parseLoopTokenUsage([]byte(tc.log))
			if err != nil || got != tc.want || peak != tc.peak {
				t.Fatalf("got %+v peak %d err %v; want %+v peak %d", got, peak, err, tc.want, tc.peak)
			}
		})
	}
}

func TestLoopTokenUsageSavedI136(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "loop-usage-i136.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	got, peak, err := parseLoopTokenUsage(data)
	want := loopTokenUsage{Input: 204, CacheWrite: 177798, CacheRead: 13274604, Output: 55306}
	if err != nil || got != want || peak != 190151 {
		t.Fatalf("got %+v peak %d err %v", got, peak, err)
	}
}

func TestLoopUsageSummary(t *testing.T) {
	u := loopTokenUsage{Input: 2, CacheWrite: 10, CacheRead: 100, Output: 40}
	want := "A1 session 2 peak-context 112 processed-tokens 152 (input 2 cache-write 10 cache-read 100 output 40) usage finalized"
	if got := loopUsageSummary("A1", 2, 112, u); got != want {
		t.Fatalf("got %q", got)
	}
	u.Provisional = true
	if !strings.HasSuffix(loopUsageSummary("A1", 2, 112, u), "usage provisional") {
		t.Fatal("missing provisional label")
	}
}

func TestLoopUsageLogMissingAndUnreadable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("DISPATCH_HOME", home)
	if got, peak, err := loopJobTokenUsage("missing"); err != nil || peak != 0 || got != (loopTokenUsage{Provisional: true}) {
		t.Fatalf("missing log: %+v peak %d err %v", got, peak, err)
	}
	if err := os.MkdirAll(filepath.Join(home, "jobs", "bad", "log"), 0700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := loopJobTokenUsage("bad"); err == nil {
		t.Fatal("directory log must fail")
	}
}

func TestLoopUsageSavedPrefixes(t *testing.T) {
	data, err := os.ReadFile("testdata/loop-usage-i136.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	checkpoints := 0
	for i, line := range lines {
		var event struct {
			Type   string `json:"type"`
			Models map[string]struct {
				Output int `json:"outputTokens"`
			} `json:"modelUsage"`
		}
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatal(err)
		}
		if event.Type != "result" {
			continue
		}
		checkpoints++
		before, _, _ := parseLoopTokenUsage([]byte(strings.Join(lines[:i], "\n")))
		if !before.Provisional {
			t.Fatalf("checkpoint %d preceding work should be provisional", checkpoints)
		}
		got, _, _ := parseLoopTokenUsage([]byte(strings.Join(lines[:i+1], "\n")))
		want := 0
		for _, model := range event.Models {
			want += model.Output
		}
		if got.Provisional || got.Output != want {
			t.Fatalf("checkpoint %d: %+v want output %d", checkpoints, got, want)
		}
	}
	if checkpoints != 4 {
		t.Fatalf("got %d checkpoints", checkpoints)
	}
}

func TestLoopUsageTerminalRefreshAndContextLimit(t *testing.T) {
	for _, peak := range []int{12, 120} {
		t.Run(fmt.Sprint(peak), func(t *testing.T) {
			root := t.TempDir()
			home := t.TempDir()
			t.Setenv("DISPATCH_HOME", home)
			log := filepath.Join(home, "jobs", "job", "log")
			if err := os.MkdirAll(filepath.Dir(log), 0700); err != nil {
				t.Fatal(err)
			}
			assistant := fmt.Sprintf(`{"type":"assistant","message":{"id":"a","usage":{"input_tokens":%d,"output_tokens":1}}}`, peak)
			if err := os.WriteFile(log, []byte(assistant+"\n"), 0600); err != nil {
				t.Fatal(err)
			}
			result := fmt.Sprintf(`{"type":"result","modelUsage":{"opus":{"inputTokens":%d,"cacheCreationInputTokens":0,"cacheReadInputTokens":0,"outputTokens":5000}}}`, peak)
			fake := filepath.Join(root, "dispatch")
			script := "#!/bin/sh\ncase \"$1\" in\nstatus) printf '%s\\n' " + shellQuote(result) + " >> " + shellQuote(log) + "; echo 'job done brain' ;;\ncancel) echo cancelled > " + shellQuote(filepath.Join(root, "cancelled")) + " ;;\nesac\n"
			if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BL_DISPATCH", fake)
			assoc := loopAssociation{Item: "A1", Job: "job", Sessions: 1}
			err := waitLoopJob(root, &assoc, filepath.Join(root, "association.json"), time.Now(), 2, 1, 100, 1, "")
			if peak == 12 {
				if err != nil || assoc.Tokens != 5012 {
					t.Fatalf("terminal refresh: tokens %d err %v", assoc.Tokens, err)
				}
				if _, err := os.Stat(filepath.Join(root, "cancelled")); !os.IsNotExist(err) {
					t.Fatal("cumulative usage cancelled modest request context")
				}
			} else if _, err := os.Stat(filepath.Join(root, "cancelled")); err != nil {
				t.Fatal("over-limit request did not cancel", err)
			}
		})
	}
}
