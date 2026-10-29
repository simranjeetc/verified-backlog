package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type intakeRecord struct {
	T         string   `json:"t"`
	ID        string   `json:"id"`
	Path      string   `json:"path"`
	Lines     string   `json:"lines"`
	SHA       string   `json:"sha"`
	Text      string   `json:"text"`
	By        string   `json:"by"`
	Title     string   `json:"title"`
	After     []string `json:"after"`
	Scope     []string `json:"scope"`
	Source    string   `json:"source"`
	Decisions []string `json:"decisions"`
	Item      string   `json:"item"`
	Observe   string   `json:"observe"`
	Expect    string   `json:"expect"`
	Example   string   `json:"example"`
	line      int
	keys      []string
	invalid   map[string]bool
}

type intakeError struct {
	line          int
	field, reason string
}

var intakeIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)

// intakeLineBreaks are the characters a viewer may render as a line break.
const intakeLineBreaks = "\r\n\u0085\u2028\u2029"

var intakeSHAPattern = regexp.MustCompile(`^[0-9a-fA-F]{40}$`)

// intakeFields lists every field each record type may carry.
var intakeFields = map[string][]string{"source": {"t", "id", "path", "lines", "sha"}, "decision": {"t", "id", "text", "by"}, "item": {"t", "id", "title", "after", "scope", "source", "decisions"}, "clause": {"t", "item", "id", "observe", "expect", "example"}}

func intakeSchema() map[string]any {
	fields := intakeFields
	arrays := map[string]bool{"after": true, "scope": true, "decisions": true}
	result := map[string]any{}
	for kind, names := range fields {
		required := names
		if kind == "item" {
			required = []string{"t", "id", "title"}
		}
		props := map[string]any{}
		for _, name := range names {
			typ := "string"
			if arrays[name] {
				typ = "array"
			}
			p := map[string]any{"type": typ}
			if typ == "array" {
				p["items"] = map[string]string{"type": "string"}
			}
			props[name] = p
		}
		result[kind] = map[string]any{"type": "object", "required": required, "properties": props, "additionalProperties": false}
	}
	return result
}

func cmdIntake(root string, a args) error {
	check, render := a.flag("check"), a.flag("render")
	path := a.get("file")
	if check == render || path == "" {
		return refuse("use --check or --render and --file <path>")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return refuse("--file %s: %v", path, err)
	}
	records, errs := parseIntake(path, string(data))
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	filed := map[string]bool{}
	for _, ev := range events {
		if ev.T == "file" {
			filed[ev.ID] = true
		}
	}
	errs = append(errs, validateIntake(path, records, filed)...)
	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool {
			if errs[i].line != errs[j].line {
				return errs[i].line < errs[j].line
			}
			return errs[i].field < errs[j].field
		})
		lines := make([]string, 0, len(errs))
		for _, e := range errs {
			lines = append(lines, fmt.Sprintf("%s:%d: %s: %s", path, e.line, e.field, e.reason))
		}
		return refuseRule("intake", "%s", strings.Join(lines, "\n"))
	}
	if check {
		counts := map[string]int{}
		for _, r := range records {
			counts[r.T]++
		}
		fmt.Printf("%d items · %d clauses · %d decision(s) · %d source(s)\n", counts["item"], counts["clause"], counts["decision"], counts["source"])
		return nil
	}
	renderIntake(records)
	return nil
}

func parseIntake(path, data string) ([]intakeRecord, []intakeError) {
	var records []intakeRecord
	var errs []intakeError
	s := bufio.NewScanner(strings.NewReader(data))
	s.Buffer(make([]byte, 4096), 4*1024*1024)
	line := 1
	for ; s.Scan(); line++ {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(s.Bytes(), &raw); err != nil || raw == nil {
			// The bad line still counts as a record, so validation knows the file is not clean.
			records = append(records, intakeRecord{line: line, invalid: map[string]bool{"json": true}})
			errs = append(errs, intakeError{line, "json", "not a JSON object"})
			continue
		}
		r := intakeRecord{invalid: map[string]bool{}}
		for key := range raw {
			r.keys = append(r.keys, key)
		}
		for key, dest := range map[string]any{"t": &r.T, "id": &r.ID, "path": &r.Path, "lines": &r.Lines, "sha": &r.SHA, "text": &r.Text, "by": &r.By, "title": &r.Title, "after": &r.After, "scope": &r.Scope, "source": &r.Source, "decisions": &r.Decisions, "item": &r.Item, "observe": &r.Observe, "expect": &r.Expect, "example": &r.Example} {
			if value, ok := raw[key]; ok {
				if !decodeIntakeValue(value, dest) {
					r.invalid[key] = true
					reason := "must be a string"
					if key == "after" || key == "scope" || key == "decisions" {
						reason = "must be an array of strings"
					}
					errs = append(errs, intakeError{line, key, reason})
				}
			}
		}
		r.line = line
		records = append(records, r)
	}
	if s.Err() != nil {
		errs = append(errs, intakeError{line, "json", "line too long to read"})
	}
	return records, errs
}

