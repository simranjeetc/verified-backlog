package main

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func cmdLoopCancel(root string, a args) error {
	request := filepath.Join(root, backlogDir, "loop.cancel")
	if err := os.WriteFile(request, []byte("cancel requested\n"), 0600); err != nil {
		return err
	}
	defer os.Remove(request)
	assocPath := filepath.Join(root, backlogDir, "loop.json")
	data, err := os.ReadFile(assocPath)
	if os.IsNotExist(err) {
		return loopExit(2, "no loop association")
	}
	if err != nil {
		return err
	}
	var assoc loopAssociation
	if json.Unmarshal(data, &assoc) != nil || assoc.Item == "" {
		return loopExit(5, "invalid loop association; attended recovery required")
	}
	timeout := 120
	if a.get("timeout") != "" {
		timeout, err = strconv.Atoi(a.get("timeout"))
		if err != nil || timeout < 1 {
			return refuse("--timeout must be at least 1")
		}
	}
	reason := a.get("reason")
	if reason == "" {
		reason = "cancelled by bl loop --cancel"
	}
	reason, err = cleanText(reason, "reason")
	if err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	_, byID, err := derive(events)
	if err != nil {
		return err
	}
	it, err := lookup(byID, assoc.Item)
	if err != nil {
		return err
	}
	var left []string
	// Stop the brain first so it cannot start more detached workers during discovery.
	if assoc.Job != "" {
		left = append(left, cancelLoopJobs(dispatchBin(), map[string]string{assoc.Job: root}, time.Duration(timeout)*time.Second)...)
	}
	refreshLoopDirs(root, &assoc)
	paths, err := loopWorktreePaths(root)
	if err != nil {
		return err
	}
	left = append(left, drainLoopWorkers(root, &assoc, true, time.Duration(timeout)*time.Second)...)
	// Workers may also create worktrees or record ledger state while stopping.
	paths, err = loopWorktreePaths(root)
	if err != nil {
		return err
	}
	events, err = readLedger(root)
	if err != nil {
		return err
	}
	_, byID, err = derive(events)
	if err != nil {
		return err
	}
	it, err = lookup(byID, assoc.Item)
	if err != nil {
		return err
	}
	stopped := len(left) == 0
	archiveDir := filepath.Join(root, backlogDir, "cancelled")
	if err := os.MkdirAll(archiveDir, 0700); err != nil {
		return err
	}
	var artifacts []string
	if it.Status != "closed" && it.Status != "declined" && it.BeforeTree != "" {
		patch, err := writeCancelPatch(root, it)
		if err != nil {
			left = append(left, "root patch: "+err.Error())
		} else if patch != "" {
			artifacts = append(artifacts, patch)
			if stopped {
				if err := restoreCancelPatch(root, it, patch); err != nil {
					left = append(left, "root files retained: "+err.Error())
				}
			} else {
				left = append(left, "root files retained because workers were not confirmed stopped")
			}
		}
	} else if it.Status == "open" && assoc.StartTree != "" {
		patch, err := saveCancelDiff(root, root, &Item{ID: assoc.Item, BeforeTree: assoc.StartTree})
		if err != nil {
			left = append(left, "root patch: "+err.Error())
		} else if patch != "" {
			artifacts = append(artifacts, patch)
			left = append(left, "root item baseline unavailable; files retained")
		}
	} else if it.Opened {
		left = append(left, "root baseline unavailable; files retained")
	}
	// Only worktrees under persisted session directories belong to this loop.
	for _, wt := range paths[1:] {
		owned := false
		for _, dir := range assoc.Dirs {
			rel, err := filepath.Rel(resolvedPath(dir), resolvedPath(wt))
			if err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				owned = true
				break
			}
		}
		if !owned {
			continue
		}
		if !stopped {
			left = append(left, "worktree retained with potentially live writers: "+wt)
			continue
		}
		before := it.BeforeTree
		if before == "" {
			before = assoc.StartTree
		}
		if before != "" {
			patch, err := saveCancelDiff(root, wt, &Item{ID: assoc.Item, BeforeTree: before})
			if err != nil {
				left = append(left, fmt.Sprintf("worktree %s patch: %v", wt, err))
				continue
			}
			if patch != "" {
				artifacts = append(artifacts, patch)
			}
		}
		archive, err := archiveCancelWorktree(root, assoc.Item, wt)
		if err != nil {
			left = append(left, fmt.Sprintf("worktree %s archive: %v", wt, err))
			continue
		}
		artifacts = append(artifacts, archive)
		head, code := git(wt, "rev-parse", "HEAD")
		if code != 0 {
			left = append(left, "cannot preserve worktree HEAD: "+wt)
			continue
		}
		head = strings.TrimSpace(head)
		ref := "refs/bl-cancel/" + assoc.Item + "/" + head
		if _, code := git(root, "update-ref", ref, head); code != 0 {
			left = append(left, "cannot preserve worktree commit history: "+wt)
			continue
		}
		artifacts = append(artifacts, "git ref "+ref)
		cmd := exec.Command("git", "worktree", "remove", "--force", wt)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			left = append(left, fmt.Sprintf("worktree %s not removed: %s", wt, strings.TrimSpace(string(out))))
		}
	}
	finished := it.Status == "closed" || it.Status == "parked" || it.Status == "declined"
	for _, p := range artifacts {
		reason += "; partial work at " + p
	}
	if !finished {
		if err := cmdDefer(root, args{flags: map[string]string{"reason": reason}, positional: []string{assoc.Item}}); err != nil {
			return err
		}
	} else if len(artifacts) > 0 {
		if err := cmdNote(root, args{flags: map[string]string{"text": reason}, positional: []string{assoc.Item}}); err != nil {
			return err
		}
	}
	if len(left) > 0 {
		text := strings.Join(strings.Fields("bl loop --cancel of "+assoc.Item+" left: "+strings.Join(left, "; ")), " ")
		id, err := nextLeftID(root, assoc.Item)
		if err != nil {
			return err
		}
		if err := cmdFile(root, args{flags: map[string]string{"id": id, "text": text, "source": "loop-cancel"}}); err != nil {
			return loopExit(5, "cannot record leftovers; association retained: %v; %s", err, text)
		}
		fmt.Printf("filed %s: %s\n", id, text)
	}
	if err := os.Remove(assocPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	fmt.Printf("cancelled %s; association released\n", assoc.Item)
	for _, p := range artifacts {
		fmt.Printf("partial work %s\n", p)
	}
	return nil
}

