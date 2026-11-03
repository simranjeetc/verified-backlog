package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestLoopBrainHelper(t *testing.T) {
	if os.Getenv("BL_LOOP_HELPER") != "1" {
		return
	}
	root := os.Getenv("BL_LOOP_ROOT")
	if err := cmdDefer(root, args{flags: map[string]string{"reason": "human decision needed"}, boolFlags: map[string]bool{}, lists: map[string][]string{}, positional: []string{os.Getenv("BL_LOOP_ITEM")}}); err != nil {
		t.Fatal(err)
	}
}

func TestLoopBudgetAndFreshJobs(t *testing.T) {
	root, fake := loopFixture(t)
	for _, id := range []string{"A1", "A2", "A3"} {
		if err := cmdFile(root, args{flags: map[string]string{"id": id, "text": "work"}, boolFlags: map[string]bool{}, lists: map[string][]string{}}); err != nil {
			t.Fatal(err)
		}
	}
	os.Setenv("BL_DISPATCH", fake)
	t.Setenv("BL_LOOP_ROOT", root)
	t.Setenv("BL_LOOP_ITEM", "A1")
	setLoopHelper(t)
	err := testLoop(root, loopArgs("max-items", "2", "poll-ms", "1", "timeout", "1"))
	if err != nil {
		t.Fatalf("loop: %v", err)
	}
	if got := strings.TrimSpace(readFile(t, filepath.Join(root, ".backlog", "ledger.jsonl"))); !strings.Contains(got, `"id":"A2"`) {
		t.Fatalf("second item not processed: %s", got)
	}
	if _, err := os.Stat(filepath.Join(root, ".backlog", "loop.json")); !os.IsNotExist(err) {
		t.Fatalf("association remains after successful completion: %v", err)
	}
	log := readFile(t, filepath.Join(root, "dispatch.log"))
	if strings.Count(log, "start brain") != 2 || !strings.Contains(log, "playbooks/item-loop/index.md") || !strings.Contains(log, "never push") {
		t.Fatalf("bad launch record/prompt: %s", log)
	}
	if !strings.Contains(log, "bl-loop-A1-") || !strings.Contains(log, "--config") {
		t.Fatalf("bad dispatch argv: %s", log)
	}
}

func TestLoopEmptyAndResumeFailures(t *testing.T) {
	root, fake := loopFixture(t)
	t.Setenv("BL_DISPATCH", fake)
	if err := testLoop(root, argsWith(map[string]string{"poll-ms": "1"})); err != nil {
		t.Fatalf("empty backlog: %v", err)
	}
	if err := cmdFile(root, argsWith(map[string]string{"id": "B1", "text": "work"})); err != nil {
		t.Fatal(err)
	}
	assocPath := filepath.Join(root, ".backlog", "loop.json")
	if err := os.WriteFile(assocPath, []byte(`{"item":"B1","token":"t","launches":1}`), 0o600); err != nil {
		t.Fatal(err)
	}
	err := testLoop(root, argsWith(map[string]string{"poll-ms": "1"}))
	var le loopExitError
	if !errors.As(err, &le) || le.code != 5 || !strings.Contains(err.Error(), "attended reconciliation") {
		t.Fatalf("no-job association: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dispatch.log")); err == nil && strings.Contains(string(data), "start") {
		t.Fatal("no-job recovery launched dispatch")
	}
}

func TestLoopJobFailureKeepsAssociation(t *testing.T) {
	root, fake := loopFixture(t)
	t.Setenv("BL_DISPATCH", fake)
	t.Setenv("BL_JOB_STATE", "failed")
	if err := cmdFile(root, argsWith(map[string]string{"id": "C1", "text": "work"})); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_LOOP_ROOT", root)
	t.Setenv("BL_LOOP_ITEM", "C1")
	setLoopHelper(t)
	err := testLoop(root, argsWith(map[string]string{"poll-ms": "1", "timeout": "1"}))
	var le loopExitError
	if !errors.As(err, &le) || le.code != 5 || !strings.Contains(err.Error(), "failed") {
		t.Fatalf("want job exit 5: %v", err)
	}
	var assoc loopAssociation
	data := readFile(t, filepath.Join(root, ".backlog", "loop.json"))
	if json.Unmarshal([]byte(data), &assoc) != nil || assoc.Job != "job1" || assoc.Item != "C1" {
		t.Fatalf("association not retained: %s", data)
	}
}

