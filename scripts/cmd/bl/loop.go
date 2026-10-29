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
	Sessions int    `json:"sessions,omitempty"`
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
	maxItems, pollMS, timeoutSec, maxContext, maxSessions := 3, 5000, 14400, 300000, 5
	var err error
	if _, ok := a.flags["max-tokens"]; ok {
		return refuse("unknown flag --max-tokens")
	}
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
	if _, ok := a.flags["max-context"]; ok {
		maxContext, err = strconv.Atoi(a.get("max-context"))
		if err != nil || maxContext < 1 {
			return refuse("--max-context must be at least 1")
		}
	}
	if _, ok := a.flags["max-sessions"]; ok {
		maxSessions, err = strconv.Atoi(a.get("max-sessions"))
		if err != nil || maxSessions < 1 {
			return refuse("--max-sessions must be at least 1")
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
		if assoc.Sessions < 1 {
			assoc.Sessions = 1
		}
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
		if err := waitLoopJob(root, assoc, assocPath, loopStarted, timeoutSec, pollMS, maxContext, maxSessions, a.get("dispatch-config")); err != nil {
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
		assoc = &loopAssociation{Item: id, Token: hex.EncodeToString(tokenBytes), Launches: launches + 1, Sessions: 1}
		if err := writeAssociation(assocPath, assoc); err != nil {
			return err
		}
		launches++
		job, err := startLoopJob(root, id, assoc.Token, a.get("dispatch-config"), false)
		if err != nil {
			return loopExit(5, "dispatch start for %s failed: %v", id, err)
		}
		assoc.Job = job
		if err := writeAssociation(assocPath, assoc); err != nil {
			return err
		}
		if err := waitLoopJob(root, assoc, assocPath, loopStarted, timeoutSec, pollMS, maxContext, maxSessions, a.get("dispatch-config")); err != nil {
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

func startLoopJob(root, id, token, config string, continuation bool) (string, error) {
	tmp, err := os.MkdirTemp("", "bl-loop-")
	if err != nil {
		return "", err
	}
	prompt := filepath.Join(tmp, "prompt.md")
	out := filepath.Join(tmp, "out.log")
	content := fmt.Sprintf("Write your final report to %s; a report printed only to stdout is lost. Work on backlog item %s in this fresh session.\n", out, id)
	if continuation {
		content += fmt.Sprintf("A previous session for this item stopped at its context limit. Read `bl show %s`, `.backlog/HANDOFF.md` and `git status` first. Resume from the item's current step without redoing recorded steps.\n", id)
	}
	content += fmt.Sprintf("Follow playbooks/item-loop.md. First read `bl show %s` and `.backlog/HANDOFF.md`. Decide routine and cross-item technical choices yourself as architect. If only the human can answer, park the item and record the question. Commit locally and never push. Exit after the item is closed or parked.\n", id)
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
	Input       int  `json:"input_tokens"`
	CacheWrite  int  `json:"cache_creation_input_tokens"`
	CacheRead   int  `json:"cache_read_input_tokens"`
	Output      int  `json:"output_tokens"`
	Provisional bool `json:"-"`
}

func (u loopTokenUsage) total() int { return u.Input + u.CacheWrite + u.CacheRead + u.Output }

func waitLoopJob(root string, assoc *loopAssociation, assocPath string, started time.Time, timeoutSec, pollMS, maxContext, maxSessions int, config string) error {
	bin := os.Getenv("BL_DISPATCH")
	if bin == "" {
		bin = "dispatch"
	}
	for {
		usage, peak, err := loopJobTokenUsage(assoc.Job)
		if err != nil {
			return loopExit(5, "cannot read token usage for %s: %v", assoc.Item, err)
		}
		total := usage.total()
		if peak > maxContext {
			assoc.Tokens = total
			if err := writeAssociation(assocPath, assoc); err != nil {
				return loopExit(5, "cannot save token usage for %s: %v", assoc.Item, err)
			}
			cancel := exec.Command(bin, "cancel", assoc.Job)
			cancel.Dir = root
			if err := cancel.Run(); err != nil {
				return loopExit(5, "cancel for job %s failed: %v", assoc.Job, err)
			}
			for {
				usage, peak, err = loopJobTokenUsage(assoc.Job)
				if err != nil {
					return loopExit(5, "cannot read token usage for %s: %v", assoc.Item, err)
				}
				total = usage.total()
				state, err := loopDispatchStatus(root, bin, assoc.Job)
				if err != nil {
					return loopExit(5, "dispatch status for %s failed: %v", assoc.Item, err)
				}
				if dispatchTerminal(state) {
					usage, peak, err = loopJobTokenUsage(assoc.Job)
					if err != nil {
						return loopExit(5, "cannot read token usage for %s: %v", assoc.Item, err)
					}
					total = usage.total()
					break
				}
				remaining := time.Duration(timeoutSec)*time.Second - time.Since(started)
				if remaining <= 0 {
					return loopExit(5, "job %s did not end after cancel; association retained at %s", assoc.Job, assocPath)
				}
				pause := time.Duration(pollMS) * time.Millisecond
				if pause > remaining {
					pause = remaining
				}
				time.Sleep(pause)
			}
			fmt.Println(loopUsageSummary(assoc.Item, assoc.Sessions, peak, usage))
			finished, err := itemIsFinished(root, assoc.Item)
			if err != nil {
				return loopExit(5, "cannot determine whether item %s is finished: %v", assoc.Item, err)
			}
			if finished {
				return nil
			}
			if assoc.Sessions >= maxSessions {
				return loopExit(5, "%s used %d sessions", assoc.Item, assoc.Sessions)
			}
			if err := scanLoopWriters(root, bin); err != nil {
				return err
			}
			if err := checkLoopIndexLock(root); err != nil {
				return loopExit(5, "%v", err)
			}
			assoc.Sessions++
			job, err := startLoopJob(root, assoc.Item, assoc.Token, config, true)
			if err != nil {
				return loopExit(5, "dispatch continuation for %s failed: %v", assoc.Item, err)
			}
			assoc.Job = job
			assoc.Tokens = 0
			if err := writeAssociation(assocPath, assoc); err != nil {
				return loopExit(5, "cannot save continuation for %s: %v", assoc.Item, err)
			}
			continue
		}
		state, err := loopDispatchStatus(root, bin, assoc.Job)
		if err != nil {
			return loopExit(5, "dispatch status for %s failed: %v", assoc.Item, err)
		}
		// The job may flush its final result between our usage and status reads.
		if dispatchTerminal(state) {
			usage, peak, err = loopJobTokenUsage(assoc.Job)
			if err != nil {
				return loopExit(5, "cannot read token usage for %s: %v", assoc.Item, err)
			}
			total = usage.total()
		}
		switch state {
		case "done":
			assoc.Tokens = total
			fmt.Println(loopUsageSummary(assoc.Item, assoc.Sessions, peak, usage))
			return nil
		case "failed", "lost", "cancelled":
			fmt.Println(loopUsageSummary(assoc.Item, assoc.Sessions, peak, usage))
			return loopExit(5, "job for %s ended %s; association retained at %s", assoc.Item, state, assocPath)
		}
		remaining := time.Duration(timeoutSec)*time.Second - time.Since(started)
		if remaining <= 0 {
			return loopExit(5, "timed out waiting for %s; association retained at %s", assoc.Item, assocPath)
		}
		pause := time.Duration(pollMS) * time.Millisecond
		if pause > remaining {
			pause = remaining
		}
		time.Sleep(pause)
	}
}

func loopJobTokenUsage(job string) (loopTokenUsage, int, error) {
	total := loopTokenUsage{Provisional: true}
	peak := 0
	home := os.Getenv("DISPATCH_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return total, peak, err
		}
		home = filepath.Join(user, ".local", "state", "dispatch")
	}
	data, err := os.ReadFile(filepath.Join(home, "jobs", job, "log"))
	if errors.Is(err, os.ErrNotExist) {
		return total, peak, nil
	}
	if err != nil {
		return total, peak, err
	}
	return parseLoopTokenUsage(data)
}

func loopUsageSummary(item string, session, peak int, usage loopTokenUsage) string {
	state := "finalized"
	if usage.Provisional {
		state = "provisional"
	}
	return fmt.Sprintf("%s session %d peak-context %d processed-tokens %d (input %d cache-write %d cache-read %d output %d) usage %s", item, session, peak, usage.total(), usage.Input, usage.CacheWrite, usage.CacheRead, usage.Output, state)
}

// Assistant usage may be repeated or partial. A result's modelUsage is a
// cumulative checkpoint, not another request and not a value to add to it.
func parseLoopTokenUsage(data []byte) (loopTokenUsage, int, error) {
	total := loopTokenUsage{Provisional: true}
	peak := 0
	messages := map[string]loopTokenUsage{}
	pending := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		var event struct {
			Type            string  `json:"type"`
			ParentToolUseID *string `json:"parent_tool_use_id"`
			Message         struct {
				ID    string          `json:"id"`
				Usage *loopTokenUsage `json:"usage"`
			} `json:"message"`
			ModelUsage map[string]struct {
				Input      *int `json:"inputTokens"`
				CacheWrite *int `json:"cacheCreationInputTokens"`
				CacheRead  *int `json:"cacheReadInputTokens"`
				Output     *int `json:"outputTokens"`
			} `json:"modelUsage"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || event.ParentToolUseID != nil {
			continue
		}
		if event.Type == "result" && len(event.ModelUsage) > 0 {
			var checkpoint loopTokenUsage
			valid := true
			for _, u := range event.ModelUsage {
				if u.Input == nil || u.CacheWrite == nil || u.CacheRead == nil || u.Output == nil {
					valid = false
					break
				}
				checkpoint.Input += *u.Input
				checkpoint.CacheWrite += *u.CacheWrite
				checkpoint.CacheRead += *u.CacheRead
				checkpoint.Output += *u.Output
			}
			// Ignore replayed older cumulative snapshots; they cannot finalize new work.
			if valid && checkpoint.Input >= total.Input && checkpoint.CacheWrite >= total.CacheWrite && checkpoint.CacheRead >= total.CacheRead && checkpoint.Output >= total.Output && (total.Provisional || checkpoint.total() > total.total()) {
				total = checkpoint
				pending = map[string]bool{}
			}
			continue
		}
		if event.Type != "assistant" || event.Message.ID == "" || event.Message.Usage == nil {
			continue
		}
		id := event.Message.ID
		u := *event.Message.Usage
		previous, seen := messages[id]
		// Fields are cumulative within a message; stale blocks cannot erase usage.
		u.Input = max(u.Input, previous.Input)
		u.CacheWrite = max(u.CacheWrite, previous.CacheWrite)
		u.CacheRead = max(u.CacheRead, previous.CacheRead)
		u.Output = max(u.Output, previous.Output)
		messages[id] = u
		peak = max(peak, u.Input+u.CacheWrite+u.CacheRead)
		if !seen || pending[id] {
			pending[id] = true
		}
	}
	for id := range pending {
		u := messages[id]
		total.Input += u.Input
		total.CacheWrite += u.CacheWrite
		total.CacheRead += u.CacheRead
		total.Output += u.Output
		total.Provisional = true
	}
	return total, peak, nil
}

func loopDispatchStatus(root, bin, job string) (string, error) {
	cmd := exec.Command(bin, "status", job)
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(data))
	if len(fields) < 2 {
		return "", nil
	}
	return fields[1], nil
}

func dispatchTerminal(state string) bool {
	return state == "done" || state == "failed" || state == "lost" || state == "cancelled"
}

func itemIsFinished(root, id string) (bool, error) {
	events, err := readLedger(root)
	if err != nil {
		return false, err
	}
	_, byID, err := derive(events)
	if err != nil {
		return false, err
	}
	it, ok := byID[strings.ToLower(id)]
	return ok && (it.Status == "closed" || it.Status == "parked" || it.Status == "declined"), nil
}

func checkLoopIndexLock(root string) error {
	cmd := exec.Command("git", "rev-parse", "--git-path", "index.lock")
	cmd.Dir = root
	data, err := cmd.Output()
	if err != nil {
		return nil
	}
	path := strings.TrimSpace(string(data))
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("git index.lock exists at %s", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot check git index.lock at %s: %w", path, err)
	}
	return nil
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
	return scanLoopWriters(root, bin)
}

func scanLoopWriters(root, bin string) error {
	paths := []string{root}
	var data []byte
	start := root
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		start = resolved
	}
	gitEntry := ""
	for dir := start; ; dir = filepath.Dir(dir) {
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			gitEntry = filepath.Join(dir, ".git")
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			return loopExit(5, "cannot inspect git from %s: %v", root, err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	if gitEntry != "" {
		probe := exec.Command("git", "rev-parse", "--is-inside-work-tree")
		probe.Dir = root
		if output, err := probe.CombinedOutput(); err != nil {
			return loopExit(5, "cannot inspect git from %s: %s", root, strings.TrimSpace(string(output)))
		}
		cmd := exec.Command("git", "worktree", "list", "--porcelain")
		cmd.Dir = root
		var err error
		data, err = cmd.Output()
		if err != nil {
			return loopExit(5, "cannot list git worktrees from %s: %v", root, err)
		}
	}
	var path string
	prunable := false
	flush := func() {
		if path != "" && !prunable && path != root {
			if info, statErr := os.Stat(path); statErr == nil && info.IsDir() {
				paths = append(paths, path)
			}
		}
		path = ""
		prunable = false
	}
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			flush()
			continue
		}
		if strings.HasPrefix(line, "worktree ") {
			flush()
			path = strings.TrimPrefix(line, "worktree ")
		} else if line == "prunable" || strings.HasPrefix(line, "prunable ") {
			prunable = true
		}
	}
	flush()
	for _, cwd := range paths {
		status := exec.Command(bin, "status")
		status.Dir = cwd
		output, err := status.Output()
		if err != nil {
			return loopExit(5, "dispatch status failed in %s: %v", cwd, err)
		}
		for _, line := range strings.Split(string(output), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || fields[1] == "done" || fields[1] == "failed" || fields[1] == "cancelled" {
				continue
			}
			return loopExit(5, "dispatch job %s is %s in %s", fields[0], fields[1], cwd)
		}
	}
	return nil
}
