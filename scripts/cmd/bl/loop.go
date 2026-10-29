package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type loopAssociation struct {
	Item     string `json:"item"`
	Token    string `json:"token"`
	Launches int    `json:"launches"`
	Job      string `json:"job,omitempty"`
	Tokens   int    `json:"tokens,omitempty"`
}

type loopExitError struct {
	code int
	msg  string
}

var loopLockFile *os.File

func releaseLoopLock() {
	if loopLockFile == nil {
		return
	}
	_ = syscall.Flock(int(loopLockFile.Fd()), syscall.LOCK_UN)
	_ = loopLockFile.Close()
	loopLockFile = nil
}

func (e loopExitError) Error() string { return e.msg }

func loopExit(code int, format string, values ...any) error {
	return loopExitError{code: code, msg: fmt.Sprintf(format, values...)}
}

func cmdLoop(root string, a args) error {
	maxItems, pollMS, timeoutSec, maxTokens := 3, 5000, 14400, 300000
	var err error
	if a.get("max-items") != "" {
		maxItems, err = strconv.Atoi(a.get("max-items"))
		if err != nil || maxItems < 1 {
			return refuse("--max-items must be at least 1")
		}
	}
	if a.get("poll-ms") != "" {
		pollMS, err = strconv.Atoi(a.get("poll-ms"))
		if err != nil || pollMS < 1 {
			return refuse("--poll-ms must be at least 1")
		}
	}
	if a.get("timeout") != "" {
		timeoutSec, err = strconv.Atoi(a.get("timeout"))
		if err != nil || timeoutSec < 1 {
			return refuse("--timeout must be at least 1")
		}
	}
	if a.get("max-tokens") != "" {
		maxTokens, err = strconv.Atoi(a.get("max-tokens"))
		if err != nil || maxTokens < 1 {
			return refuse("--max-tokens must be at least 1")
		}
	}
	p := filepath.Join(root, backlogDir)
	if err := os.MkdirAll(p, 0o755); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(p, "loop.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = lock.Close()
		return refuse("could not acquire %s: %v", filepath.Join(p, "loop.lock"), err)
	}
	loopLockFile = lock
	loopStarted := time.Now()
	assocPath := filepath.Join(p, "loop.json")
	var assoc *loopAssociation
	if data, readErr := os.ReadFile(assocPath); readErr == nil {
		var saved loopAssociation
		if json.Unmarshal(data, &saved) != nil || saved.Item == "" || saved.Token == "" {
			return loopExit(5, "invalid %s; attended reconciliation required", assocPath)
		}
		if saved.Job == "" {
			return loopExit(5, "association for %s has no job id; attended reconciliation required", saved.Item)
		}
		assoc = &saved
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return readErr
	}
	if assoc == nil {
		state, err := loopState(root)
		if err != nil {
			return loopExit(4, "bl check failed before launch: %v", err)
		}
		if state.inFlight != "" {
			return loopExit(5, "item %s is already in flight without a loop association", state.inFlight)
		}
	}
	launches := 0
	if assoc != nil {
		launches = assoc.Launches
		if err := waitLoopJob(root, assoc, assocPath, loopStarted, timeoutSec, pollMS, maxTokens); err != nil {
			return err
		}
		if err := completeLoopItem(root, assoc); err != nil {
			return err
		}
		if err := os.Remove(assocPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for launches < maxItems {
		state, err := loopState(root)
		if err != nil {
			return loopExit(4, "bl check failed: %v", err)
		}
		id, err := loopNextID(root)
		if err != nil {
			return err
		}
		if id == "none" {
			if state.openOrBlocked == 0 {
				fmt.Printf("backlog complete · %d parked\n", state.parked)
				return nil
			}
			return loopExit(3, "no workable item; remaining: %s", strings.Join(state.remaining, ", "))
		}
		tokenBytes := make([]byte, 12)
		if _, err := rand.Read(tokenBytes); err != nil {
			return err
		}
		assoc = &loopAssociation{Item: id, Token: hex.EncodeToString(tokenBytes), Launches: launches + 1}
		if err := writeAssociation(assocPath, assoc); err != nil {
			return err
		}
		launches++
		job, err := startLoopJob(root, id, assoc.Token, a.get("dispatch-config"))
		if err != nil {
			return loopExit(5, "dispatch start for %s failed: %v", id, err)
		}
		assoc.Job = job
		if err := writeAssociation(assocPath, assoc); err != nil {
			return err
		}
		if err := waitLoopJob(root, assoc, assocPath, loopStarted, timeoutSec, pollMS, maxTokens); err != nil {
			return err
		}
		if err := completeLoopItem(root, assoc); err != nil {
			return err
		}
		if err := os.Remove(assocPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		assoc = nil
	}
	state, err := loopState(root)
	if err != nil {
		return loopExit(4, "bl check failed: %v", err)
	}
	if len(state.remaining) == 0 {
		fmt.Printf("backlog complete · %d parked\n", state.parked)
	} else {
		fmt.Printf("launch budget exhausted; remaining open items: %s\n", strings.Join(state.remaining, ", "))
	}
	return nil
}

type loopLedgerState struct {
	inFlight      string
	openOrBlocked int
	parked        int
	remaining     []string
}

func loopState(root string) (loopLedgerState, error) {
	var s loopLedgerState
	if err := cmdCheck(root, args{flags: map[string]string{}, boolFlags: map[string]bool{}, lists: map[string][]string{}}); err != nil {
		return s, err
	}
	statusText, err := captureLoopOutput(func() error {
		return cmdStatus(root, args{flags: map[string]string{}, boolFlags: map[string]bool{}, lists: map[string][]string{}})
	})
	if err != nil {
		return s, err
	}
	for _, line := range strings.Split(statusText, "\n") {
		if strings.HasPrefix(line, "in flight: ") {
			s.inFlight = strings.TrimPrefix(line, "in flight: ")
			if s.inFlight == "none" {
				s.inFlight = ""
			}
		}
	}
	events, err := readLedger(root)
	if err != nil {
		return s, err
	}
	order, _, err := derive(events)
	if err != nil {
		return s, err
	}
	cur, err := pinned(order)
	if err != nil {
		return s, err
	}
	if cur != nil {
		s.inFlight = cur.ID
	}
	for _, it := range order {
		if it.Status == "parked" {
			s.parked++
		}
		if it.Status == "open" || it.BlockedBy != "" {
			s.openOrBlocked++
			s.remaining = append(s.remaining, it.ID)
		}
	}
	return s, nil
}

func loopNextID(root string) (string, error) {
	text, err := captureLoopOutput(func() error {
		return cmdNext(root, args{flags: map[string]string{}, boolFlags: map[string]bool{}, lists: map[string][]string{}})
	})
	if err != nil {
		return "", err
	}
	line := strings.TrimSpace(text)
	if line == "none" {
		return "none", nil
	}
	id, _, _ := strings.Cut(line, "\t")
	return id, nil
}

func captureLoopOutput(fn func() error) (string, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return "", err
	}
	type readResult struct {
		data []byte
		err  error
	}
	readDone := make(chan readResult, 1)
	go func() {
		data, readErr := io.ReadAll(r)
		readDone <- readResult{data: data, err: readErr}
	}()
	old := os.Stdout
	os.Stdout = w
	callErr := fn()
	os.Stdout = old
	if err := w.Close(); err != nil {
		r.Close()
		return "", err
	}
	read := <-readDone
	r.Close()
	if read.err != nil {
		return "", read.err
	}
	return string(read.data), callErr
}

func writeAssociation(path string, assoc *loopAssociation) error {
	data, err := json.Marshal(assoc)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.CreateTemp(filepath.Dir(path), ".loop-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func startLoopJob(root, id, token, config string) (string, error) {
	tmp, err := os.MkdirTemp("", "bl-loop-")
	if err != nil {
		return "", err
	}
	prompt := filepath.Join(tmp, "prompt.md")
	out := filepath.Join(tmp, "out.log")
	content := fmt.Sprintf("Write your final report to %s; a report printed only to stdout is lost. Work on backlog item %s in this fresh session. Follow playbooks/item-loop.md. First read `bl show %s` and `.backlog/HANDOFF.md`. Decide routine and cross-item technical choices yourself as architect. If only the human can answer, park the item and record the question. Commit locally and never push. Exit after the item is closed or parked.\n", out, id, id)
	if err := os.WriteFile(prompt, []byte(content), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, nil, 0o600); err != nil {
		return "", err
	}
	if config == "" {
		config = filepath.Join("scripts", "bl-loop.dispatch.conf")
	}
	if !filepath.IsAbs(config) {
		config = filepath.Join(root, config)
	}
	bin := os.Getenv("BL_DISPATCH")
	if bin == "" {
		bin = "dispatch"
	}
	cmd := exec.Command(bin, "start", "brain", prompt, out, "--config", config, "--title", "bl-loop-"+id+"-"+token)
	cmd.Dir = root
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	job := strings.TrimSpace(stdout.String())
	if job == "" {
		return "", fmt.Errorf("dispatch returned no job id")
	}
	return strings.Fields(job)[0], nil
}

type loopTokenUsage struct {
	Input      int `json:"input_tokens"`
	CacheWrite int `json:"cache_creation_input_tokens"`
	CacheRead  int `json:"cache_read_input_tokens"`
	Output     int `json:"output_tokens"`
}

func (u loopTokenUsage) total() int { return u.Input + u.CacheWrite + u.Output }

func waitLoopJob(root string, assoc *loopAssociation, assocPath string, started time.Time, timeoutSec, pollMS, maxTokens int) error {
	bin := os.Getenv("BL_DISPATCH")
	if bin == "" {
		bin = "dispatch"
	}
	for {
		usage, usageErr := loopJobTokenUsage(assoc.Job)
		if usageErr != nil {
			return loopExit(5, "cannot read token usage for %s: %v", assoc.Item, usageErr)
		}
		total := usage.total()
		if total > maxTokens {
			assoc.Tokens = total
			if err := writeAssociation(assocPath, assoc); err != nil {
				return loopExit(5, "cannot save token usage for %s: %v", assoc.Item, err)
			}
			cancel := exec.Command(bin, "cancel", assoc.Job)
			cancel.Dir = root
			_ = cancel.Run()
			fmt.Printf("%s tokens %d (input %d cache-write %d cache-read %d output %d)\n", assoc.Item, total, usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output)
			return loopExit(5, "token budget exceeded for %s: %d > %d", assoc.Item, total, maxTokens)
		}
		cmd := exec.Command(bin, "status", assoc.Job)
		cmd.Dir = root
		data, err := cmd.Output()
		if err != nil {
			fmt.Printf("%s tokens %d (input %d cache-write %d cache-read %d output %d)\n", assoc.Item, total, usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output)
			return loopExit(5, "dispatch status for %s failed: %v", assoc.Item, err)
		}
		fields := strings.Fields(string(data))
		if len(fields) >= 2 {
			switch fields[1] {
			case "done":
				fmt.Printf("%s tokens %d (input %d cache-write %d cache-read %d output %d)\n", assoc.Item, total, usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output)
				return nil
			case "failed", "lost", "cancelled":
				fmt.Printf("%s tokens %d (input %d cache-write %d cache-read %d output %d)\n", assoc.Item, total, usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output)
				return loopExit(5, "job for %s ended %s; association retained at %s", assoc.Item, fields[1], assocPath)
			}
		}
		remaining := time.Duration(timeoutSec)*time.Second - time.Since(started)
		if remaining <= 0 {
			fmt.Printf("%s tokens %d (input %d cache-write %d cache-read %d output %d)\n", assoc.Item, total, usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output)
			return loopExit(5, "timed out waiting for %s; association retained at %s", assoc.Item, assocPath)
		}
		pause := time.Duration(pollMS) * time.Millisecond
		if pause > remaining {
			pause = remaining
		}
		time.Sleep(pause)
	}
}

func loopJobTokenUsage(job string) (loopTokenUsage, error) {
	var total loopTokenUsage
	home := os.Getenv("DISPATCH_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return total, err
		}
		home = filepath.Join(user, ".local", "state", "dispatch")
	}
	data, err := os.ReadFile(filepath.Join(home, "jobs", job, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return total, nil
	}
	if err != nil {
		return total, err
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		var event struct {
			Type    string `json:"type"`
			Message struct {
				ID    string         `json:"id"`
				Usage loopTokenUsage `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.Type != "assistant" || event.Message.ID == "" || seen[event.Message.ID] {
			continue
		}
		seen[event.Message.ID] = true
		total.Input += event.Message.Usage.Input
		total.CacheWrite += event.Message.Usage.CacheWrite
		total.CacheRead += event.Message.Usage.CacheRead
		total.Output += event.Message.Usage.Output
	}
	return total, nil
}

func completeLoopItem(root string, assoc *loopAssociation) error {
	state, err := loopState(root)
	if err != nil {
		return loopExit(5, "bl check failed for %s: %v", assoc.Item, err)
	}
	events, err := readLedger(root)
	if err != nil {
		return loopExit(5, "cannot read item %s status: %v", assoc.Item, err)
	}
	_, byID, err := derive(events)
	if err != nil {
		return loopExit(5, "cannot derive item %s status: %v", assoc.Item, err)
	}
	it, ok := byID[strings.ToLower(assoc.Item)]
	if !ok || (it.Status != "closed" && it.Status != "parked" && it.Status != "declined") {
		status := "missing"
		if ok {
			status = it.Status
		}
		return loopExit(5, "item %s remains %s", assoc.Item, status)
	}
	if state.inFlight != "" {
		return loopExit(5, "item %s finished but %s is in flight", assoc.Item, state.inFlight)
	}
	bin := os.Getenv("BL_DISPATCH")
	if bin == "" {
		bin = "dispatch"
	}
	cmd := exec.Command(bin, "status")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return loopExit(5, "cannot inspect running jobs after %s: %v", assoc.Item, err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.Contains(line, "running") {
			return loopExit(5, "job for %s finished while a dispatch job is still running", assoc.Item)
		}
	}
	return nil
}
