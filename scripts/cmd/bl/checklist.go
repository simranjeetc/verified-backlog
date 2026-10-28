package main

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ── check list ───────────────────────────────────────────────────────────────

var checkColumns = []string{"id", "decider", "severity", "brief_ref", "cmd", "expect", "fail_if", "why_not_mech", "criterion"}

var placeholderReasons = map[string]bool{
	"tbd": true, "todo": true, "n/a": true, "none": true, "because": true, "because it is judgment": true,
}

// checkRow is one row of a check list, its cells keyed by column name.
type checkRow struct {
	line int
	f    map[string]string
}

// parseChecks reads a check list by column name (§2). It enforces the header and the field count
// of R1; validateChecks enforces the rest.
func parseChecks(text string) ([]checkRow, error) {
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return nil, refuseRule("R1", "the check list is empty")
	}
	lines := strings.Split(text, "\n")
	header := strings.Split(strings.TrimRight(lines[0], "\r"), "\t")
	known := map[string]bool{}
	for _, c := range checkColumns {
		known[c] = true
	}
	seen := map[string]bool{}
	for _, h := range header {
		switch {
		case !known[h]:
			return nil, refuseRule("R1", "the header names an unknown column %q; it must name exactly: %s", h, strings.Join(checkColumns, ", "))
		case seen[h]:
			return nil, refuseRule("R1", "the header names column %q twice", h)
		}
		seen[h] = true
	}
	if len(seen) != len(checkColumns) {
		var missing []string
		for _, c := range checkColumns {
			if !seen[c] {
				missing = append(missing, c)
			}
		}
		return nil, refuseRule("R1", "the header is missing column(s): %s", strings.Join(missing, ", "))
	}
	var rows []checkRow
	for n, raw := range lines[1:] {
		raw = strings.TrimRight(raw, "\r")
		line := n + 2
		if strings.TrimSpace(raw) == "" {
			return nil, refuseRule("R1", "line %d is empty", line)
		}
		cells := strings.Split(raw, "\t")
		if len(cells) != len(header) {
			return nil, refuseRule("R1", "line %d has %d fields, want %d", line, len(cells), len(header))
		}
		r := checkRow{line: line, f: map[string]string{}}
		for i, h := range header {
			r.f[h] = strings.TrimSpace(cells[i])
		}
		rows = append(rows, r)
	}
	if len(rows) == 0 {
		return nil, refuseRule("R1", "the check list has a header and no rows")
	}
	return rows, nil
}

// validateChecks applies the LIST's own rules to every row, in the order §4 gives: R1 (the
// schema), then R34 (the command's dialect), then R4 (the amend scope), then R2 (the brief
// clause), then R3 (the judge's justification), then R8 (the expectation form). A rule is
// evaluated over the whole list before the next rule is, so the refusal names the earliest
// broken rule and not the first broken row.
//
// The brief is read at R2's place: it is re-hashed (R20) and its clauses are what a row's
// brief_ref may cite. amend says the list is an amendment of the frozen one — the scope of an
// amendment is R4's, and it is checked between R1 and R2. The frozen list's hash is returned so
// the caller can record it as the previous hash of the amendment.
//
// This is the one sweep over a list: `bl checks` and `bl rebaseline` both use it, so the two
// commands cannot drift apart on what a list may say. format is the format the event will
// carry: R34 and R8 are the format-1 rules, and a format-2 `decider: bl` row is R37's.
func validateChecks(root string, it *Item, rows []checkRow, amend bool, format int) (string, error) {
	if err := validateR1(rows); err != nil {
		return "", err
	}
	// R34: the command's dialect. A cmd is a script run by /bin/sh -c, so a pipe in it is a
	// pipe; a pipe with an odd number of backslashes before it is read by the shell as a
	// literal pipe, and the row then does not do what it says. The list is refused here, before
	// it is frozen, so the mistake cannot reach a round.
	if err := validateR34(rows, format); err != nil {
		return "", err
	}
	prevSHA := ""
	if amend {
		prev, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
		if err != nil {
			return "", err
		}
		old, err := parseChecks(string(prev))
		if err != nil {
			return "", err
		}
		if _, err := checkAmendment(old, rows); err != nil {
			return "", err
		}
		prevSHA = it.ChecksSHA
	}
	if it.Brief == "" {
		return "", refuseRule("R2", "%s has no brief to cite — it was opened before bl wrote briefs", it.ID)
	}
	brief, err := verifyArtefact(root, it.Brief, it.BriefSHA, "brief")
	if err != nil {
		return "", err
	}
	if err := validateR2(rows, briefClauses(string(brief))); err != nil {
		return "", err
	}
	if err := validateR3(rows); err != nil {
		return "", err
	}
	if err := validateR8(rows, format); err != nil {
		return "", err
	}
	return prevSHA, nil
}