func TestLoopResumeAttachesWithoutLaunching(t *testing.T) {
	root, fake := loopFixture(t)
	t.Setenv("BL_DISPATCH", fake)
	if err := cmdFile(root, argsWith(map[string]string{"id": "E1", "text": "work"})); err != nil {
		t.Fatal(err)
	}
	if err := cmdDefer(root, args{flags: map[string]string{"reason": "parked"}, boolFlags: map[string]bool{}, lists: map[string][]string{}, positional: []string{"E1"}}); err != nil {
		t.Fatal(err)
	}
	assoc := loopAssociation{Item: "E1", Token: "saved", Launches: 1, Job: "attached"}
	if err := writeAssociation(filepath.Join(root, ".backlog", "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	if err := testLoop(root, argsWith(map[string]string{"poll-ms": "1"})); err != nil {
		t.Fatalf("resume: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "dispatch.log")); err == nil && strings.Contains(string(data), "start") {
		t.Fatalf("resume launched a new job: %s", data)
	}
	if _, err := os.Stat(filepath.Join(root, ".backlog", "loop.json")); !os.IsNotExist(err) {
		t.Fatalf("completed association was not removed: %v", err)
	}
}

func TestLoopNoWorkableAndCheckFailureCodes(t *testing.T) {
	root, fake := loopFixture(t)
	t.Setenv("BL_DISPATCH", fake)
	if err := cmdFile(root, argsWith(map[string]string{"id": "F1", "text": "dependency"})); err != nil {
		t.Fatal(err)
	}
	if err := cmdFile(root, args{flags: map[string]string{"id": "F2", "text": "blocked", "after": "F1"}, boolFlags: map[string]bool{}, lists: map[string][]string{}}); err != nil {
		t.Fatal(err)
	}
	if err := cmdDefer(root, args{flags: map[string]string{"reason": "parked"}, boolFlags: map[string]bool{}, lists: map[string][]string{}, positional: []string{"F1"}}); err != nil {
		t.Fatal(err)
	}
	err := testLoop(root, argsWith(nil))
	var le loopExitError
	if !errors.As(err, &le) || le.code != 3 || !strings.Contains(err.Error(), "F2") {
		t.Fatalf("unworkable open item should exit 3: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".backlog", "ledger.jsonl"), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = testLoop(root, argsWith(nil))
	if !errors.As(err, &le) || le.code != 4 {
		t.Fatalf("check failure should exit 4: %v", err)
	}
}

func TestLoopRejectsInvalidBudget(t *testing.T) {
	root, _ := loopFixture(t)
	if err := testLoop(root, argsWith(map[string]string{"max-items": "0"})); err == nil {
		t.Fatal("accepted zero launch budget")
	}
}

func TestLoopLockAndUnfinishedItem(t *testing.T) {
	root, fake := loopFixture(t)
	t.Setenv("BL_DISPATCH", fake)
	lock, err := os.OpenFile(filepath.Join(root, ".backlog", "loop.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	err = testLoop(root, argsWith(nil))
	var refusal Refusal
	if !errors.As(err, &refusal) || !strings.Contains(err.Error(), "loop.lock") {
		t.Fatalf("lock contention not refused: %v", err)
	}
	_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	lock.Close()
	if err := cmdFile(root, argsWith(map[string]string{"id": "D1", "text": "work"})); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_LOOP_ACTION", "noop")
	err = testLoop(root, argsWith(map[string]string{"poll-ms": "1", "timeout": "1"}))
	var le loopExitError
	if !errors.As(err, &le) || le.code != 5 || !strings.Contains(err.Error(), "remains open") {
		t.Fatalf("unfinished item should stop: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".backlog", "loop.json")); err != nil {
		t.Fatalf("unfinished association was not retained: %v", err)
	}
}

func loopFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	if err := cmdInit(root, argsWith(nil)); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(root, "fake-dispatch")
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncase \"$1\" in\nstart) shift; printf 'start' >> \"$BL_LOOP_ROOT/dispatch.log\"; title=; for x in \"$@\"; do printf ' %s' \"$x\" >> \"$BL_LOOP_ROOT/dispatch.log\"; if [ \"$prev\" = --title ]; then title=$x; fi; prev=$x; done; printf '\\n' >> \"$BL_LOOP_ROOT/dispatch.log\"; cat \"$2\" >> \"$BL_LOOP_ROOT/dispatch.log\"; item=${title#bl-loop-}; item=${item%-*}; if [ \"$BL_LOOP_ACTION\" != noop ]; then BL_LOOP_HELPER=1 BL_LOOP_ITEM=\"$item\" BL_LOOP_ROOT=\"$BL_LOOP_ROOT\" " + shellQuote(bin) + " -test.run=TestLoopBrainHelper >/dev/null; fi; echo job1 ;;\nstatus) if [ -n \"$2\" ]; then echo \"$2 ${BL_JOB_STATE:-done} brain - 0s /tmp/out\"; fi ;;\nesac\n"
	if err := os.WriteFile(fake, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_LOOP_ROOT", root)
	t.Setenv("BL_LOOP_ITEM", "A1")
	t.Setenv("BL_JOB_STATE", "done")
	return root, fake
}

func setLoopHelper(t *testing.T) {
	t.Helper()
	bin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_HELPER_BIN", bin)
}

func argsWith(flags map[string]string) args {
	if flags == nil {
		flags = map[string]string{}
	}
	return args{flags: flags, boolFlags: map[string]bool{}, lists: map[string][]string{}}
}

func loopArgs(kv ...string) args {
	f := map[string]string{}
	for i := 0; i+1 < len(kv); i += 2 {
		f[kv[i]] = kv[i+1]
	}
	return argsWith(f)
}

func testLoop(root string, a args) error {
	err := cmdLoop(root, a)
	releaseLoopLock()
	return err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func TestLoopContextUpperBound(t *testing.T) {
	root, _ := loopFixture(t)
	for _, value := range []string{"0", "130001", "300000"} {
		if err := testLoop(root, argsWith(map[string]string{"max-context": value})); err == nil || !strings.Contains(err.Error(), "between 1 and 130000") {
			t.Fatalf("limit %s: %v", value, err)
		}
	}
	for _, value := range []string{"120000", "130000"} {
		if err := testLoop(root, argsWith(map[string]string{"max-context": value})); err != nil {
			t.Fatalf("valid limit %s: %v", value, err)
		}
	}
}
