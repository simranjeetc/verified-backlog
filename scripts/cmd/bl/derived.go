package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

type derivedRun struct {
	Control derivedControl `json:"control"`
	Paths   []derivedPath  `json:"paths"`
}
type derivedControl struct {
	Verdict   string `json:"verdict"`
	FailedRow string `json:"failed_row,omitempty"`
	Reason    string `json:"reason,omitempty"`
}
type derivedPath struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Kind        string `json:"kind"`
	Verdict     string `json:"verdict"`
	Weak        bool   `json:"weak"`
	KilledBy    string `json:"killed_by"`
	Exit        int    `json:"exit"`
	TimedOut    bool   `json:"timed_out"`
	MS          int64  `json:"ms"`
	BuildOutput string `json:"build_output,omitempty"`
}

func changedPaths(root, before, after string) ([]derivedPath, error) {
	if before == "" || after == "" || !isGit(root) {
		return nil, nil
	}
	cmd := exec.Command("git", "diff", "--no-renames", "--name-status", "-z", before, after)
	cmd.Dir = root
	b, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	parts := bytes.Split(b, []byte{0})
	var ps []derivedPath
	for i := 0; i+1 < len(parts); {
		status := string(parts[i])
		path := string(parts[i+1])
		i += 2
		if path == "" {
			continue
		}
		if isArtifact(path) || strings.HasSuffix(path, ".md") {
			continue
		}
		kind := "modified"
		if strings.HasPrefix(status, "A") {
			kind = "added"
		} else if strings.HasPrefix(status, "D") {
			kind = "deleted"
		}
		ps = append(ps, derivedPath{Path: path, Kind: kind})
	}
	sort.Slice(ps, func(i, j int) bool { return ps[i].Path < ps[j].Path })
	for i := range ps {
		ps[i].ID = fmt.Sprintf("D%d", i+1)
		if isCodeTestPath(ps[i].Path) {
			ps[i].Verdict = "exempt"
		}
	}
	return ps, nil
}
func isCodeTestPath(p string) bool {
	b := filepath.Base(p)
	if strings.HasSuffix(b, "_test.go") || strings.Contains(b, ".test.") || strings.HasPrefix(b, "test_") {
		return true
	}
	for _, s := range strings.Split(p, "/") {
		if s == "tests" {
			return true
		}
	}
	return false
}

func deriveCoverage(root string, it *Item, rows []checkRow, records []runRow, env []string, before, after string) (*derivedRun, error) {
	if !isGit(root) || before == "" || after == "" {
		return nil, nil
	}
	ps, err := changedPaths(root, before, after)
	if err != nil {
		return nil, nil
	}
	dr := &derivedRun{Paths: ps}
	if len(ps) == 0 {
		dr.Paths = []derivedPath{}
		dr.Control.Verdict = "none"
		return dr, nil
	}
	passed := map[string]bool{}
	for _, r := range records {
		passed[r.ID] = r.Verdict == "PASS"
	}
	var suite []checkRow
	for _, r := range rows {
		if r.f["severity"] == "blocking" && r.f["decider"] == "bl" && passed[r.f["id"]] {
			suite = append(suite, r)
		}
	}
	home := envValue(env, "HOME")
	if home == "" {
		home = os.TempDir()
	}
	control, clean, e := scratchTree(root, after, it.ID)
	if e != nil {
		dr.Control.Verdict = "FAIL"
		dr.Control.Reason = e.Error()
		markUnusable(dr)
		return dr, nil
	}
	defer clean()
	cr := deriveRows(control, suite, env, home, after)
	dr.Control.Verdict = "PASS"
	for _, r := range cr {
		if r.Verdict != "PASS" {
			dr.Control.Verdict = "FAIL"
			dr.Control.FailedRow = r.ID
			break
		}
	}
	if len(suite) == 0 && dr.Control.Verdict != "FAIL" {
		dr.Control.Verdict = "none"
	}
	for i := range dr.Paths {
		p := &dr.Paths[i]
		if p.Verdict == "exempt" {
			continue
		}
		if dr.Control.Verdict != "PASS" {
			p.Verdict = "unusable"
			continue
		}
		scratch, done, e := scratchTree(root, after, it.ID)
		if e != nil {
			p.Verdict = "unusable"
			continue
		}
		if p.Kind == "added" {
			e = os.RemoveAll(filepath.Join(scratch, filepath.FromSlash(p.Path)))
		} else {
			e = restorePath(root, it.BeforeTree, scratch, p.Path)
		}
		if e != nil {
			done()
			p.Verdict = "unusable"
			continue
		}
		rowEnv := env
		if _, statErr := os.Stat(filepath.Join(scratch, "scripts", "cmd", "bl")); statErr == nil {
			bin := filepath.Join(scratch, ".bl-under-test")
			build := exec.Command("go", "-C", "scripts", "build", "-o", bin, "./cmd/bl")
			build.Dir = scratch
			build.Env = setEnv(append([]string{}, env...), "HOME", home)
			var output bytes.Buffer
			build.Stdout, build.Stderr = &output, &output
			if buildErr := build.Run(); buildErr != nil {
				p.Verdict, p.Exit, p.BuildOutput = "red", 1, output.String()
				done()
				continue
			}
			rowEnv = setEnv(append([]string{}, env...), "BL_UNDER_TEST", bin)
		}
		recs := deriveRows(scratch, suite, rowEnv, home, after)
		done()
		p.Verdict = "green"
		for _, r := range recs {
			p.Exit = r.Exit
			p.TimedOut = r.TimedOut
			p.MS += r.MS
			if r.Verdict != "PASS" {
				p.Verdict = "red"
				p.KilledBy = r.ID
				break
			}
		}
		if p.Kind == "added" && p.Verdict == "red" {
			p.Weak = true
		}
	}
	return dr, nil
}
func markUnusable(d *derivedRun) {
	for i := range d.Paths {
		if d.Paths[i].Verdict != "exempt" {
			d.Paths[i].Verdict = "unusable"
		}
	}
}
func scratchTree(root, tree, id string) (string, func(), error) {
	base, e := scratchDir(root)
	if e != nil {
		return "", nil, e
	}
	cleanup := func() { _ = os.RemoveAll(base) }
	c := exec.Command("git", "archive", tree)
	c.Dir = root
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, e := c.StdoutPipe()
	if e != nil {
		cleanup()
		return "", nil, e
	}
	if e = c.Start(); e != nil {
		cleanup()
		return "", nil, e
	}
	tar := exec.Command("/usr/bin/tar", "-x", "-C", base)
	tar.Stdin = out
	var terr bytes.Buffer
	tar.Stderr = &terr
	te := tar.Run()
	ce := c.Wait()
	if te != nil || ce != nil {
		cleanup()
		return "", nil, fmt.Errorf("archive extraction: %v %s %v %s", te, terr.String(), ce, stderr.String())
	}
	itemSrc := filepath.Join(root, backlogDir, "items", id)
	itemDst := filepath.Join(base, backlogDir, "items", id)
	if _, e = os.Stat(itemSrc); e == nil {
		if e = os.MkdirAll(filepath.Dir(itemDst), 0755); e != nil {
			cleanup()
			return "", nil, e
		}
		if e = exec.Command("/bin/cp", "-R", itemSrc, itemDst).Run(); e != nil {
			cleanup()
			return "", nil, e
		}
	}
	if e = initializeScratchGit(base); e != nil {
		cleanup()
		return "", nil, e
	}
	return base, cleanup, nil
}

