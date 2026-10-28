package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ── the round's rules ────────────────────────────────────────────────────────

// computedVerdicts reads the row-verdicts the binary computed for the `decider: bl` rows of a
// run (R7). They are computed by `bl run`, from each row's expectation and the values the run
// observed, and they are the row-verdicts of record: the judge's word for a binary row is not
// the row's verdict, and the round's derived verdict does not rest on it.
//
// A judge row has none: its row-verdict is the judge's, and it comes from the verdicts file.
func computedVerdicts(frozen []checkRow, rf *runFile) (map[string]string, error) {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	out := map[string]string{}
	for _, row := range frozen {
		if row.f["decider"] != "bl" {
			continue
		}
		id := row.f["id"]
		rec, ok := observed[id]
		if !ok {
			return nil, refuseRule("R6", "the run has no record of row %s — the round cannot take its row-verdict from the run", id)
		}
		if rec.Verdict != "PASS" && rec.Verdict != "FAIL" {
			return nil, refuseRule("R6", "the run carries no computed row-verdict for row %s — the row-verdict is the binary's, and it is computed by `bl run`", id)
		}
		out[id] = rec.Verdict
	}
	return out, nil
}

// rowVerdict is a row's row-verdict: the binary's computed verdict for a `decider: bl` row
// (R7), and the judge's for a `decider: judge` row.
func rowVerdict(id string, frozen []checkRow, vf *verdictsFile, computed map[string]string) string {
	for _, row := range frozen {
		if row.f["id"] != id {
			continue
		}
		if row.f["decider"] == "bl" {
			return computed[id]
		}
		break
	}
	return vf.rows[id].verdict
}

// checkAnchors is R9: a judge row is anchored, and only the form the anchor is written in is
// checked. An output anchor's text must appear in the row's captured stdout or stderr; a
// source anchor's file must be in the item's scope, its line must exist, its text must be at
// that line, and the line must lie in a hunk of the round's diff.
func checkAnchors(root string, it *Item, vf *verdictsFile, frozen []checkRow, rf *runFile, hunks []hunk) error {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	for _, row := range frozen {
		if row.f["decider"] != "judge" {
			continue
		}
		id := row.f["id"]
		v := vf.rows[id]
		if strings.TrimSpace(v.anchor) == "" {
			return refuseRule("R9", "row %s is a judge row: its anchor must not be empty", id)
		}
		a, err := parseAnchor(id, v.anchor)
		if err != nil {
			return err
		}
		switch a.form {
		case anchorOutput:
			rec, ok := observed[id]
			if !ok {
				return refuseRule("R9", "row %s: the run has no record of it", id)
			}
			if !strings.Contains(rec.Stdout, a.text) && !strings.Contains(rec.Stderr, a.text) {
				return refuseRule("R9", "row %s: the anchor's text %q is not in the row's captured output", id, a.text)
			}
		case anchorSource:
			if !inScope(it.Scope, a.file) {
				return refuseRule("R9", "row %s: the anchor names %s, which is not in the item's scope", id, a.file)
			}
			line, ok := sourceLine(root, a.file, a.line)
			if !ok {
				return refuseRule("R9", "row %s: %s has no line %d", id, a.file, a.line)
			}
			if !strings.Contains(line, a.text) {
				return refuseRule("R9", "row %s: the anchor's text %q is not at %s:%d", id, a.text, a.file, a.line)
			}
			if !inHunk(hunks, a.file, a.line) {
				return refuseRule("R9", "row %s: %s:%d lies in no hunk of the round's diff", id, a.file, a.line)
			}
		}
	}
	return nil
}

// sourceLine reads one line of a file of the verified tree — the working tree, which is what
// the verified tree is computed from.
func sourceLine(root, file string, line int) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if line < 1 || line > len(lines) {
		return "", false
	}
	return lines[line-1], true
}

// inHunk reports whether a line lies in the new-side range of a hunk of the given file.
func inHunk(hunks []hunk, file string, line int) bool {
	for _, h := range hunks {
		if h.file == file && h.contains(line) {
			return true
		}
	}
	return false
}