// validateR1 applies R1 — the row schema — to every row: the id, the severity, the command, and
// the fields the row's decider requires. The FORM of an expectation is R8's business and the
// judge's justification is R3's, so neither is refused here.
func validateR1(rows []checkRow) error {
	ids := map[string]bool{}
	for _, r := range rows {
		id := r.f["id"]
		switch {
		case !rowIDRe.MatchString(id):
			return refuseRule("R1", "line %d: id %q does not match %s", r.line, id, rowIDRe.String())
		case strings.EqualFold(id, "run") || strings.EqualFold(id, "tree"):
			return refuseRule("R1", "line %d: id %q is reserved", r.line, id)
		case ids[id]:
			return refuseRule("R1", "line %d: id %q is not unique", r.line, id)
		}
		ids[id] = true
		if s := r.f["severity"]; s != "blocking" && s != "advisory" {
			return refuseRule("R1", "row %s: severity %q must be blocking or advisory", id, s)
		}
		if r.f["cmd"] == "" {
			return refuseRule("R1", "row %s: cmd is empty — every row runs a command", id)
		}
		switch r.f["decider"] {
		case "bl":
			if r.f["expect"] == "" {
				return refuseRule("R1", "row %s: a decider bl row needs a non-empty expect", id)
			}
			if r.f["fail_if"] != "" || r.f["why_not_mech"] != "" {
				return refuseRule("R1", "row %s: a decider bl row must have empty fail_if and why_not_mech", id)
			}
		case "judge":
			if r.f["expect"] != "" {
				return refuseRule("R1", "row %s: a decider judge row must have an empty expect", id)
			}
			// R1 owns the row schema: a judge row needs a non-empty fail_if. R3 owns the
			// justification — the placeholder and the too-short why_not_mech.
			if r.f["fail_if"] == "" {
				return refuseRule("R1", "row %s: a judge row needs a non-empty fail_if", id)
			}
		default:
			return refuseRule("R1", "row %s: decider %q must be bl or judge", id, r.f["decider"])
		}
	}
	return nil
}

// validateR34 applies R34 — a command is a shell script, and a pattern is not a shell — to
// every row. A cmd is run by /bin/sh -c, so a pipe in it is a pipe; a pipe is ESCAPED when an
// odd number of backslashes precedes it, because the shell then reads it as a literal pipe, so
// the row does not do what it says and the list is refused before it can reach a round.
//
// The check counts the backslashes rather than looking for the pair: an even number of
// backslashes before a pipe is an escaped backslash followed by a real pipe, and that row is
// fine.
//
// Only the cmd field is checked. An expect pattern is Go's RE2, where a backslash before a
// pipe means a literal pipe — and there that is what the pattern says, not a fault.
//
// R34 does not reach a format-2 `decider: bl` row (I89, B10): a format-2 test row holds no
// shell syntax, and a format-2 shell row is governed by B6's simple-command scan instead.
func validateR34(rows []checkRow, format int) error {
	for _, r := range rows {
		if format == 2 && r.f["decider"] == "bl" {
			continue
		}
		if n, ok := escapedPipe(r.f["cmd"]); ok {
			return refuseRule("R34", "row %s: the command holds an odd number of backslashes (%d) before a pipe — the shell reads that as a literal pipe; write the pipe as a pipe",
				r.f["id"], n)
		}
	}
	return nil
}

