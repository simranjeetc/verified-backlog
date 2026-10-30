package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func cancelFixture(t *testing.T) (string, string) {
	t.Helper()
	root := gitItemWith(t, map[string]string{"a.go": "one\n", "other": "human\n", ".gitignore": ".backlog/\nignored\n"})
	dir := t.TempDir()
	fake := filepath.Join(dir, "dispatch")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$CANCEL_LOG"
case "$1" in
 status)
  if [ -n "$2" ]; then
   state=running
   [ -f "$CANCEL_STATE/$2" ] && state=cancelled
   [ "$2" = stubborn ] && state=running
   printf '%s %s brain fake 0s /tmp/out\n' "$2" "$state"
  fi ;;
 cancel) [ "$2" = stubborn ] || touch "$CANCEL_STATE/$2" ;;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_DISPATCH", fake)
	t.Setenv("CANCEL_STATE", dir)
	t.Setenv("CANCEL_LOG", filepath.Join(dir, "log"))
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Launches: 1}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	return root, dir
}

func cancelArgs() args {
	return args{flags: map[string]string{"reason": "stop", "timeout": "1", "poll-ms": "1"}, boolFlags: map[string]bool{"cancel": true}}
}

func TestLoopCancelPreservesPatchRestoresAndParks(t *testing.T) {
	root, _ := cancelFixture(t)
	writeFile(t, root, "a.go", "two\n")
	writeFile(t, root, "new file", "new\x00data")
	if err := cmdLoop(root, cancelArgs()); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, filepath.Join(root, "a.go")); got != "one\n" {
		t.Fatal(got)
	}
	if _, err := os.Stat(filepath.Join(root, "new file")); !os.IsNotExist(err) {
		t.Fatal("new file not restored")
	}
	patches, _ := filepath.Glob(filepath.Join(root, backlogDir, "cancelled", "*.patch"))
	if len(patches) != 1 {
		t.Fatal(patches)
	}
	cmd := exec.Command("git", "apply", "--binary", patches[0])
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("saved patch unusable: %v %s", err, out)
	}
	if got := readFile(t, filepath.Join(root, "new file")); got != "new\x00data" {
		t.Fatal("binary work lost")
	}
	events, _ := readLedger(root)
	_, items, _ := derive(events)
	if items["a1"].Status != "parked" || items["a1"].Attempt != 1 {
		t.Fatal(items["a1"])
	}
	if _, err := os.Stat(filepath.Join(root, backlogDir, "loop.json")); !os.IsNotExist(err) {
		t.Fatal("association retained")
	}
}

func TestLoopCancelNeverRollsBackCommitsOrStagedChanges(t *testing.T) {
	for _, kind := range []string{"commit", "stage"} {
		t.Run(kind, func(t *testing.T) {
			root, _ := cancelFixture(t)
			writeFile(t, root, "a.go", "saved\n")
			if kind == "commit" {
				commitSeed(t, root)
			} else {
				if _, code := git(root, "add", "a.go"); code != 0 {
					t.Fatal("git add")
				}
			}
			if err := cmdLoop(root, cancelArgs()); err != nil {
				t.Fatal(err)
			}
			if got := readFile(t, filepath.Join(root, "a.go")); got != "saved\n" {
				t.Fatal("overwrote work")
			}
			events, _ := readLedger(root)
			_, items, _ := derive(events)
			if items["a1-left-1"] == nil {
				t.Fatal("missing retained-files followup")
			}
		})
	}
}

func TestLoopCancelScopedRestoreLeavesOtherEdits(t *testing.T) {
	root, _ := cancelFixture(t)
	// Set the scope through normal defer/reopen/open before claims exist.
	if err := call(t, cmdDefer, root, "A1", "--reason", "scope"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdReopen, root, "A1"); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdOpen, root, "A1", "--scope", "a.go"); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "a.go", "two\n")
	writeFile(t, root, "other", "unrelated\n")
	if err := cmdLoop(root, cancelArgs()); err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "a.go")) != "one\n" || readFile(t, filepath.Join(root, "other")) != "unrelated\n" {
		t.Fatal("wrong scope restored")
	}
}

func TestLoopCancelStubbornJobRetainsFilesAndFilesFollowup(t *testing.T) {
	root, _ := cancelFixture(t)
	writeFile(t, root, "a.go", "two\n")
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "stubborn", Launches: 1}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	out, err := captureLoopOutput(func() error { return cmdLoop(root, cancelArgs()) })
	if err != nil {
		t.Fatal(err)
	}
	if readFile(t, filepath.Join(root, "a.go")) != "two\n" || !strings.Contains(out, "stubborn") || !strings.Contains(out, "filed A1-left-1") {
		t.Fatal(out)
	}
}

