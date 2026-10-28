package main

import (
	"strconv"
	"strings"
)

// ── the verdicts file, the diff, and the rules the round path applies ─────────

// verdictColumns are the columns of the verdicts file (§2): the header names exactly these
// three, in any order, and the columns are read by name.
var verdictColumns = []string{"id", "row_verdict", "anchor"}

// verdictRow is one line of a verdicts file: the frozen id it decides, the verdict the judge
// recorded, and the anchor that supports it.
type verdictRow struct {
	id      string
	verdict string
	anchor  string
}

// verdictsFile is a parsed verdicts file: the rows it carries, the run and the tree it names,
// and the finding it raises.
type verdictsFile struct {
	rows    map[string]verdictRow
	order   []string
	run     string
	tree    string
	finding string
	found   bool
}

// parseVerdicts reads the verifier's verdicts file (§2). The header names exactly the three
// columns; the two lines after it are `run <run-id>` and `tree <40-hex>`; the rest are one
// line per frozen check id, and `#` lines are comments.
//
// The shape of the file is R6's: the ids must match the frozen list exactly, the run and the
// tree must be there, and a `decider: bl` row's anchor must be empty. A judge row's anchor is
// R9's, and is checked in checkAnchors.
func parseVerdicts(text string, frozen []checkRow) (*verdictsFile, error) {
	if strings.TrimSpace(text) == "" {
		return nil, refuseRule("R6", "the verdicts file is empty")
	}
	lines := strings.Split(strings.TrimRight(text, "\r\n"), "\n")
	header := strings.Split(strings.TrimRight(lines[0], "\r"), "\t")
	known := map[string]bool{}
	for _, c := range verdictColumns {
		known[c] = true
	}
	seen := map[string]bool{}
	for _, h := range header {
		switch {
		case !known[h]:
			return nil, refuseRule("R6", "the verdicts file names an unknown column %q; it must name exactly: %s", h, strings.Join(verdictColumns, ", "))
		case seen[h]:
			return nil, refuseRule("R6", "the verdicts file names column %q twice", h)
		}
		seen[h] = true
	}
	for _, c := range verdictColumns {
		if !seen[c] {
			return nil, refuseRule("R6", "the verdicts file is missing column %q", c)
		}
	}
	deciders := map[string]string{}
	for _, r := range frozen {
		deciders[r.f["id"]] = r.f["decider"]
	}
	vf := &verdictsFile{rows: map[string]verdictRow{}}
	// The two metadata lines: `run <run-id>` and `tree <40-hex>`, in that order.
	meta := 0
	for n, raw := range lines[1:] {
		line := strings.TrimRight(raw, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			// a comment: not a row, and a `# finding:` line is read by R13.
			if strings.HasPrefix(trimmed, "# finding:") {
				vf.found = true
				vf.finding = strings.TrimSpace(strings.TrimPrefix(trimmed, "# finding:"))
			}
			continue
		}
		switch meta {
		case 0:
			rest, ok := strings.CutPrefix(trimmed, "run ")
			if !ok || strings.TrimSpace(rest) == "" {
				return nil, refuseRule("R6", "line %d: the verdicts file must name the run: `run <run-id>`", n+2)
			}
			vf.run = strings.TrimSpace(rest)
			meta = 1
			continue
		case 1:
			rest, ok := strings.CutPrefix(trimmed, "tree ")
			if !ok || !treeHashRe.MatchString(rest) {
				return nil, refuseRule("R6", "line %d: the verdicts file must name the tree: `tree <40-hex>`", n+2)
			}
			vf.tree = rest
			meta = 2
			continue
		}
		cells := strings.Split(line, "\t")
		if len(cells) != len(header) {
			return nil, refuseRule("R6", "line %d: the verdicts file has %d fields, want %d", n+2, len(cells), len(header))
		}
		f := map[string]string{}
		for i, h := range header {
			f[h] = strings.TrimSpace(cells[i])
		}
		id := f["id"]
		if id == "" {
			return nil, refuseRule("R6", "line %d: a verdicts row has no id", n+2)
		}
		if _, dup := vf.rows[id]; dup {
			return nil, refuseRule("R6", "line %d: row %s appears twice in the verdicts file", n+2, id)
		}
		decider, known := deciders[id]
		if !known {
			return nil, refuseRule("R6", "line %d: row %s is not in the frozen check list", n+2, id)
		}
		switch f["row_verdict"] {
		case "PASS", "FAIL":
		default:
			return nil, refuseRule("R6", "line %d: row %s: row_verdict %q must be PASS or FAIL", n+2, id, f["row_verdict"])
		}
		if decider == "bl" && f["anchor"] != "" {
			return nil, refuseRule("R6", "line %d: row %s is a decider bl row — its anchor must be empty", n+2, id)
		}
		vf.rows[id] = verdictRow{id: id, verdict: f["row_verdict"], anchor: f["anchor"]}
		vf.order = append(vf.order, id)
	}
	if meta < 2 {
		return nil, refuseRule("R6", "the verdicts file must name the run and the tree: `run <run-id>` then `tree <40-hex>`")
	}
	for _, r := range frozen {
		if _, ok := vf.rows[r.f["id"]]; !ok {
			return nil, refuseRule("R6", "the verdicts file has no row for %s — it must carry one line per frozen check id", r.f["id"])
		}
	}
	return vf, nil
}