// escapedPipe reports the number of backslashes immediately before the first pipe the shell
// would read as a literal pipe, and whether there is one at all. It reads the command the way
// the shell does: inside a single-quoted or double-quoted string a pipe is text the pattern
// carries, not a pipe, so it is ignored there along with any backslash before it. Outside quotes
// a pipe is ESCAPED when an ODD number of backslashes precedes it: the shell reads `\|` as a
// literal pipe. An EVEN number is an escaped backslash followed by a real pipe, and a pipe with
// no backslash before it is a real pipe too — so only the odd count is a fault.
func escapedPipe(cmd string) (int, bool) {
	var quote byte // 0 outside quotes, else the quote that is open
	run := 0       // backslashes immediately before this character, outside quotes
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++ // a backslash in double quotes escapes the next character
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			run++
			continue
		case (c == '\'' || c == '"') && run%2 == 0:
			quote = c
		case c == '|' && run%2 == 1:
			return run, true
		}
		run = 0
	}
	return 0, false
}

// validateR2 applies R2 — a blocking row cites the brief — to every row.
func validateR2(rows []checkRow, clauses map[string]bool) error {
	for _, r := range rows {
		id, ref := r.f["id"], r.f["brief_ref"]
		if ref == "" && r.f["severity"] == "blocking" {
			return refuseRule("R2", "row %s: a blocking row needs a brief_ref naming a clause in the brief", id)
		}
		if ref != "" && !clauses[ref] {
			return refuseRule("R2", "row %s: brief_ref %q names no clause in the brief", id, ref)
		}
	}
	return nil
}

// validateR3 applies R3 — a judge row justifies itself — to every row.
func validateR3(rows []checkRow) error {
	for _, r := range rows {
		if r.f["decider"] != "judge" {
			continue
		}
		id, why := r.f["id"], r.f["why_not_mech"]
		if why == "" {
			return refuseRule("R3", "row %s: a judge row needs a non-empty why_not_mech", id)
		}
		if placeholderReasons[strings.ToLower(why)] {
			return refuseRule("R3", "row %s: why_not_mech %q is a placeholder, not a reason", id, why)
		}
		if len([]rune(why)) < 12 {
			return refuseRule("R3", "row %s: why_not_mech %q is shorter than 12 characters", id, why)
		}
	}
	return nil
}

// validateR8 applies R8 — an expectation is one of five forms — to every row of a format-1
// list. R8's text is unchanged, but it no longer applies to a format-2 list: there a
// `decider: bl` row's expect is R37's (I89, B10), and a judge row carries no expect at all.
func validateR8(rows []checkRow, format int) error {
	if format != 1 {
		return nil
	}
	for _, r := range rows {
		if r.f["decider"] != "bl" {
			continue
		}
		if err := checkExpect(r.f["id"], r.f["expect"]); err != nil {
			return err
		}
	}
	return nil
}

