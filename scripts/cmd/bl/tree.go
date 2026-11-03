package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// ── the verified tree ────────────────────────────────────────────────────────

// The modes git writes in a tree.
const (
	treeMode = "40000"  // a directory
	fileMode = "100644" // a regular file
	execMode = "100755" // a regular file with the owner's execute bit
	linkMode = "120000" // a symbolic link
)

// treeEntry is one entry of a tree: its mode, its name, and the sha of what it points at.
type treeEntry struct {
	name string
	mode string
	sha  string
}

// verifiedTree computes the verified tree from the FILESYSTEM (§2): every file of the working
// directory — except .git/, the artefacts of R25 (the ledger's own directory, and the two
// rendered views, TODO.md and PROGRESS.md), and the paths .gitignore excludes — is hashed
// with `git hash-object`, and the entries are written as a tree.
//
// The index is never consulted, so a staged-but-unstaged path, a skip-worktree path and an
// assume-unchanged path cannot hide a change from the tree (R26).
func verifiedTree(root string) (string, error) {
	return buildTree(root, false)
}

// verifiedTreeWriting computes the same tree and leaves the objects in the repository's store,
// so that `git diff` can take the difference between two trees: the round's diff is taken
// between the before-tree and the verified tree, and both have to be objects git can read.
func verifiedTreeWriting(root string) (string, error) {
	return buildTree(root, true)
}

func buildTree(root string, write bool) (string, error) {
	// R25: the artefacts are never part of a verified tree, and a repository that tracks them is
	// a repository error — bl says so, wherever a tree is computed.
	reportTrackedArtefact(root)
	b := &treeBuilder{root: root, ignored: map[string]bool{}, write: write}
	if err := b.findIgnored(); err != nil {
		return "", err
	}
	sha, ok, err := b.tree("")
	if err != nil {
		return "", err
	}
	if !ok {
		// an empty working tree is git's empty tree
		return gitStdin(root, "tree", nil, write)
	}
	return sha, nil
}

// treeBuilder computes a tree from the files on disk.
type treeBuilder struct {
	root    string
	ignored map[string]bool
	// write leaves the objects in the repository's store, so that git can diff two trees.
	write bool
}

// findIgnored asks git which of the working tree's paths .gitignore excludes. They are asked
// for in one call: `git check-ignore -z --stdin`.
func (b *treeBuilder) findIgnored() error {
	var paths []string
	err := filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || (p == filepath.Join(b.root, backlogDir)) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(b.root, p)
		if err != nil {
			return err
		}
		if d.Name() == ".git" {
			// a nested .git is never part of the tree, so it is not asked about
			return nil
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = b.root
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 128) {
			// 1: nothing is ignored. 128: not a git repository, so there are no rules.
			return nil
		}
		return err
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			b.ignored[p] = true
		}
	}
	return nil
}

// tree computes the tree of one directory, from the files on disk. It returns false when the
// directory holds nothing git can store: git has no empty directories.
func (b *treeBuilder) tree(rel string) (string, bool, error) {
	dir := filepath.Join(b.root, filepath.FromSlash(rel))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false, err
	}
	var list []treeEntry
	for _, e := range ents {
		name := e.Name()
		if rel == "" && name == backlogDir {
			continue
		}
		if name == ".git" {
			// git's own metadata is never part of a tree, and neither is a nested one: a
			// submodule's gitfile is skipped as well, the way git skips it — git refuses a
			// tree that holds a .git entry, so hashing one would fail the whole tree.
			continue
		}
		child := name
		if rel != "" {
			child = rel + "/" + name
		}
		if b.ignored[child] {
			continue
		}
		if e.IsDir() {
			sha, ok, err := b.tree(child)
			if err != nil {
				return "", false, err
			}
			if !ok {
				continue
			}
			list = append(list, treeEntry{name, treeMode, sha})
			continue
		}
		// R25: the two rendered views, TODO.md and PROGRESS.md, are artefacts: every event in
		// the ledger rewrites them, so they are never part of a verified tree.
		if rel == "" && (name == queueView || name == recordView) {
			continue
		}
		sha, mode, err := b.blob(child)
		if err != nil {
			return "", false, err
		}
		list = append(list, treeEntry{name, mode, sha})
	}
	if len(list) == 0 {
		return "", false, nil
	}
	sortTree(list)
	var buf bytes.Buffer
	for _, e := range list {
		fmt.Fprintf(&buf, "%s %s\x00", e.mode, e.name)
		raw, err := hex.DecodeString(e.sha)
		if err != nil {
			return "", false, err
		}
		buf.Write(raw)
	}
	sha, err := gitStdin(b.root, "tree", buf.Bytes(), b.write)
	if err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// blob hashes one file with `git hash-object` and names the mode git stores it under. A
// symbolic link is stored as the blob of its target.
func (b *treeBuilder) blob(rel string) (string, string, error) {
	full := filepath.Join(b.root, filepath.FromSlash(rel))
	st, err := os.Lstat(full)
	if err != nil {
		return "", "", err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", "", err
		}
		sha, err := gitStdin(b.root, "blob", []byte(target), b.write)
		if err != nil {
			return "", "", err
		}
		return sha, linkMode, nil
	}
	if !st.Mode().IsRegular() {
		return "", "", refuse("%s is not a regular file — git cannot store it", rel)
	}
	mode := fileMode
	if st.Mode().Perm()&0o100 != 0 {
		mode = execMode
	}
	out, code := git(b.root, "hash-object", "--", rel)
	if code != 0 {
		return "", "", refuse("git hash-object %s: exit %d", rel, code)
	}
	sha := strings.TrimSpace(out)
	if b.write {
		// leave the blob in the store, so that git can diff two trees
		if wout, wcode := git(b.root, "hash-object", "-w", "--", rel); wcode == 0 {
			sha = strings.TrimSpace(wout)
		}
	}
	return sha, mode, nil
}

// sortTree orders the entries of a tree the way git does: by name, and a directory is compared
// as if its name ended in a slash, so "foo.bar" sorts before the directory "foo".
func sortTree(list []treeEntry) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		n := len(a.name)
		if len(b.name) < n {
			n = len(b.name)
		}
		if c := strings.Compare(a.name[:n], b.name[:n]); c != 0 {
			return c < 0
		}
		ca, cb := byte(0), byte(0)
		if len(a.name) > n {
			ca = a.name[n]
		} else if a.mode == treeMode {
			ca = '/'
		}
		if len(b.name) > n {
			cb = b.name[n]
		} else if b.mode == treeMode {
			cb = '/'
		}
		return ca < cb
	})
}

// gitStdin hashes data as a git object of the given type WITHOUT writing it: `git hash-object
// -t <type> --stdin` computes the id and leaves the object store alone. With write, the object
// is left in the store, so that git can read it back — the round's diff is taken between two
// trees, and both have to be objects git can find. Outside a git repository the write fails
// and the id is computed without it; the id is the same either way.
func gitStdin(root, typ string, data []byte, write bool) (string, error) {
	if write {
		sha, err := hashStdin(root, typ, data, true)
		if err == nil {
			return sha, nil
		}
	}
	return hashStdin(root, typ, data, false)
}

func hashStdin(root, typ string, data []byte, write bool) (string, error) {
	args := []string{"hash-object", "-t", typ}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "--stdin")
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git hash-object -t %s: exit %d: %s", typ, ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