// testCovers reads the frozen test file and returns, per test row id, the repository paths its
// function's `@_covers(...)` decorator names (I89, B7). A test row covers a hunk or a bare path
// through these, never through its cmd. The file parsed when it was frozen and B8 re-hashes it
// at the top of the round, so a parse failure here is a defect of the frozen file and is
// refused under R37 rather than silently covering nothing.
func testCovers(root string, it *Item, frozen []checkRow) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if it.ChecksFormat != 2 || it.TestsSHA == "" {
		return out, nil
	}
	tf, err := parseTestFile(root, it.Tests)
	if err != nil {
		return nil, err
	}
	for _, r := range frozen {
		name, ok := testRowName(it.ChecksFormat, it, r)
		if !ok {
			continue
		}
		paths := map[string]bool{}
		for _, p := range tf.defs[name].covers {
			paths[p] = true
		}
		out[r.f["id"]] = paths
	}
	return out, nil
}

// checkCoverage is R10: every hunk in the round's diff must be covered by at least one
// blocking row. A row covers a hunk if it is a judge row whose source anchor lies in the
// hunk's new-side range, or if its cmd contains the hunk's file path as a literal substring.
// A format-2 test row covers a hunk only through its function's `@_covers(...)` (I89, B7):
// its cmd is `python3 <test file> <name>`, which names no repository file, and it is never
// read as a path by substring. Advisory rows do not count.
func checkCoverage(vf *verdictsFile, frozen []checkRow, hunks []hunk, bare []string, covers map[string]map[string]bool) error {
	for _, h := range hunks {
		if coveredHunk(vf, frozen, h, covers) {
			continue
		}
		return refuseRule("R10", "the hunk in %s at line %d is covered by no blocking row", h.file, h.start)
	}
	for _, f := range bare {
		if coveredPath(frozen, f, covers) {
			continue
		}
		return refuseRule("R10", "the change in %s is covered by no blocking row", f)
	}
	return nil
}

// coveredHunk reports whether a blocking row covers a hunk. covers holds, for each test row,
// the paths its function's `@_covers(...)` names; a test row's cmd covers nothing.
func coveredHunk(vf *verdictsFile, frozen []checkRow, h hunk, covers map[string]map[string]bool) bool {
	for _, row := range frozen {
		if row.f["severity"] != "blocking" {
			continue
		}
		if paths, ok := covers[row.f["id"]]; ok {
			if paths[h.file] {
				return true
			}
			continue
		}
		if strings.Contains(row.f["cmd"], h.file) {
			return true
		}
		if row.f["decider"] != "judge" {
			continue
		}
		a, err := parseAnchor(row.f["id"], vf.rows[row.f["id"]].anchor)
		if err != nil {
			continue
		}
		if a.form == anchorSource && a.file == h.file && h.contains(a.line) {
			return true
		}
	}
	return false
}

// coveredPath reports whether a blocking row names a file path in its cmd. A format-2 test row
// names the path in its `@_covers(...)` instead (I89, B7).
func coveredPath(frozen []checkRow, path string, covers map[string]map[string]bool) bool {
	for _, row := range frozen {
		if row.f["severity"] != "blocking" {
			continue
		}
		if paths, ok := covers[row.f["id"]]; ok {
			if paths[path] {
				return true
			}
			continue
		}
		if strings.Contains(row.f["cmd"], path) {
			return true
		}
	}
	return false
}

// checkR14 is R14: every advisory row whose row-verdict is FAIL must already be filed as a
// backlog item, with the row id appearing in the item's text as a whitespace-delimited token,
// and that item being the most recently filed item mentioning the id.
//
// The row-verdict of a `decider: bl` row is the one the binary computed (R7), so an advisory
// binary row that failed on its own values has to be filed too.
func checkR14(vf *verdictsFile, frozen []checkRow, computed map[string]string, order []*Item) error {
	for _, row := range frozen {
		if row.f["severity"] != "advisory" {
			continue
		}
		id := row.f["id"]
		if rowVerdict(id, frozen, vf, computed) != "FAIL" {
			continue
		}
		latest := latestMention(order, id)
		if latest == nil {
			return refuseRule("R14", "the advisory row %s failed and is not filed as an item yet — file it with `bl file --id <id> --text \"... %s ...\"`", id, id)
		}
		if !hasToken(latest.Text, id) {
			return refuseRule("R14", "the advisory row %s failed: the most recently filed item mentioning it (%s) does not name %s as a whole token", id, latest.ID, id)
		}
	}
	return nil
}

