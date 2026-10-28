package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// briefText numbers the item's clauses B1, B2, ... one per line. An item text is split into
// clauses at semicolons; a text with none is one clause, B1.
func briefText(text string) string {
	var clauses []string
	for _, part := range clauseSplit.Split(strings.Join(strings.Fields(text), " "), -1) {
		if part = strings.TrimSpace(part); part != "" {
			clauses = append(clauses, part)
		}
	}
	var b strings.Builder
	for i, c := range clauses {
		fmt.Fprintf(&b, "B%d: %s\n", i+1, c)
	}
	return b.String()
}

// briefClauses returns the clause ids of a brief (§2: a line matching ^(B[0-9]+):\s+(.+)$).
func briefClauses(text string) map[string]bool {
	out := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if m := clauseRe.FindStringSubmatch(strings.TrimRight(line, "\r")); m != nil {
			out[m[1]] = true
		}
	}
	return out
}

func clausesWithoutExamples(text string) []string {
	lines := strings.Split(text, "\n")
	var missing []string
	for i, line := range lines {
		m := clauseRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		var clause strings.Builder
		clause.WriteString(line)
		for j := i + 1; j < len(lines); j++ {
			if clauseRe.MatchString(strings.TrimRight(lines[j], "\r")) {
				break
			}
			clause.WriteByte('\n')
			clause.WriteString(lines[j])
		}
		if !strings.Contains(strings.ToLower(clause.String()), "example:") {
			missing = append(missing, m[1])
		}
	}
	return missing
}

// failureRecords is the bytes of every failure record the item's restarts wrote, oldest first,
// as they are on disk (R36). It is what attempt n's brief carries after its own text: the
// records of every attempt before it, byte for byte. A record that cannot be read contributes
// nothing here — `bl check` is what reports it.
func failureRecords(root string, it *Item) string {
	var b strings.Builder
	for _, r := range it.Restarts {
		if r.Record == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Record)))
		if err != nil {
			continue
		}
		b.Write(data)
	}
	return b.String()
}

// attemptBrief is the brief an attempt records (R36): the text, then the failure records of
// every attempt before it. A text whose last line has no newline gets one first, so the first
// record always starts a line of its own; nothing is de-duplicated.
func attemptBrief(text, records string) string {
	if records == "" {
		return text
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + records
}

// cmdBrief records the item's brief. The text comes from --file, or from stdin when --file is
// absent. An amended brief is a new event with a new hash; the earlier event keeps its own hash
// (R24). Once the producer has claimed, the brief is frozen (R5). Before it overwrites the stored
// brief it re-hashes that file and refuses when it no longer matches the ledger (R20).
func cmdBrief(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	order, byID, err := derive(events)
	if err != nil {
		return err
	}
	it, err := liveItem(byID, a.pos(0))
	if err != nil {
		return err
	}
	if err := requireInFlight(order, it, ""); err != nil {
		return err
	}
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the brief is frozen", it.ID)
	}
	// R20: the stored brief is what the ledger says it is. Re-hash it before overwriting it —
	// the same helper `bl check` and `bl round` use, so there is one comparison, not three.
	// An item with no recorded brief has nothing to re-hash.
	if it.Brief != "" {
		if _, err := verifyArtefact(root, it.Brief, it.BriefSHA, "brief"); err != nil {
			return err
		}
	}
	text, err := a.input("file")
	if err != nil {
		return err
	}
	if len(briefClauses(text)) == 0 {
		return refuseRule("R2", "the brief has no numbered clause — a clause is a line matching %s", clauseRe.String())
	}
	clauses := briefClauses(text)
	if !clauses["B0"] {
		return refuseRule("R2", "the brief has no B0 purpose clause")
	}
	if missing := clausesWithoutExamples(text); len(missing) != 0 {
		return refuseRule("R2", "clauses %s have no Example:", strings.Join(missing, ", "))
	}
	text = attemptBrief(text, failureRecords(root, it))
	rel := attemptFile(it.ID, it.Attempt, briefName)
	if err := atomicWrite(filepath.Join(root, rel), text); err != nil {
		return err
	}
	ev := Event{T: "brief", ID: it.ID, Brief: rel, BriefSHA: shaHex([]byte(text))}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s brief recorded\n", it.ID)
	return nil
}