func TestLoopCancelArchivesIgnoredWorktreeFiles(t *testing.T) {
	root, _ := cancelFixture(t)
	session := t.TempDir()
	wt := filepath.Join(session, "producer")
	if out, code := git(root, "worktree", "add", "--detach", wt, "HEAD"); code != 0 {
		t.Fatal(out)
	}
	// Unrelated worktree must survive even if clean.
	other := filepath.Join(t.TempDir(), "other")
	if out, code := git(root, "worktree", "add", "--detach", other, "HEAD"); code != 0 {
		t.Fatal(out)
	}
	writeFile(t, wt, "a.go", "producer\n")
	commitSeed(t, wt)
	head, _ := git(wt, "rev-parse", "HEAD")
	head = strings.TrimSpace(head)
	writeFile(t, wt, "ignored", "valuable\n")
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Launches: 1, Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	if err := cmdLoop(root, cancelArgs()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(wt); !os.IsNotExist(err) {
		t.Fatal("owned worktree remains")
	}
	if _, err := os.Stat(other); err != nil {
		t.Fatal("unrelated worktree removed")
	}
	archives, _ := filepath.Glob(filepath.Join(root, backlogDir, "cancelled", "*.tar.gz"))
	if len(archives) != 1 {
		t.Fatal(archives)
	}
	f, err := os.Open(archives[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	found := map[string]string{}
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		found[h.Name] = string(data)
	}
	if _, code := git(root, "show-ref", "--verify", "refs/bl-cancel/A1/"+head); code != 0 {
		t.Fatal("detached commit history lost")
	}
	if found["ignored"] != "valuable\n" || found["a.go"] != "producer\n" {
		t.Fatal(found)
	}
}

func TestLoopCancellationRequestReleasesLiveOwner(t *testing.T) {
	root, _ := loopFixture(t)
	path := filepath.Join(root, backlogDir, "loop.lock")
	owner, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer owner.Close()
	if err := syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	requester, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer requester.Close()
	done := make(chan error, 1)
	go func() { done <- requestLoopStop(root, requester) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(root, backlogDir, "loop.cancel")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no cancellation request")
		}
		time.Sleep(time.Millisecond)
	}
	syscall.Flock(int(owner.Fd()), syscall.LOCK_UN)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	syscall.Flock(int(requester.Fd()), syscall.LOCK_UN)
}

func writeWorkerReceipt(t *testing.T, session, token, job, cwd string) {
	t.Helper()
	dir := filepath.Join(session, "workers")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]string{"job": job, "cwd": cwd, "token": token})
	if err := os.WriteFile(filepath.Join(dir, job+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestLoopReapsRegisteredWorkerAfterBrainFailure(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	writeWorkerReceipt(t, session, "token", "child", root)
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	// Pretend brain has ended; no real models or external dispatch service.
	if err := os.WriteFile(filepath.Join(state, "brain"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	err := waitLoopJob(root, &assoc, filepath.Join(root, backlogDir, "loop.json"), time.Now(), 1, 1, 130000, 5, "")
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "child")); err != nil {
		t.Fatal("detached worker not cancelled")
	}
}

func TestLoopReapsBrainAndWorkerOnWaitTimeout(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	writeWorkerReceipt(t, session, "token", "child", root)
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	err := waitLoopJob(root, &assoc, filepath.Join(root, backlogDir, "loop.json"), time.Now().Add(-2*time.Second), 1, 1, 130000, 5, "")
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatal(err)
	}
	for _, job := range []string{"brain", "child"} {
		if _, err := os.Stat(filepath.Join(state, job)); err != nil {
			t.Fatalf("%s not cancelled", job)
		}
	}
}

func TestLoopWorkerWrapperRegistersWithoutChangingBacklog(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(strconv.FormatBool(stale), func(t *testing.T) {
			root, state := cancelFixture(t)
			session := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "scripts"), 0755); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"dispatch.sh", "register-loop-worker.py"} {
				data, err := os.ReadFile(filepath.Join("..", "..", name))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "scripts", name), data, 0700); err != nil {
					t.Fatal(err)
				}
			}
			fake := filepath.Join(state, "wrapper-dispatch")
			if err := os.WriteFile(fake, []byte("#!/bin/sh\ncase \"$1\" in start) echo child ;; wait) echo 'child done produce fake 0s /tmp/out' ;; cancel) touch \"$CANCEL_STATE/child-cancelled\" ;; esac\n"), 0700); err != nil {
				t.Fatal(err)
			}
			assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Dirs: []string{session}}
			if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
				t.Fatal(err)
			}
			t.Setenv("BL_LOOP_ROOT", root)
			t.Setenv("BL_LOOP_TOKEN", "token")
			t.Setenv("BL_LOOP_SESSION", session)
			t.Setenv("DISPATCH_BIN", fake)
			if stale {
				t.Setenv("BL_LOOP_TOKEN", "wrong")
			}
			before, err := backlogDigest(root)
			if err != nil {
				t.Fatal(err)
			}
			prompt := writeFile(t, state, "prompt", "implement")
			cmd := exec.Command("/bin/sh", filepath.Join(root, "scripts", "dispatch.sh"), "produce", prompt, filepath.Join(state, "report"))
			cmd.Dir = root
			out, err := cmd.CombinedOutput()
			if stale {
				if err == nil {
					t.Fatal("stale registration accepted")
				}
				if _, err := os.Stat(filepath.Join(state, "child-cancelled")); !os.IsNotExist(err) {
					t.Fatalf("preflight should refuse before launch: %s", out)
				}
			} else {
				if err != nil {
					t.Fatalf("wrapper: %v %s", err, out)
				}
				jobs, err := registeredLoopJobs(&assoc)
				if err != nil || resolvedPath(jobs["child"]) != resolvedPath(root) {
					t.Fatalf("receipt: %v %v", jobs, err)
				}
			}
			after, err := backlogDigest(root)
			if err != nil || before != after {
				t.Fatal("worker registration changed verifier evidence window")
			}
		})
	}
}