// latestMention returns the most recently filed item whose text mentions the id at all.
func latestMention(order []*Item, id string) *Item {
	var latest *Item
	for _, it := range order {
		if !strings.Contains(it.Text, id) {
			continue
		}
		if latest == nil || it.FiledAt >= latest.FiledAt {
			latest = it
		}
	}
	return latest
}

// hasToken reports whether the text holds the token as a whole word.
func hasToken(text, token string) bool {
	for _, f := range strings.Fields(text) {
		if f == token {
			return true
		}
	}
	return false
}

// checkR16 is R16: a second check that disagrees with the row's recorded verdict refuses the
// round. A second-check event computed against an earlier run is stale and is ignored.
//
// The row's recorded verdict is the binary's computed verdict for a `decider: bl` row (R7) and
// the judge's for a `decider: judge` row.
func checkR16(root string, it *Item, vf *verdictsFile, frozen []checkRow, computed map[string]string, runID string) error {
	for _, ev := range it.SecondChecks {
		if ev.Run != runID {
			continue
		}
		if _, ok := vf.rows[ev.Row]; !ok {
			continue
		}
		recorded := rowVerdict(ev.Row, frozen, vf, computed)
		if ev.Computed == recorded {
			continue
		}
		// A misclassified event is the one kind of refusal that writes to the ledger: it is
		// not a round and it consumes no code round (R16).
		if _, err := appendEvent(root, Event{
			T: "misclassified", ID: it.ID, Row: ev.Row, Computed: ev.Computed, Run: ev.Run,
			Cmd: ev.Cmd, Expect: ev.Expect,
		}); err != nil {
			return err
		}
		return refuseRule("R16", "the second check computed %s for row %s and the verdicts file records %s — the row is misclassified", ev.Computed, ev.Row, recorded)
	}
	return nil
}

// checkR28 is R28: an ignored file under the item's scope may not change, unless the item
// declared it as one it may write.
func checkR28(root string, it *Item) error {
	if len(it.IgnoredBefore) == 0 {
		return nil
	}
	want := map[string]string{}
	for _, entry := range it.IgnoredBefore {
		if i := strings.Index(entry, "\t"); i > 0 {
			want[entry[:i]] = entry[i+1:]
		}
	}
	now, err := ignoredUnderScope(root, it.Scope)
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, entry := range now {
		if i := strings.Index(entry, "\t"); i > 0 {
			got[entry[:i]] = entry[i+1:]
		}
	}
	for path, sha := range want {
		if got[path] == sha {
			continue
		}
		if scopeIgnored(it, path) {
			continue
		}
		return refuseRule("R28", "the ignored file %s changed under the item's scope — the item did not declare it as one it may write", path)
	}
	return nil
}

// ── R25: the artefacts ───────────────────────────────────────────────────────

// trackedArtefact says whether the repository tracks the artefact directory `.backlog/`. The
// artefacts are never part of a verified tree, and a repository that tracks them is a repository
// error.
func trackedArtefact(root string) bool {
	out, code := git(root, "ls-files", "--", backlogDir)
	return code == 0 && strings.TrimSpace(out) != ""
}

// trackedArtefactMessage is the one sentence R25 uses about a repository that tracks `.backlog/`:
// it is said wherever a tree is computed, and it is what the round refuses under.
func trackedArtefactMessage() string {
	return fmt.Sprintf("the %s directory is tracked in this repository — it is an artefact directory and must never be tracked", backlogDir)
}

// artefactReported remembers the repositories whose tracked artefacts have already been said, so
// that a command that computes several trees says it once.
var artefactReported = struct {
	sync.Mutex
	reported map[string]bool
}{reported: map[string]bool{}}

// reportTrackedArtefact says so when `.backlog/` is tracked: the artefacts are excluded from every
// verified tree, and a repository that tracks them is a repository error (R25). It is a report and
// not a refusal — the command that computed the tree carries on and writes its evidence as usual.
func reportTrackedArtefact(root string) {
	if !trackedArtefact(root) {
		return
	}
	artefactReported.Lock()
	said := artefactReported.reported[root]
	artefactReported.reported[root] = true
	artefactReported.Unlock()
	if said {
		return
	}
	fmt.Fprintf(os.Stderr, "bl: %s\n", trackedArtefactMessage())
}

