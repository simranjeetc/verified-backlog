package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

func requestLoopStop(root string, lock *os.File) error {
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		return nil
	}
	request := filepath.Join(root, backlogDir, "loop.cancel")
	if err := os.WriteFile(request, []byte("cancel requested\n"), 0600); err != nil {
		return err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return loopExit(5, "loop owner did not release its lock; cancellation request retained at %s", request)
}

func recordLoopSession(root, token, dir string) error {
	path := filepath.Join(root, backlogDir, "loop.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var assoc loopAssociation
	if err = json.Unmarshal(data, &assoc); err != nil {
		return err
	}
	if assoc.Token != token {
		return fmt.Errorf("loop association changed before launch")
	}
	assoc.Dirs = append(assoc.Dirs, dir)
	return writeAssociation(path, &assoc)
}

// Persisted session directories, not model-provided paths, locate receipts.
func registeredLoopJobs(assoc *loopAssociation) (map[string]string, error) {
	jobs := map[string]string{}
	var problems []error
	for _, dir := range assoc.Dirs {
		entries, err := os.ReadDir(filepath.Join(dir, "workers"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			data, err := os.ReadFile(filepath.Join(dir, "workers", entry.Name()))
			if err != nil {
				problems = append(problems, err)
				continue
			}
			var rec struct {
				Job   string `json:"job"`
				Cwd   string `json:"cwd"`
				Token string `json:"token"`
			}
			if json.Unmarshal(data, &rec) != nil || rec.Job == "" || rec.Cwd == "" || rec.Token != assoc.Token || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(rec.Job) || !filepath.IsAbs(rec.Cwd) {
				problems = append(problems, fmt.Errorf("invalid worker receipt %s", entry.Name()))
				continue
			}
			jobs[rec.Job] = rec.Cwd
		}
	}
	return jobs, errors.Join(problems...)
}

func dispatchBin() string {
	if bin := os.Getenv("BL_DISPATCH"); bin != "" {
		return bin
	}
	return "dispatch"
}

func boundedDispatch(cwd, bin string, duration time.Duration, argv ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, argv...)
	cmd.Dir = cwd
	data, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("dispatch %s: %v: %s", strings.Join(argv, " "), err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

// cancelLoopJobs verifies termination; an accepted cancel alone proves nothing.
func cancelLoopJobs(bin string, jobs map[string]string, timeout time.Duration) []string {
	ids := make([]string, 0, len(jobs))
	for id := range jobs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	var left []string
	deadline := time.Now().Add(timeout)
	for _, id := range ids {
		cwd := jobs[id]
		commandCwd := cwd
		if _, err := os.Stat(commandCwd); err != nil {
			commandCwd = os.TempDir()
		}
		out, err := boundedDispatch(commandCwd, bin, 5*time.Second, "status", id)
		f := strings.Fields(out)
		if err == nil && len(f) >= 2 && f[1] == "lost" {
			left = append(left, fmt.Sprintf("job %s in %s is lost; termination cannot be confirmed", id, cwd))
			continue
		}
		if err == nil && len(f) >= 2 && dispatchTerminal(f[1]) {
			continue
		}
		if _, err := boundedDispatch(commandCwd, bin, 15*time.Second, "cancel", id); err != nil {
			left = append(left, fmt.Sprintf("job %s in %s: %v", id, cwd, err))
			continue
		}
		for {
			out, err = boundedDispatch(commandCwd, bin, 5*time.Second, "status", id)
			f = strings.Fields(out)
			if err == nil && len(f) >= 2 && f[1] == "lost" {
				left = append(left, fmt.Sprintf("job %s in %s became lost; termination cannot be confirmed", id, cwd))
				break
			}
			if err == nil && len(f) >= 2 && dispatchTerminal(f[1]) {
				fmt.Printf("worker job %s ended %s\n", id, f[1])
				break
			}
			if err != nil || time.Now().After(deadline) {
				left = append(left, fmt.Sprintf("job %s in %s not confirmed stopped (%s; %v)", id, cwd, strings.TrimSpace(out), err))
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	return left
}

func reapLoopWorkers(root string, assoc *loopAssociation) (result error) {
	// Reload: session ownership is saved before startLoopJob returns.
	if data, err := os.ReadFile(filepath.Join(root, backlogDir, "loop.json")); err == nil {
		var saved loopAssociation
		if json.Unmarshal(data, &saved) == nil && saved.Token == assoc.Token {
			assoc.Dirs = saved.Dirs
		}
	}
	if left := drainLoopWorkers(root, assoc, false, 10*time.Second); len(left) > 0 {
		return loopExit(5, "orphan workers: %s", strings.Join(left, "; "))
	}
	return nil
}

func refreshLoopDirs(root string, assoc *loopAssociation) {
	data, err := os.ReadFile(filepath.Join(root, backlogDir, "loop.json"))
	if err != nil {
		return
	}
	var saved loopAssociation
	if json.Unmarshal(data, &saved) == nil && saved.Token == assoc.Token {
		assoc.Dirs = saved.Dirs
	}
}

// Dispatch writes the title in job metadata before detaching its supervisor. The
// ownership tag closes the start-to-receipt gap if the wrapper is killed there.
func discoverLoopJobs(assoc *loopAssociation) (map[string]string, error) {
	jobs, receiptErr := registeredLoopJobs(assoc)
	home := os.Getenv("DISPATCH_HOME")
	if home == "" {
		user, err := os.UserHomeDir()
		if err != nil {
			return jobs, errors.Join(receiptErr, err)
		}
		home = filepath.Join(user, ".local", "state", "dispatch")
	}
	dirs, err := os.ReadDir(filepath.Join(home, "jobs"))
	if os.IsNotExist(err) {
		return jobs, receiptErr
	}
	if err != nil {
		return jobs, errors.Join(receiptErr, err)
	}
	var problems []error
	if receiptErr != nil {
		problems = append(problems, receiptErr)
	}
	if assoc.Token == "" {
		return jobs, errors.Join(problems...)
	}
	for _, entry := range dirs {
		if !entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(home, "jobs", entry.Name(), "meta"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			problems = append(problems, err)
			continue
		}
		tagged := false
		cwd := ""
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "title=bl-worker-"+assoc.Token+"-") {
				tagged = true
			}
			if strings.HasPrefix(line, "cwd=") {
				cwd = strings.TrimPrefix(line, "cwd=")
			}
		}
		if tagged {
			if !filepath.IsAbs(cwd) || !regexp.MustCompile(`^[A-Za-z0-9_-]+$`).MatchString(entry.Name()) {
				problems = append(problems, fmt.Errorf("invalid tagged worker metadata: %s", entry.Name()))
				continue
			}
			jobs[entry.Name()] = cwd
		}
	}
	return jobs, errors.Join(problems...)
}

// Fence first, then drain until discovery stabilizes. A worker may have passed
// preflight just before the fence, so one receipt/metadata snapshot is insufficient.
func drainLoopWorkers(root string, assoc *loopAssociation, includeRepo bool, timeout time.Duration) []string {
	var left []string
	deadline := time.Now().Add(timeout)
	for _, dir := range assoc.Dirs {
		if err := os.WriteFile(filepath.Join(dir, "workers.stopping"), []byte("stopping\n"), 0600); err != nil && !os.IsNotExist(err) {
			left = append(left, "cannot fence session "+dir+": "+err.Error())
		}
	}
	previous := ""
	for {
		pending := []string{}
		for _, dir := range assoc.Dirs {
			if err := fenceLoopSession(dir, time.Now()); err != nil {
				pending = append(pending, err.Error())
			}
		}
		// Discovery follows the barrier: a launch may publish its job just before
		// releasing the inherited descriptor.
		jobs, err := discoverLoopJobs(assoc)
		if err != nil {
			left = append(left, err.Error())
		}
		if includeRepo {
			paths, pathErr := loopWorktreePaths(root)
			if pathErr != nil {
				left = append(left, pathErr.Error())
			} else {
				for _, cwd := range paths {
					out, err := boundedDispatch(cwd, dispatchBin(), 5*time.Second, "status")
					if err != nil {
						left = append(left, fmt.Sprintf("cannot inspect jobs in %s: %v", cwd, err))
						continue
					}
					for _, line := range strings.Split(out, "\n") {
						f := strings.Fields(line)
						if len(f) >= 2 && f[1] != "done" && f[1] != "failed" && f[1] != "cancelled" {
							jobs[f[0]] = cwd
						}
					}
				}
			}
		}
		problems := cancelLoopJobs(dispatchBin(), jobs, max(time.Until(deadline), time.Millisecond))
		if len(problems) > 0 {
			return append(left, problems...)
		}
		ids := make([]string, 0, len(jobs))
		for id := range jobs {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		current := strings.Join(ids, "\n")
		if current == previous && len(pending) == 0 {
			return left
		}
		if time.Now().After(deadline) {
			return append(append(left, pending...), "worker discovery did not stabilize before timeout")
		}
		previous = current
		time.Sleep(20 * time.Millisecond)
	}
}

func fenceLoopSession(dir string, deadline time.Time) error {
	if err := os.WriteFile(filepath.Join(dir, "workers.stopping"), []byte("stopping\n"), 0600); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("cannot fence session %s: %w", dir, err)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "workers.launch.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	for {
		if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("pending worker launch did not release session lock: %s", dir)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
