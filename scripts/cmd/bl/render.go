package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── rendering ────────────────────────────────────────────────────────────────

func atomicWrite(path, text string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, "."+filepath.Base(path)+".")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return err
	}
	if err := flushFile(f); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func renderQueue(root string, order []*Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# TODO — %s\n\n<!-- %s; do not edit — ask instead -->\n\n", filepath.Base(root), marker)
	groups := []struct {
		title string
		rows  []*Item
	}{
		{"Queue", nil}, {"Blocked", nil}, {"Parked", nil}, {"Declined", nil}, {"Closed", nil},
	}
	for _, it := range order {
		switch {
		case it.Status == "open" && it.BlockedBy == "":
			groups[0].rows = append(groups[0].rows, it)
		case it.Status == "open":
			groups[1].rows = append(groups[1].rows, it)
		case it.Status == "parked":
			groups[2].rows = append(groups[2].rows, it)
		case it.Status == "declined":
			groups[3].rows = append(groups[3].rows, it)
		case it.Status == "closed":
			groups[4].rows = append(groups[4].rows, it)
		}
	}
	for _, g := range groups {
		fmt.Fprintf(&b, "## %s (%d)\n\n", g.title, len(g.rows))
		if len(g.rows) == 0 {
			b.WriteString("_none_\n\n")
			continue
		}
		for _, it := range g.rows {
			var bits []string
			if it.BlockedBy != "" {
				bits = append(bits, "waiting on "+it.BlockedBy)
			}
			if it.Status == "closed" && it.Commit != "" {
				c := it.Commit
				if len(c) > 7 {
					c = c[:7]
				}
				bits = append(bits, "commit "+c)
			}
			if (it.Status == "parked" || it.Status == "declined") && it.Reason != "" {
				bits = append(bits, it.Reason)
			}
			if it.Source != "" {
				bits = append(bits, it.Source)
			}
			tail := ""
			if len(bits) > 0 {
				tail = "  _(" + strings.Join(bits, "; ") + ")_"
			}
			box := " "
			if it.Status == "closed" || it.Status == "declined" {
				box = "x"
			}
			fmt.Fprintf(&b, "- [%s] %s — %s%s\n", box, it.ID, it.Text, tail)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func renderRecords(root string, order []*Item) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# PROGRESS — %s\n\n<!-- %s; do not edit — ask instead -->\n\n", filepath.Base(root), marker)
	var recorded []*Item
	for _, it := range order {
		if len(it.Rounds) > 0 || len(it.Notes) > 0 || it.Status == "closed" || it.Status == "declined" {
			recorded = append(recorded, it)
		}
	}
	if len(recorded) == 0 {
		b.WriteString("_no records yet_\n\n")
		return b.String()
	}
	for _, it := range recorded {
		fmt.Fprintf(&b, "## %s — %s\n\n", it.ID, it.Text)
		switch it.Status {
		case "closed":
			fmt.Fprintf(&b, "Verdict: PASS — closed at %s\n", it.Commit)
		case "declined":
			fmt.Fprintf(&b, "Decision: declined — %s (not verified)\n", it.Reason)
		case "parked":
			fmt.Fprintf(&b, "Decision: parked — %s (not verified)\n", it.Reason)
		default:
			if last := it.last(); last != nil && last.Verdict == "PASS" {
				b.WriteString("Status: open — PASS recorded, close pending\n")
			} else {
				b.WriteString("Status: open\n")
			}
		}
		if it.Source != "" {
			fmt.Fprintf(&b, "Source: %s\n", it.Source)
		}
		b.WriteString("\n")
		for i, r := range it.Rounds {
			cause := ""
			if r.Cause != "" {
				cause = " cause=" + r.Cause
			}
			if r.Rows == "" {
				// a harness round carries no rows, so there is no evidence file to name
				fmt.Fprintf(&b, "- r%d %s%s — no rows\n", i+1, r.Verdict, cause)
				continue
			}
			sha := r.RowsSHA
			if len(sha) > 12 {
				sha = sha[:12]
			}
			fmt.Fprintf(&b, "- r%d %s%s — %s (%s)\n", i+1, r.Verdict, cause, r.Rows, sha)
		}
		if len(it.Rounds) > 0 {
			fmt.Fprintf(&b, "- code failures: %d / cap %d\n", it.codeFails(), roundCap)
		}
		for _, n := range it.Notes {
			fmt.Fprintf(&b, "- note: %s\n", n)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func render(root string, events []Event) error {
	order, _, err := derive(events)
	if err != nil {
		return err
	}
	p := newPaths(root)
	if err := atomicWrite(p.queue, renderQueue(root, order)); err != nil {
		return err
	}
	return atomicWrite(p.record, renderRecords(root, order))
}
