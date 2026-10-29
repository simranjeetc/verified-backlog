package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// The rules doc is read here, not named to the dispatch: a packet carries the rules the item
// cites, verbatim, and the rest as an index, so a dispatch reads the rules it needs instead of
// the whole file (I76).
const packetRulesDoc = "references/design-check-list-schema.md"

var packetRuleHeading = regexp.MustCompile(`^\*\*R([0-9]+) — (.*)$`)
var packetRuleCitation = regexp.MustCompile(`R[0-9]+`)

// packet is one item's reading packet: what a dispatch pastes in place of the rules doc and the
// item's files. The claims are the producer's account, so only the verifier's packet holds them.
type packet struct {
	item    *Item
	role    string
	brief   string
	claims  string
	cited   []packetRule
	omitted []packetRule
	rulesOK bool
	run     *runFile
	runPath string
}

// packetRule is one rule paragraph of the rules doc: its number, its title, and its text.
type packetRule struct {
	number int
	text   string
	title  string
}

func cmdPacket(root string, a args) error {
	_, role, err := packetArgs(a)
	if err != nil {
		return err
	}
	if err := requireLedger(root); err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	_, byID, err := derive(events)
	if err != nil {
		return err
	}
	it, err := lookup(byID, a.pos(0))
	if err != nil {
		return err
	}
	if role == "verifier" && it.Claims == "" {
		return refuse("%s has no recorded claims", it.ID)
	}
	p := packet{item: it, role: role}
	if role == "verifier" {
		runs := it.attemptRuns()
		if len(runs) > 0 {
			latest := runs[len(runs)-1]
			p.runPath = filepath.ToSlash(filepath.Join(".backlog", "evidence", it.ID+"-"+latest.Run+".json"))
			data, readErr := verifyArtefact(root, p.runPath, latest.RunSHA, "run")
			if readErr != nil {
				return readErr
			}
			var run runFile
			if err := json.Unmarshal(data, &run); err != nil {
				return fmt.Errorf("decode run file %s: %w", p.runPath, err)
			}
			p.run = &run
		}
	}
	if p.brief, err = packetArtefact(root, it.Brief, it.BriefSHA, "(no brief)"); err != nil {
		return err
	}
	if role == "verifier" {
		if p.claims, err = packetArtefact(root, it.Claims, it.ClaimsSHA, ""); err != nil {
			return err
		}
	}
	if p.cited, p.omitted, p.rulesOK, err = packetRules(root, it, p.brief); err != nil {
		return err
	}
	p.printTo(os.Stdout)
	return nil
}

// packetArgs checks the verb's arguments: one id and one of the three roles.
func packetArgs(a args) (string, string, error) {
	id, role := a.pos(0), a.get("role")
	if len(a.positional) != 1 || id == "" {
		return "", "", refuse("usage: bl packet <id> --role checks|producer|verifier")
	}
	if role != "checks" && role != "producer" && role != "verifier" {
		return "", "", refuse("usage: bl packet <id> --role checks|producer|verifier")
	}
	return id, role, nil
}

// packetArtefact reads an artefact the ledger locates, refusing one that no longer matches the
// hash recorded for it (R20) rather than pasting an edited file into a dispatch.
func packetArtefact(root, rel, sha, empty string) (string, error) {
	if rel == "" {
		return empty, nil
	}
	data, err := verifyArtefact(root, rel, sha, strings.TrimSuffix(filepath.Base(rel), ".md"))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// packetRules splits the rules doc into the rules the item cites and the ones it leaves out.
// cited is true when the doc was read; a missing doc is not an error, because the rest of the
// packet is still worth printing.
func packetRules(root string, it *Item, brief string) (cited, omitted []packetRule, found bool, err error) {
	data, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(packetRulesDoc)))
	if readErr != nil {
		if os.IsNotExist(readErr) {
			return nil, nil, false, nil
		}
		return nil, nil, false, readErr
	}
	citations := citedPacketRules(it.Text, brief, strings.Join(it.Notes, "\n"))
	for _, rule := range parsePacketRules(string(data)) {
		if citations[rule.number] {
			cited = append(cited, rule)
		} else {
			omitted = append(omitted, rule)
		}
	}
	return cited, omitted, true, nil
}