// ── the anchors (R9) ─────────────────────────────────────────────────────────

const (
	anchorOutput = "output"
	anchorSource = "source"
)

// anchor is a parsed anchor: its form, the file and line of a source anchor, and the quoted
// text both forms carry.
type anchor struct {
	form string
	file string
	line int
	text string
}

// parseAnchor reads an anchor field (§2). The form is decided syntactically: an anchor whose
// text before the first `"` holds a `:` and a line number is a source anchor, and anything else
// is an output anchor. The quoted text is the content between the first `"` and the last one.
func parseAnchor(id, field string) (anchor, error) {
	first := strings.Index(field, "\"")
	last := strings.LastIndex(field, "\"")
	if first < 0 {
		return anchor{}, refuseRule("R9", "row %s: the anchor %q is not one of the two anchor forms — a source anchor is `file:line \"text\"` and an output anchor is `\"text\"`", id, field)
	}
	if first == last {
		return anchor{}, refuseRule("R9", "row %s: the anchor has no quoted text", id)
	}
	text, err := unquote(id, field[first+1:last])
	if err != nil {
		return anchor{}, err
	}
	if text == "" {
		return anchor{}, refuseRule("R9", "row %s: the anchor's quoted text is empty", id)
	}
	a := anchor{text: text}
	if file, line, ok := sourceAnchor(strings.TrimSpace(field[:first])); ok {
		a.form, a.file, a.line = anchorSource, file, line
		return a, nil
	}
	a.form = anchorOutput
	return a, nil
}

// sourceAnchor reads a source anchor's prefix: a file, a colon, and a line number. A prefix
// that is not that shape makes the anchor an output anchor.
func sourceAnchor(prefix string) (string, int, bool) {
	i := strings.LastIndex(prefix, ":")
	if i < 0 {
		return "", 0, false
	}
	file := strings.TrimSpace(prefix[:i])
	num := strings.TrimSpace(prefix[i+1:])
	if file == "" || num == "" {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	if err != nil || n < 1 {
		return "", 0, false
	}
	return file, n, true
}

// unquote reads the quoted text of an anchor: `\"` is a literal quote, `\\` a literal
// backslash, and an unescaped `"` inside the text is refused.
func unquote(id, body string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(body); i++ {
		c := body[i]
		if c != '\\' {
			if c == '"' {
				return "", refuseRule("R9", "row %s: the anchor's quoted text holds an unescaped quote", id)
			}
			b.WriteByte(c)
			continue
		}
		if i+1 >= len(body) {
			return "", refuseRule("R9", "row %s: the anchor's quoted text ends with a lone backslash", id)
		}
		i++
		switch body[i] {
		case '"':
			b.WriteByte('"')
		case '\\':
			b.WriteByte('\\')
		default:
			b.WriteByte('\\')
			b.WriteByte(body[i])
		}
	}
	return b.String(), nil
}