// checkExpect accepts exactly the five forms of R8.
func checkExpect(id, expect string) error {
	switch {
	case strings.HasPrefix(expect, "exit "):
		code := strings.TrimPrefix(expect, "exit ")
		if n, err := strconv.Atoi(code); err != nil || !exitCodeRe.MatchString(code) || n > 255 {
			return refuseRule("R8", "row %s: `exit` takes an integer 0-255, not %q", id, code)
		}
		return nil
	case strings.HasPrefix(expect, "stdout matches "), strings.HasPrefix(expect, "stderr matches "):
		re := expect[len("stdout matches "):]
		if re == "" {
			return refuseRule("R8", "row %s: %q has an empty regex", id, expect)
		}
		if _, err := regexp.Compile(re); err != nil {
			return refuseRule("R8", "row %s: regex %q is not valid RE2: %v", id, re, err)
		}
		return nil
	case strings.HasPrefix(expect, "file exists "):
		path := strings.TrimPrefix(expect, "file exists ")
		if path == "" || filepath.IsAbs(path) || strings.HasPrefix(path, "/") || strings.Contains(path, "..") {
			return refuseRule("R8", "row %s: `file exists` takes a non-empty relative path without .., not %q", id, path)
		}
		return nil
	case strings.HasPrefix(expect, "tree == "):
		hash := strings.TrimPrefix(expect, "tree == ")
		if !treeHashRe.MatchString(hash) {
			return refuseRule("R8", "row %s: `tree ==` takes a 40-character lowercase hex tree, not %q", id, hash)
		}
		return nil
	}
	return refuseRule("R8", "row %s: expect %q is not one of: exit <code>, stdout matches <regex>, stderr matches <regex>, file exists <path>, tree == <hash>", id, expect)
}

// checkAmendment allows exactly the three edits of R4: judge → bl, filling an empty brief_ref on
// an advisory row, and ADDING a row at the end of the list. old and next are both already valid
// lists, so an added row's id is already known not to repeat one the list holds (R1). It reports
// whether the amendment CHANGES a row already in the list: the claims freeze the list's content
// (R5), so only an amendment that changes a row is refused after them.
func checkAmendment(old, next []checkRow) (bool, error) {
	if len(next) < len(old) {
		return false, refuseRule("R4", "an amendment never removes a row (%d rows, was %d)", len(next), len(old))
	}
	changes := false
	for i := range old {
		o, n := old[i].f, next[i].f
		if o["id"] != n["id"] {
			return false, refuseRule("R4", "row %d: an amendment never changes or reorders ids (%s, was %s)", i+1, n["id"], o["id"])
		}
		id := o["id"]
		for _, col := range []string{"severity", "cmd", "criterion"} {
			if o[col] != n[col] {
				return false, refuseRule("R4", "row %s: an amendment never changes %s", id, col)
			}
		}
		switch {
		case o["decider"] == n["decider"]:
			for _, col := range []string{"expect", "fail_if", "why_not_mech"} {
				if o[col] != n[col] {
					return false, refuseRule("R4", "row %s: %s may change only when the row is reclassified from judge to bl", id, col)
				}
			}
		case o["decider"] == "judge" && n["decider"] == "bl":
			changes = true
		default:
			return false, refuseRule("R4", "row %s: the only reclassification an amendment may make is judge → bl", id)
		}
		if o["brief_ref"] != n["brief_ref"] {
			if o["brief_ref"] != "" || o["severity"] != "advisory" {
				return false, refuseRule("R4", "row %s: an amendment may only fill an empty brief_ref on an advisory row", id)
			}
			changes = true
		}
	}
	if !changes && len(next) == len(old) {
		return false, refuseRule("R4", "the amendment changes nothing")
	}
	return changes, nil
}

// amendGuards applies the state rules of an amendment once its scope is known: R5 (the claims
// freeze the list's content, so an amendment that changes a row is refused after them — adding
// a row is not), then R4 (an amendment is refused once the item has a PASS round). R5 comes
// first, so an amendment that changes a row says R5 even after a PASS round.
func amendGuards(root string, it *Item, rows []checkRow) error {
	prev, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	old, err := parseChecks(string(prev))
	if err != nil {
		return err
	}
	changes, err := checkAmendment(old, rows)
	if err != nil {
		return err
	}
	if changes && it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the check list's content is frozen; only an amendment that adds a row is allowed", it.ID)
	}
	for _, r := range it.attemptRounds() {
		if r.Verdict == "PASS" {
			return refuseRule("R4", "%s already has a PASS round — an amendment is refused once the item has passed", it.ID)
		}
	}
	return nil
}