func (p packet) printTo(w io.Writer) {
	fmt.Fprintf(w, "# %s packet (role %s)\n", p.item.ID, p.role)
	if p.role == "verifier" {
		if clause := packetClause(p.brief, "B0"); clause != "" {
			fmt.Fprint(w, "\n## Purpose (B0)\n")
			writePacketText(w, clause)
			fmt.Fprintln(w, "A defect that breaks this purpose is a failure, not report-only.")
			fmt.Fprintln(w, "End the report with one line `Report-only: N` and tag each report-only note [RO1]..[ROn]; with no notes write `Report-only: 0`.")
			fmt.Fprintln(w, "Write no other bracketed RO-like spelling anywhere in the report: describe a near-miss tag in words, without brackets.")
		}
	}
	fmt.Fprint(w, "\n## Intent\n")
	writePacketText(w, p.item.Text)
	if p.role == "verifier" && p.run != nil {
		fmt.Fprint(w, "\n## Run\n")
		fmt.Fprintln(w, p.runPath)
		for _, row := range p.run.Rows {
			fmt.Fprintf(w, "%s exit %d\n", row.ID, row.Exit)
		}
		fmt.Fprintln(w, "These rows already ran under bl run, including the test suites: do not re-run the suites or every row. Re-run only a row you doubt, and name it and why in the report.")
	}
	fmt.Fprint(w, "\n## Brief\n")
	writePacketText(w, p.brief)
	fmt.Fprint(w, "\n## Notes\n")
	writePacketLines(w, p.item.Notes, "- %s")
	fmt.Fprint(w, "\n## Rules cited\n")
	switch {
	case !p.rulesOK:
		fmt.Fprintf(w, "(rules doc not found: %s)\n", packetRulesDoc)
	case len(p.cited) == 0:
		fmt.Fprintln(w, "(none)")
	default:
		for _, rule := range p.cited {
			fmt.Fprintln(w, rule.text)
		}
	}
	fmt.Fprint(w, "\n## Rules not included\n")
	if len(p.omitted) == 0 {
		fmt.Fprintln(w, "(none)")
	}
	for _, rule := range p.omitted {
		fmt.Fprintf(w, "R%d — %s\n", rule.number, rule.title)
	}
	fmt.Fprint(w, "\n## Scope\n")
	writePacketLines(w, p.item.Scope, "- %s")
	if p.role == "verifier" {
		fmt.Fprint(w, "\n## Claims\n")
		writePacketText(w, p.claims)
	}
}

func packetClause(brief, id string) string {
	lines := strings.Split(brief, "\n")
	start := -1
	for i, line := range lines {
		m := clauseRe.FindStringSubmatch(strings.TrimRight(line, "\r"))
		if m == nil {
			continue
		}
		if start >= 0 {
			return strings.TrimRight(strings.Join(lines[start:i], "\n"), "\n")
		}
		if m[1] == id {
			start = i
		}
	}
	if start >= 0 {
		return strings.TrimRight(strings.Join(lines[start:], "\n"), "\n")
	}
	return ""
}

// writePacketText prints a recorded text verbatim, adding the newline it may lack.
func writePacketText(w io.Writer, text string) {
	fmt.Fprint(w, text)
	if !strings.HasSuffix(text, "\n") {
		fmt.Fprintln(w)
	}
}

// writePacketLines prints a list as one line each, or "(none)" when the list is empty.
func writePacketLines(w io.Writer, lines []string, format string) {
	if len(lines) == 0 {
		fmt.Fprintln(w, "(none)")
		return
	}
	for _, line := range lines {
		fmt.Fprintf(w, format+"\n", line)
	}
}

// citedPacketRules collects the rule numbers cited by the item's text, brief and notes: R<n> as a
// whole token, so R1 is not cited by R10 or by R100.
func citedPacketRules(texts ...string) map[int]bool {
	cited := map[int]bool{}
	for _, text := range texts {
		for _, loc := range packetRuleCitation.FindAllStringIndex(text, -1) {
			if !packetTokenEdge(text, loc[0], loc[1]) {
				continue
			}
			number, _ := strconv.Atoi(text[loc[0]+1 : loc[1]])
			cited[number] = true
		}
	}
	return cited
}

// packetTokenEdge reports whether the match at [start, end) stands alone: neither neighbour is a
// word character, which is what makes it a token rather than part of a longer word.
func packetTokenEdge(text string, start, end int) bool {
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(text[:start])
		if packetWordRune(previous) {
			return false
		}
	}
	if end < len(text) {
		next, _ := utf8.DecodeRuneInString(text[end:])
		return !packetWordRune(next)
	}
	return true
}

func packetWordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' }

// parsePacketRules reads the rules doc into paragraphs: a rule starts at a `**R<n> — ` line and
// runs to the line before the next rule or the next heading line.
func parsePacketRules(text string) []packetRule {
	var rules []packetRule
	var current *packetRule
	flush := func() {
		if current == nil {
			return
		}
		current.text = strings.TrimRight(current.text, "\n")
		rules = append(rules, *current)
		current = nil
	}
	for _, line := range strings.Split(text, "\n") {
		switch {
		case strings.HasPrefix(line, "#"):
			flush()
		case packetRuleHeading.MatchString(line):
			flush()
			heading := packetRuleHeading.FindStringSubmatch(line)
			number, _ := strconv.Atoi(heading[1])
			current = &packetRule{number: number, text: line, title: packetRuleTitle(heading[2])}
		case current != nil:
			current.text += "\n" + line
		}
	}
	flush()
	sort.Slice(rules, func(i, j int) bool { return rules[i].number < rules[j].number })
	return rules
}

// packetRuleTitle is the rule's title: the text after `— ` up to the first `.**`.
func packetRuleTitle(heading string) string {
	if end := strings.Index(heading, ".**"); end >= 0 {
		return heading[:end]
	}
	return heading
}