// checkR25 is R25: `.backlog/` is never part of a verified tree, and a repository that tracks it
// is a repository error — so the round refuses rather than comparing trees that include it.
func checkR25(root string) error {
	if !trackedArtefact(root) {
		return nil
	}
	return refuseRule("R25", "%s", trackedArtefactMessage())
}

// ── the evidence file ────────────────────────────────────────────────────────

// evidenceText writes the evidence file of a round (§2): one line per frozen check id, in the
// order the list was frozen, with the run's observed values — so the claim that a command ran
// is never something a model typed into a cell.
//
// The row_verdict column holds the row's row-verdict: the binary's computed verdict for a
// `decider: bl` row (R7) and the judge's for a `decider: judge` row.
func evidenceText(vf *verdictsFile, frozen []checkRow, rf *runFile, computed map[string]string) string {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	var b strings.Builder
	b.WriteString("id\tcmd\texit\tsignalled\ttimed_out\tobserved\texcerpt\trow_verdict\n")
	for _, row := range frozen {
		id := row.f["id"]
		rec := observed[id]
		v := vf.rows[id]
		seen := fmt.Sprintf("stdout sha %s, stderr sha %s", rec.StdoutSHA, rec.StderrSHA)
		if v.anchor != "" {
			seen += "; anchor " + v.anchor
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\t%t\t%t\t%s\t%s\t%s\n",
			id, rec.Cmd, rec.Exit, rec.Signalled, rec.TimedOut, seen, excerpt(rec), rowVerdict(id, frozen, vf, computed))
	}
	return b.String()
}

// excerpt is the human-readable excerpt of a row's output: the first 4096 bytes of each
// stream, with the control characters escaped so the evidence file stays one line per row.
func excerpt(rec runRow) string {
	out := flattenStream(rec.Stdout, rec.StdoutBinary)
	if rec.Stderr != "" {
		if out != "" {
			out += " | "
		}
		out += flattenStream(rec.Stderr, rec.StderrBinary)
	}
	return out
}

func flattenStream(s string, binary bool) string {
	if binary {
		return "(binary output)"
	}
	const limit = 4096
	if len(s) > limit {
		s = s[:limit]
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── the expectation (R8) ─────────────────────────────────────────────────────

// expectVerdict computes a row's verdict from the observed values and an expectation of R8's
// five forms. It is what `bl second-check` records (R16).
func expectVerdict(root, id, expect string, rec runRow, tree string) (string, error) {
	switch {
	case strings.HasPrefix(expect, "exit "):
		code := strings.TrimPrefix(expect, "exit ")
		n, err := strconv.Atoi(code)
		if err != nil {
			return "", refuseRule("R8", "row %s: `exit` takes an integer 0-255, not %q", id, code)
		}
		if rec.Exit == n {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "stdout matches "):
		re, err := regexp.Compile(expect[len("stdout matches "):])
		if err != nil {
			return "", refuseRule("R8", "row %s: regex %q is not valid RE2: %v", id, expect, err)
		}
		if matchStream(re, rec.Stdout) {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "stderr matches "):
		re, err := regexp.Compile(expect[len("stderr matches "):])
		if err != nil {
			return "", refuseRule("R8", "row %s: regex %q is not valid RE2: %v", id, expect, err)
		}
		if matchStream(re, rec.Stderr) {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "file exists "):
		path := strings.TrimPrefix(expect, "file exists ")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "tree == "):
		hash := strings.TrimPrefix(expect, "tree == ")
		if tree == hash {
			return "PASS", nil
		}
		return "FAIL", nil
	}
	return "", refuseRule("R8", "row %s: expect %q is not one of: exit <code>, stdout matches <regex>, stderr matches <regex>, file exists <path>, tree == <hash>", id, expect)
}

// matchStream also tries the captured stream with one final newline removed.
func matchStream(re *regexp.Regexp, s string) bool {
	return re.MatchString(s) || (strings.HasSuffix(s, "\n") && re.MatchString(s[:len(s)-1]))
}