func TestLoopTaggedDetachedProcessActuallyStops(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	child := exec.Command("sleep", "60")
	child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer syscall.Kill(-child.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- child.Wait() }()
	fake := filepath.Join(state, "process-dispatch")
	script := "#!/bin/sh\ncase \"$1\" in status) state=running; [ -f \"$CANCEL_STATE/stopped\" ] && state=cancelled; echo \"$2 $state produce fake 0s /tmp/out\" ;; cancel) python3 -c 'import os,signal;os.killpg(" + strconv.Itoa(child.Process.Pid) + ",signal.SIGKILL)'; touch \"$CANCEL_STATE/stopped\" ;; esac\n"
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_DISPATCH", fake)
	home := t.TempDir()
	t.Setenv("DISPATCH_HOME", home)
	jobDir := filepath.Join(home, "jobs", "detached")
	if err := os.MkdirAll(jobDir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, jobDir, "meta", "title=bl-worker-token-worker\ncwd="+root+"\n")
	// No receipt exists: model the wrapper dying immediately after dispatch start.

	assoc := loopAssociation{Item: "A1", Token: "token", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	if err := reapLoopWorkers(root, &assoc); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("registered detached process survived cleanup")
	}
}

func TestLoopCancelMissingAssociationDoesNotChangeLedger(t *testing.T) {
	root, _ := loopFixture(t)
	before, _ := readLedger(root)
	err := cmdLoop(root, cancelArgs())
	var le loopExitError
	if !errors.As(err, &le) || le.code != 2 {
		t.Fatal(err)
	}
	after, _ := readLedger(root)
	if len(before) != len(after) {
		t.Fatal("ledger changed")
	}
}

func TestLoopReapMalformedReceiptStillCancelsKnownJobs(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	writeWorkerReceipt(t, session, "token", "child", root)
	writeFile(t, filepath.Join(session, "workers"), "bad.json", "broken")
	assoc := loopAssociation{Token: "token", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	if err := reapLoopWorkers(root, &assoc); err == nil {
		t.Fatal("malformed receipt ignored")
	}
	if _, err := os.Stat(filepath.Join(state, "child")); err != nil {
		t.Fatal("valid job left running")
	}
}

func TestLoopCancelRequestStopsBrainBeforeReaping(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	writeWorkerReceipt(t, session, "token", "child", root)
	assoc := loopAssociation{Item: "A1", Token: "token", Job: "brain", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, backlogDir), "loop.cancel", "requested")
	err := waitLoopJob(root, &assoc, filepath.Join(root, backlogDir, "loop.json"), time.Now(), 1, 1, 130000, 5, "")
	if err == nil || !strings.Contains(err.Error(), "cancellation requested") {
		t.Fatal(err)
	}
	log := readFile(t, filepath.Join(state, "log"))
	brain := strings.Index(log, "cancel brain")
	child := strings.Index(log, "cancel child")
	if brain < 0 || child < brain {
		t.Fatal("worker snapshot taken before brain shutdown: " + log)
	}
}