// decodeIntakeValue fills dest with a string or an array of strings; null, and a
// null or non-string element, is a wrong type rather than an absent value.
func decodeIntakeValue(value json.RawMessage, dest any) bool {
	var v any
	if json.Unmarshal(value, &v) != nil {
		return false
	}
	switch d := dest.(type) {
	case *string:
		s, ok := v.(string)
		*d = s
		return ok
	case *[]string:
		list, ok := v.([]any)
		if !ok {
			return false
		}
		out := make([]string, 0, len(list))
		for _, e := range list {
			s, ok := e.(string)
			if !ok {
				return false
			}
			out = append(out, s)
		}
		*d = out
		return true
	}
	return false
}

func validateIntake(path string, rs []intakeRecord, filed map[string]bool) []intakeError {
	var es []intakeError
	for i := range rs {
		r := &rs[i]
		for field, value := range map[string]string{"t": r.T, "id": r.ID, "path": r.Path, "lines": r.Lines, "sha": r.SHA, "text": r.Text, "by": r.By, "title": r.Title, "source": r.Source, "item": r.Item, "observe": r.Observe, "expect": r.Expect, "example": r.Example} {
			if !r.invalid[field] && strings.ContainsAny(value, intakeLineBreaks) {
				r.invalid[field] = true
				es = append(es, intakeError{r.line, field, "must be one line"})
			}
		}
		for field, values := range map[string][]string{"after": r.After, "scope": r.Scope, "decisions": r.Decisions} {
			for _, value := range values {
				if strings.ContainsAny(value, intakeLineBreaks) {
					if !r.invalid[field] {
						es = append(es, intakeError{r.line, field, "must be one line"})
					}
					r.invalid[field] = true
				}
			}
		}
		for field, value := range map[string]string{"id": r.ID, "item": r.Item} {
			if !r.invalid[field] && value != "" && !intakeIDPattern.MatchString(value) {
				r.invalid[field] = true
				es = append(es, intakeError{r.line, field, "must be letters, digits, '.', '_' or '-'"})
			}
		}
		for field, values := range map[string][]string{"after": r.After, "decisions": r.Decisions} {
			for _, value := range values {
				if !r.invalid[field] && !intakeIDPattern.MatchString(value) {
					r.invalid[field] = true
					es = append(es, intakeError{r.line, field, "must be letters, digits, '.', '_' or '-'"})
				}
			}
		}
	}
	seen := map[string]map[string]int{}
	items := map[string]int{}
	sourceIDs := map[string]int{}
	decisions := map[string]int{}
	clauses := map[string]int{}
	add := func(r intakeRecord, field, reason string) { es = append(es, intakeError{r.line, field, reason}) }
	for _, r := range rs {
		if r.invalid["t"] || r.invalid["json"] {
			continue
		}
		fields := map[string][]string{"source": {"t", "id", "path", "lines", "sha"}, "decision": {"t", "id", "text", "by"}, "item": {"t", "id", "title"}, "clause": {"t", "item", "id", "observe", "expect", "example"}}[r.T]
		if fields == nil {
			add(r, "t", "unknown record type")
			continue
		}
		for _, key := range r.keys {
			if !slices.Contains(intakeFields[r.T], key) {
				add(r, key, "unknown field for a "+r.T+" record")
			}
		}
		for _, f := range fields {
			var v string
			switch f {
			case "t":
				v = r.T
			case "id":
				v = r.ID
			case "path":
				v = r.Path
			case "lines":
				v = r.Lines
			case "sha":
				v = r.SHA
			case "text":
				v = r.Text
			case "by":
				v = r.By
			case "title":
				v = r.Title
			case "item":
				v = r.Item
			case "observe":
				v = r.Observe
			case "expect":
				v = r.Expect
			case "example":
				v = r.Example
			}
			if v == "" && !r.invalid[f] {
				r.invalid[f] = true
				add(r, f, "is required")
			}
		}
		if seen[r.T] == nil {
			seen[r.T] = map[string]int{}
		}
		dupKey := r.ID
		if r.T == "clause" {
			dupKey = r.Item + "/" + r.ID
		}
		if dupKey != "" && !r.invalid["id"] && !r.invalid["item"] {
			if seen[r.T][dupKey] != 0 {
				add(r, "id", "duplicate id")
			}
			seen[r.T][dupKey] = r.line
		}
		switch r.T {
		case "source":
			if !r.invalid["id"] {
				sourceIDs[r.ID] = r.line
			}
		case "item":
			if !r.invalid["id"] {
				items[r.ID] = r.line
			}
			if !r.invalid["id"] && filed[r.ID] && r.ID != "" {
				add(r, "id", "already filed in ledger")
			}
		case "decision":
			if !r.invalid["id"] {
				decisions[r.ID] = r.line
			}
		case "clause":
			clauses[r.Item]++
		}
		if r.T == "clause" && r.Example == "" && !r.invalid["example"] {
			add(r, "example", "must not be empty")
		}
		if r.T == "source" {
			if !r.invalid["sha"] && !intakeSHAPattern.MatchString(r.SHA) {
				add(r, "sha", "must be 40 hexadecimal characters")
			}
			parts := strings.Split(r.Lines, "-")
			if r.invalid["lines"] {
				continue
			}
			if len(parts) != 2 {
				add(r, "lines", "must be <start>-<end>")
			} else {
				a, e1 := strconv.Atoi(parts[0])
				b, e2 := strconv.Atoi(parts[1])
				if e1 != nil || e2 != nil || a < 1 || b < a {
					add(r, "lines", "must be <start>-<end> with start <= end")
				}
			}
		}
	}
	// A reference is not checked while a record it could name is itself bad,
	// so one bad record is never reported again as a broken reference in another.
	kinds := map[string]bool{"source": true, "decision": true, "item": true, "clause": true}
	tainted := map[string]bool{}
	for _, r := range rs {
		switch {
		case r.invalid["t"] || r.invalid["json"] || !kinds[r.T]:
			for k := range kinds {
				tainted[k] = true
			}
		case r.invalid["id"]:
			tainted[r.T] = true
		}
		if r.T == "clause" && r.invalid["item"] {
			tainted["clause"] = true
		}
	}
	for _, r := range rs {
		if r.T == "clause" && !tainted["item"] && !r.invalid["item"] && items[r.Item] == 0 {
			add(r, "item", "does not name an item in this intake")
		}
		if r.T == "item" {
			if !tainted["source"] && !r.invalid["source"] && r.Source != "" && sourceIDs[r.Source] == 0 {
				add(r, "source", "unknown source "+r.Source)
			}
			for _, d := range r.Decisions {
				if !tainted["decision"] && !r.invalid["decisions"] && decisions[d] == 0 {
					add(r, "decisions", "unknown decision "+d)
				}
			}
			for _, dep := range r.After {
				if !tainted["item"] && !r.invalid["after"] && items[dep] == 0 && !filed[dep] {
					add(r, "after", "unknown item "+dep)
				}
			}
			if !tainted["clause"] && !r.invalid["id"] && clauses[r.ID] == 0 {
				add(r, "id", "item has no clause")
			}
		}
	}
	state := map[string]int{}
	var visit func(string)
	visit = func(id string) {
		state[id] = 1
		for _, r := range rs {
			if r.T == "item" && r.ID == id {
				for _, d := range r.After {
					if items[d] == 0 {
						continue
					}
					if state[d] == 1 {
						add(r, "after", "dependency cycle")
					} else if state[d] == 0 {
						visit(d)
					}
				}
			}
		}
		state[id] = 2
	}
	for id := range items {
		if state[id] == 0 {
			visit(id)
		}
	}
	return es
}

func renderIntake(rs []intakeRecord) {
	escape := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	decisions := map[string]string{}
	for _, r := range rs {
		if r.T == "decision" {
			decisions[r.ID] = r.Text
		}
	}
	for _, r := range rs {
		if r.T != "item" {
			continue
		}
		fmt.Printf("## %s — %s\n\n", escape(r.ID), escape(r.Title))
		if len(r.After) > 0 {
			after := make([]string, len(r.After))
			for i, id := range r.After {
				after[i] = escape(id)
			}
			fmt.Printf("after: %s\n\n", strings.Join(after, ", "))
		}
		for _, d := range r.Decisions {
			fmt.Printf("Decision: %s\n\n", escape(decisions[d]))
		}
		for _, c := range rs {
			if c.T == "clause" && c.Item == r.ID {
				fmt.Printf("%s: %s — %s. Example: %s\n", escape(c.ID), escape(c.Observe), escape(c.Expect), escape(c.Example))
			}
		}
		fmt.Println()
	}
}
