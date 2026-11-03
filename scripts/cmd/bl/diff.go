package main

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ── the diff (R9, R10) ───────────────────────────────────────────────────────

// hunk is one contiguous block of changed lines in the round's diff (§2): its file, and its
// new-side range — the start line through the last line of the block. A deleted-only hunk has
// an empty new-side range.
type hunk struct {
	file  string
	start int
	count int
}

// contains reports whether a line lies in the hunk's new-side range.
func (h hunk) contains(line int) bool {
	return h.count > 0 && line >= h.start && line < h.start+h.count
}

// roundDiff computes the round's diff: the before-tree against the verified tree (§2). It
// returns the hunks and the files that changed with no textual hunk — a binary file, an empty
// file, a mode change — which only a blocking row naming the path can cover.
func roundDiff(root, before, after string) ([]hunk, []string) {
	if before == "" || after == "" {
		return nil, nil
	}
	out, code := git(root, "diff", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--diff-algorithm=myers", "--unified=0", before, after)
	if code != 0 {
		// the trees are not in the store, or this is not a git repository: there is no diff
		// to check.
		return nil, nil
	}
	var hunks []hunk
	var files []string
	seen := map[string]bool{}
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = diffPath(strings.TrimPrefix(line, "diff --git "))
			// The ledger's own artefacts are not part of the diff the round checks: the views
			// are rendered from the ledger, so they change on every append (R25).
			if isArtifact(cur) {
				cur = ""
				continue
			}
			if !seen[cur] {
				seen[cur] = true
				files = append(files, cur)
			}
		case strings.HasPrefix(line, "@@ "):
			start, count, ok := hunkRange(line)
			if ok && cur != "" {
				hunks = append(hunks, hunk{file: cur, start: start, count: count})
			}
		}
	}
	var bare []string
	for _, f := range files {
		if !fileHasHunk(hunks, f) {
			bare = append(bare, f)
		}
	}
	return hunks, bare
}

// fileHasHunk reports whether a file carries at least one hunk.
func fileHasHunk(hunks []hunk, file string) bool {
	for _, h := range hunks {
		if h.file == file {
			return true
		}
	}
	return false
}

// diffPath reads the file path out of a `diff --git a/<p> b/<p>` line. With --no-renames the
// two paths are the same, so the path is the middle of the line.
func diffPath(rest string) string {
	if strings.HasPrefix(rest, "\"") {
		_, tail, ok := cutQuoted(rest)
		if ok {
			second, _, _ := cutQuoted(tail)
			return strings.TrimPrefix(second, "b/")
		}
		return rest
	}
	if len(rest) < 5 {
		return rest
	}
	// the line is `a/<p> b/<p>`: with --no-renames the two paths are the same.
	if (len(rest)-5)%2 == 0 {
		p := (len(rest) - 5) / 2
		first := rest[2 : 2+p]
		second := rest[3+p:]
		if strings.HasPrefix(second, "b/") && strings.TrimPrefix(second, "b/") == first {
			return first
		}
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// cutQuoted reads one quoted string off the front of s and returns the rest.
func cutQuoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, "\"") {
		return "", s, false
	}
	end := strings.Index(s[1:], "\"")
	if end < 0 {
		return "", s, false
	}
	return s[1 : 1+end], strings.TrimSpace(s[2+end:]), true
}

// hunkRange reads the new-side range of a `@@ -a,b +c,d @@` line.
func hunkRange(line string) (int, int, bool) {
	i := strings.Index(line, "+")
	if i < 0 {
		return 0, 0, false
	}
	j := strings.Index(line[i:], " @@")
	if j < 0 {
		return 0, 0, false
	}
	spec := line[i+1 : i+j]
	count := 1
	if k := strings.Index(spec, ","); k >= 0 {
		count, _ = strconv.Atoi(spec[k+1:])
		spec = spec[:k]
	}
	start, err := strconv.Atoi(spec)
	if err != nil {
		return 0, 0, false
	}
	return start, count, true
}

// ── the scope (R28) ──────────────────────────────────────────────────────────

// inScope reports whether a repository-relative path is inside the item's scope. A scope path
// ending in `/` names every file under that directory; a path naming no file is still a scope
// entry, because a file that does not exist yet is in scope. An item with no scope is not
// constrained: the whole repository is in scope.
func inScope(scope []string, path string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, s := range scope {
		s = strings.TrimSuffix(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}

// scopeIgnored reports whether the item declared an ignored file as one it may write.
func scopeIgnored(it *Item, path string) bool {
	for _, s := range it.ScopeIgnored {
		s = strings.TrimSuffix(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}

// ignoredUnderScope lists the ignored files under the item's scope paths, with the sha256 of
// each: the before-state of R28. An ignored file is not in the verified tree, so a change to
// it is invisible to the diff — this is the list that makes it visible to the record.
func ignoredUnderScope(root string, scope []string) ([]string, error) {
	b := &treeBuilder{root: root, ignored: map[string]bool{}}
	if err := b.findIgnored(); err != nil {
		return nil, err
	}
	var out []string
	for path := range b.ignored {
		if isRenderedView(path) {
			continue
		}
		if !inScope(scope, path) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			continue
		}
		out = append(out, path+"\t"+shaHex(data))
	}
	sort.Strings(out)
	return out, nil
}

// isRenderedView reports whether path is one of the root-level views rewritten by ledger events.
func isRenderedView(path string) bool {
	return path == "TODO.md" || path == "PROGRESS.md"
}

// treesMatch reports whether two trees are the same tree, ignoring the ledger's own
// artefacts. The two views are rendered from the ledger, so they change on every append.
//
// The verified tree excludes the artefacts (§2, R25), so the comparison is the narrow one the
// design describes: a ledger event between the run and the round — R14 files the advisory items
// there — rewrites the views, and the tree the round verifies does not move.
func treesMatch(root, a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	out, code := git(root, "diff", "--no-renames", "--name-only", a, b)
	if code != 0 {
		// the trees cannot be compared: they are not the same tree.
		return false
	}
	for _, p := range strings.Fields(out) {
		if !isArtifact(p) {
			return false
		}
	}
	return true
}