func TestLoopDrainFindsChildLaunchedWhileWorkerStops(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	home := t.TempDir()
	t.Setenv("DISPATCH_HOME", home)
	writeWorkerReceipt(t, session, "token", "parent", root)
	lateDir := filepath.Join(home, "jobs", "late")
	if err := os.MkdirAll(lateDir, 0700); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(state, "spawning-dispatch")
	script := `#!/bin/sh
case "$1" in
 status) state=running; [ -f "$CANCEL_STATE/$2" ] && state=cancelled; echo "$2 $state produce fake 0s /tmp/out" ;;
 cancel)
  if [ "$2" = parent ]; then
   printf 'title=bl-worker-token-late\ncwd=%s\n' "$BL_TEST_ROOT" > "$DISPATCH_HOME/jobs/late/meta"
  fi
  touch "$CANCEL_STATE/$2" ;;
esac
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_DISPATCH", fake)
	t.Setenv("BL_TEST_ROOT", root)
	assoc := loopAssociation{Item: "A1", Token: "token", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	if err := reapLoopWorkers(root, &assoc); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "late")); err != nil {
		t.Fatal("late child survived")
	}
	if _, err := os.Stat(filepath.Join(session, "workers.stopping")); err != nil {
		t.Fatal("session not fenced")
	}
}

func TestLoopDrainWaitsForPendingLaunchBeforeDiscovery(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	home := t.TempDir()
	t.Setenv("DISPATCH_HOME", home)
	lock, err := os.OpenFile(filepath.Join(session, "workers.launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}
	assoc := loopAssociation{Token: "token", Dirs: []string{session}}
	done := make(chan []string, 1)
	go func() { done <- drainLoopWorkers(root, &assoc, false, 2*time.Second) }()
	deadline := time.Now().Add(time.Second)
	for {
		if _, err := os.Stat(filepath.Join(session, "workers.stopping")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no fence")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case result := <-done:
		t.Fatalf("cleanup returned with pending launch: %v", result)
	case <-time.After(50 * time.Millisecond):
	}
	dir := filepath.Join(home, "jobs", "late")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, dir, "meta", "title=bl-worker-token-late\ncwd="+root+"\n")
	syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	if result := <-done; len(result) > 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(state, "late")); err != nil {
		t.Fatal("pending worker survived")
	}
}

func TestLoopKilledHelperCannotReleasePendingDispatchLaunch(t *testing.T) {
	root, state := cancelFixture(t)
	session := t.TempDir()
	home := t.TempDir()
	t.Setenv("DISPATCH_HOME", home)
	t.Setenv("BL_LOOP_ROOT", root)
	t.Setenv("BL_LOOP_TOKEN", "token")
	t.Setenv("BL_LOOP_SESSION", session)
	assoc := loopAssociation{Item: "A1", Token: "token", Dirs: []string{session}}
	if err := writeAssociation(filepath.Join(root, backlogDir, "loop.json"), &assoc); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(state, "pending-dispatch")
	script := `#!/usr/bin/env python3
import os,sys,time
from pathlib import Path
base=Path(os.environ["CANCEL_STATE"])
if sys.argv[1]=="start":
 (base/"launch-pid").write_text(str(os.getpid()))
 while not (base/"launch-gate").exists(): time.sleep(.01)
 job=Path(os.environ["DISPATCH_HOME"])/"jobs"/"late"
 job.mkdir(parents=True)
 (job/"meta").write_text("title=bl-worker-token-late\ncwd="+os.environ["BL_LOOP_ROOT"]+"\n")
 try: print("late",flush=True)
 except BrokenPipeError: pass
 (base/"launch-ended").touch()
elif sys.argv[1]=="status":
 state="cancelled" if (base/"late").exists() else "running"
 print(sys.argv[2],state,"produce fake 0s /tmp/out")
elif sys.argv[1]=="cancel": (base/"late").touch()
`
	if err := os.WriteFile(fake, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BL_DISPATCH", fake)
	helper, err := filepath.Abs(filepath.Join("..", "..", "register-loop-worker.py"))
	if err != nil {
		t.Fatal(err)
	}
	launcher := exec.Command("python3", helper, fake, "produce", "prompt", "output", "60", "bl-worker-token-late", "")
	launcher.Dir = root
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer null.Close()
	launcher.Stdout = null
	launcher.Stderr = null
	if err := launcher.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { launcher.Process.Kill(); os.WriteFile(filepath.Join(state, "launch-gate"), nil, 0600) }()
	deadline := time.Now().Add(2 * time.Second)
	var pid int
	for {
		data, err := os.ReadFile(filepath.Join(state, "launch-pid"))
		if err == nil {
			pid, _ = strconv.Atoi(string(data))
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("dispatch start did not begin")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		if _, err := os.Stat(filepath.Join(state, "launch-ended")); os.IsNotExist(err) {
			syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	if err := launcher.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	launcher.Wait()
	done := make(chan []string, 1)
	go func() { done <- drainLoopWorkers(root, &assoc, false, 3*time.Second) }()
	select {
	case result := <-done:
		t.Fatalf("killed helper released pending-launch barrier: %v", result)
	case <-time.After(80 * time.Millisecond):
	}
	writeFile(t, state, "launch-gate", "go")
	if result := <-done; len(result) > 0 {
		t.Fatal(result)
	}
	if _, err := os.Stat(filepath.Join(state, "late")); err != nil {
		t.Fatal("late dispatch job survived")
	}
}
