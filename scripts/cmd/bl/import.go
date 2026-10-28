package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ── legacy import ────────────────────────────────────────────────────────────

type legacyEntry struct {
	ID     string
	Text   string
	Closed bool
	Rounds []string
	Commit string
}

func parseLegacy(todo, progress string) ([]*legacyEntry, []string, error) {
	var items []*legacyEntry
	var findings []string
	seen := map[string]bool{}
	if data, err := os.ReadFile(todo); err == nil {
		for n, line := range strings.Split(string(data), "\n") {
			m := legacyItemRe.FindStringSubmatch(line)
			if m == nil {
				if legacyBox.MatchString(line) {
					findings = append(findings, fmt.Sprintf("%s:%d: checkbox without a readable id — skipped", todo, n+1))
				}
				continue
			}
			id := m[2]
			if seen[strings.ToLower(id)] {
				findings = append(findings, fmt.Sprintf("%s:%d: duplicate id %s — skipped", todo, n+1, id))
				continue
			}
			seen[strings.ToLower(id)] = true
			items = append(items, &legacyEntry{ID: id, Text: strings.TrimSpace(m[3]), Closed: strings.EqualFold(m[1], "x")})
		}
	}
	if data, err := os.ReadFile(progress); err == nil {
		lines := strings.Split(string(data), "\n")
		byID := map[string]*legacyEntry{}
		for _, it := range items {
			byID[strings.ToLower(it.ID)] = it
		}
		for n, line := range lines {
			m := legacyHead.FindStringSubmatch(line)
			if m == nil {
				continue
			}
			id := m[1]
			target, ok := byID[strings.ToLower(id)]
			if !ok {
				findings = append(findings, fmt.Sprintf("%s:%d: record for unknown item %s — kept as an item", progress, n+1, id))
				text := strings.TrimSpace(m[2])
				if text == "" {
					text = "(imported record)"
				}
				target = &legacyEntry{ID: id, Text: text, Closed: true}
				byID[strings.ToLower(id)] = target
				items = append(items, target)
			}
			verdict, commit := "", ""
			end := n + 6
			if end > len(lines) {
				end = len(lines)
			}
			for _, body := range lines[n:end] {
				if vm := legacyVerdic.FindStringSubmatch(body); vm != nil {
					verdict = strings.ToUpper(vm[1])
				}
				if verdict == "PASS" {
					if cm := commitRe.FindStringSubmatch(body); cm != nil && len(cm[1]) >= 7 {
						commit = cm[1]
					}
				}
			}
			if verdict != "" {
				target.Rounds = append(target.Rounds, verdict)
			}
			if verdict == "PASS" {
				target.Closed = true
			}
			if commit != "" {
				target.Commit = commit
			}
		}
	}
	return items, findings, nil
}

func cmdLint(root string, a args) error {
	items, findings, err := parseLegacy(a.pos(0), a.pos(1))
	if err != nil {
		return err
	}
	fmt.Printf("parsed %d items, %d findings\n", len(items), len(findings))
	for _, f := range findings {
		fmt.Printf("  %s\n", f)
	}
	for _, it := range items {
		state := "open"
		if it.Closed {
			state = "closed"
		}
		text := it.Text
		if len(text) > 60 {
			text = text[:60]
		}
		fmt.Printf("  %-10s %-7s %s\n", it.ID, state, text)
	}
	if len(findings) > 0 {
		return errFailed
	}
	return nil
}

func cmdImport(root string, a args) error {
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	if len(events) > 0 {
		return refuse("the ledger is not empty — import runs once, into a fresh ledger")
	}
	p := newPaths(root)
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	items, findings, err := parseLegacy(a.pos(0), a.pos(1))
	if err != nil {
		return err
	}
	if len(items) == 0 {
		return refuse("nothing parseable found")
	}
	stamp := now()
	for _, it := range items {
		if _, err := appendEvent(root, Event{T: "file", ID: it.ID, Text: it.Text, Source: "import:" + filepath.Base(a.pos(0))}); err != nil {
			return err
		}
		if !it.Closed {
			continue
		}
		rows := "imported from prose; no evidence captured"
		name := it.ID + "-r1.txt"
		if err := atomicWrite(filepath.Join(p.evidence, name), rows+"\n"); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(rows + "\n"))
		if _, err := appendEvent(root, Event{
			T: "round", ID: it.ID, Verdict: "PASS",
			Rows:    backlogDir + "/" + evidenceDir + "/" + name,
			RowsSHA: hex.EncodeToString(sum[:]),
		}); err != nil {
			return err
		}
		commit := it.Commit
		if commit == "" {
			commit = "legacy:unknown"
		}
		if _, err := appendEvent(root, Event{T: "close", ID: it.ID, Commit: commit}); err != nil {
			return err
		}
		note := fmt.Sprintf("imported %s: closed on the prose record's word", stamp)
		if it.Commit != "" {
			note += ", commit " + it.Commit
		} else {
			note += ", no commit recorded"
		}
		if _, err := appendEvent(root, Event{T: "note", ID: it.ID, Text: note}); err != nil {
			return err
		}
	}
	fmt.Printf("bl: imported %d items, %d findings\n", len(items), len(findings))
	return nil
}
