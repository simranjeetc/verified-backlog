package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
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
}

type intakeError struct {
	line          int
	field, reason string
}

func intakeSchema() map[string]any {
	fields := map[string][]string{"source": {"t", "id", "path", "lines", "sha"}, "decision": {"t", "id", "text", "by"}, "item": {"t", "id", "title", "after", "scope", "source", "decisions"}, "clause": {"t", "item", "id", "observe", "expect", "example"}}
	arrays := map[string]bool{"after": true, "scope": true, "decisions": true}
	result := map[string]any{}
	for kind, names := range fields {
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
		result[kind] = map[string]any{"type": "object", "required": names, "properties": props}
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
	for line := 1; s.Scan(); line++ {
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(s.Bytes(), &raw); err != nil {
			errs = append(errs, intakeError{line, "", "invalid JSON"})
			continue
		}
		var r intakeRecord
		if err := json.Unmarshal(s.Bytes(), &r); err != nil {
			errs = append(errs, intakeError{line, "", "invalid JSON"})
			continue
		}
		r.line = line
		records = append(records, r)
	}
	if s.Err() != nil {
		errs = append(errs, intakeError{1, "", "cannot read input"})
	}
	return records, errs
}

func validateIntake(path string, rs []intakeRecord, filed map[string]bool) []intakeError {
	var es []intakeError
	seen := map[string]map[string]int{}
	items := map[string]int{}
	decisions := map[string]int{}
	clauses := map[string]int{}
	add := func(r intakeRecord, field, reason string) { es = append(es, intakeError{r.line, field, reason}) }
	for _, r := range rs {
		fields := map[string][]string{"source": {"t", "id", "path", "lines", "sha"}, "decision": {"t", "id", "text", "by"}, "item": {"t", "id", "title"}, "clause": {"t", "item", "id", "observe", "expect", "example"}}[r.T]
		if fields == nil {
			add(r, "t", "unknown record type")
			continue
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
			if v == "" {
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
		if dupKey != "" {
			if seen[r.T][dupKey] != 0 {
				add(r, "id", "duplicate id")
			}
			seen[r.T][dupKey] = r.line
		}
		switch r.T {
		case "item":
			items[r.ID] = r.line
			if filed[r.ID] && r.ID != "" {
				add(r, "id", "already filed in ledger")
			}
		case "decision":
			decisions[r.ID] = r.line
		case "clause":
			clauses[r.Item]++
		}
		if r.T == "clause" && r.Example == "" {
			add(r, "example", "must not be empty")
		}
		if r.T == "source" {
			if !regexp.MustCompile(`^[0-9a-fA-F]{40}$`).MatchString(r.SHA) {
				add(r, "sha", "must be 40 hexadecimal characters")
			}
			parts := strings.Split(r.Lines, "-")
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
	for _, r := range rs {
		if r.T == "clause" && items[r.Item] == 0 {
			add(r, "item", "does not name an item in this intake")
		}
		if r.T == "item" {
			for _, d := range r.Decisions {
				if decisions[d] == 0 {
					add(r, "decisions", "unknown decision "+d)
				}
			}
			for _, dep := range r.After {
				if items[dep] == 0 && !filed[dep] {
					add(r, "after", "unknown item "+dep)
				}
			}
			if clauses[r.ID] == 0 {
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
		fmt.Printf("## %s — %s\n\n", r.ID, r.Title)
		if len(r.After) > 0 {
			fmt.Printf("after: %s\n\n", strings.Join(r.After, ", "))
		}
		for _, d := range r.Decisions {
			fmt.Printf("Decision: %s\n\n", decisions[d])
		}
		for _, c := range rs {
			if c.T == "clause" && c.Item == r.ID {
				fmt.Printf("%s: %s — %s. Example: %s\n", c.ID, c.Observe, c.Expect, c.Example)
			}
		}
		fmt.Println()
	}
}