func initializeScratchGit(dir string) error {
	commands := [][]string{
		{"git", "init", "-q"},
		{"git", "add", "-A"},
		{"git", "-c", "user.name=bl scratch", "-c", "user.email=bl-scratch@example.invalid", "commit", "--allow-empty", "-qm", "scratch tree"},
	}
	for _, args := range commands {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("initialize scratch Git repository (%s): %w: %s", strings.Join(args, " "), err, output)
		}
	}
	return nil
}
func restorePath(root, tree, dest, path string) error {
	parent := filepath.Dir(path)
	if parent != "." {
		_ = os.MkdirAll(filepath.Join(dest, filepath.FromSlash(parent)), 0755)
	}
	c := exec.Command("git", "archive", tree, "--", path)
	c.Dir = root
	out, e := c.StdoutPipe()
	if e != nil {
		return e
	}
	if e = c.Start(); e != nil {
		return e
	}
	tar := exec.Command("/usr/bin/tar", "-x", "-C", dest)
	tar.Stdin = out
	te := tar.Run()
	ce := c.Wait()
	if te != nil {
		return te
	}
	if ce != nil {
		return ce
	}
	return nil
}
func deriveRows(dir string, rows []checkRow, env []string, home, tree string) []runRow {
	var result []runRow
	for _, row := range rows {
		tmp, e := os.MkdirTemp("", "bl-derive-row-")
		if e != nil {
			result = append(result, runRow{ID: row.f["id"], Exit: 1, Verdict: "FAIL"})
			break
		}
		rowEnv := append([]string{}, env...)
		rowEnv = setEnv(rowEnv, "TMPDIR", tmp)
		rowEnv = setEnv(rowEnv, "HOME", home)
		rec := execRow(dir, row, rowEnv, runTimeout)
		_ = os.RemoveAll(tmp)
		v, _ := expectVerdict(dir, row.f["id"], row.f["expect"], rec, tree)
		if rec.TimedOut {
			v = "FAIL"
		}
		rec.Verdict = v
		result = append(result, rec)
		if v != "PASS" {
			break
		}
	}
	return result
}
func setEnv(env []string, key, value string) []string {
	prefix := key + "="
	out := env[:0]
	for _, v := range env {
		if !strings.HasPrefix(v, prefix) {
			out = append(out, v)
		}
	}
	return append(out, prefix+value)
}