func writeCancelPatch(root string, it *Item) (string, error) { return saveCancelDiff(root, root, it) }

func saveCancelDiff(root, source string, it *Item) (string, error) {
	current, err := verifiedTreeWriting(source)
	if err != nil {
		return "", err
	}
	argv := []string{"--literal-pathspecs", "diff", "--binary", "--no-renames", "--no-ext-diff", "--no-textconv", it.BeforeTree, current}
	if len(it.Scope) > 0 {
		argv = append(argv, "--")
		argv = append(argv, it.Scope...)
	}
	diff, code := git(source, argv...)
	if code != 0 {
		return "", fmt.Errorf("cannot compute item diff")
	}
	if diff == "" {
		return "", nil
	}
	f, err := os.CreateTemp(filepath.Join(root, backlogDir, "cancelled"), it.ID+"-*.patch")
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err = f.WriteString(diff); err != nil {
		return "", err
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	return relativeToRoot(root, f.Name()), nil
}

func restoreCancelPatch(root string, it *Item, patch string) error {
	head, code := git(root, "rev-parse", "HEAD")
	if code != 0 {
		return fmt.Errorf("HEAD unavailable")
	}
	before, err := baselineEntries(root, it.BeforeTree)
	if err != nil {
		return err
	}
	committed, err := baselineEntries(root, strings.TrimSpace(head))
	if err != nil {
		return err
	}
	// An absent explicit scope means the entire repository. Never uncommit any
	// entry or overwrite unrelated staged work in the root.
	for p, entry := range before {
		if inScope(it.Scope, p) && !isArtifact(p) && committed[p] != entry {
			return fmt.Errorf("committed in-scope change at %s", p)
		}
	}
	for p, entry := range committed {
		if inScope(it.Scope, p) && !isArtifact(p) && before[p] != entry {
			return fmt.Errorf("committed in-scope change at %s", p)
		}
	}
	staged, code := git(root, "diff", "--cached", "--name-only", "-z")
	if code != 0 {
		return fmt.Errorf("cannot inspect staged files")
	}
	for _, p := range strings.Split(staged, "\x00") {
		if p != "" && inScope(it.Scope, p) && !isArtifact(p) {
			return fmt.Errorf("staged in-scope change at %s", p)
		}
	}
	cmd := exec.Command("git", "apply", "--check", "-R", "--binary", filepath.Join(root, patch))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reverse patch check: %s", strings.TrimSpace(string(out)))
	}
	cmd = exec.Command("git", "apply", "-R", "--binary", filepath.Join(root, patch))
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("reverse patch: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// Archive the whole owned worktree, including ignored and untracked files. A patch
// alone cannot preserve those, or commits made in a detached worktree.
func archiveCancelWorktree(root, id, wt string) (string, error) {
	f, err := os.CreateTemp(filepath.Join(root, backlogDir, "cancelled"), id+"-worktree-*.tar.gz")
	if err != nil {
		return "", err
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	err = filepath.Walk(wt, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(wt, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		if rel == ".git" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := ""
		if info.Mode()&os.ModeSymlink != 0 {
			target, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, target)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(rel)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, in)
			closeErr := in.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		return nil
	})
	err = errors.Join(err, tw.Close(), gz.Close(), f.Sync(), f.Close())
	if err != nil {
		return "", err
	}
	return relativeToRoot(root, f.Name()), nil
}
func relativeToRoot(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return filepath.ToSlash(rel)
}
func resolvedPath(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	return path
}
func nextLeftID(root, base string) (string, error) {
	events, err := readLedger(root)
	if err != nil {
		return "", err
	}
	_, byID, err := derive(events)
	if err != nil {
		return "", err
	}
	for n := 1; ; n++ {
		id := fmt.Sprintf("%s-left-%d", base, n)
		if _, ok := byID[strings.ToLower(id)]; !ok {
			return id, nil
		}
	}
}
