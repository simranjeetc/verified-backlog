// Command bl is the verified-backlog ledger.
//
// State lives in .backlog/ledger.jsonl, one JSON object per line, append-only, in call order.
// TODO.md and PROGRESS.md are rendered from it after every append and are never read by a
// program. The model judges; this binary records, refuses, and renders.
//
// One command, one append. Nothing is rewritten and nothing is inferred: if a line is not in
// the ledger, it did not happen.
//
// Usage:
//
//	bl init
//	bl file --id A1 --text "..." [--after B2] [--source human]
//	bl open A1                                            (writes .backlog/items/A1/brief.md)
//	bl brief A1 --file brief.md                           (amends the brief; a new event)
//	bl checks A1 [--file checks.tsv] [--amend]            (list from stdin without --file)
//	bl rebaseline A1 --file checks.tsv --reason "..."      (replaces the frozen list)
//	bl claims A1 --file claims.md
//	bl run A1                                            (runs the frozen rows; writes the run file)
//	bl round A1 --verdict FAIL --cause code --rows -      (rows from stdin)
//	bl round A1 --verdict PASS --rows checks.txt
//	bl close A1 --commit 6742cf6
//	bl drop D9 --reason "cosmetic; not worth it"
//	bl defer O4 --reason "awaiting triage"
//	bl reopen O4
//	bl note A1 --text "deferred because ..."
//	bl next | bl show A1 | bl status | bl check [--views] | bl render
//	bl audit [--since 2d] [--out audit]
//	bl import TODO.md PROGRESS.md | bl lint TODO.md PROGRESS.md
//	bl schema [--json]
//
// Exit status: 0 ok, 2 refused (nothing written), 3 the ledger itself is corrupt.
// Every invocation appends one line to .backlog/run.log: argv, exit code, duration.
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"
)

const (
	backlogDir  = ".backlog"
	ledgerName  = "ledger.jsonl"
	runlogName  = "run.log"
	evidenceDir = "evidence"
	queueView   = "TODO.md"
	recordView  = "PROGRESS.md"
	roundCap    = 3
	totalCap    = 6
	marker      = "rendered from " + backlogDir + "/" + ledgerName + " by bl"
	itemsDir    = "items"
	briefName   = "brief.md"
	checksName  = "checks.tsv"
	claimsName  = "claims.md"
)

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:[._-][A-Za-z0-9]+)*$`)
	legacyItemRe = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX~])\]\s*(?:\*\*)?([A-Z][A-Z0-9]{0,9}-?\d+(?:\.\d+)*[A-Za-z]?)(?:\*\*)?\s*[—–:-]?\s*(.+)$`)
	legacyHead   = regexp.MustCompile(`^#{2,6}\s+(?:\*\*)?([A-Z][A-Z0-9]{0,9}-?\d+(?:\.\d+)*[A-Za-z]?)(?:\*\*)?\s*[—–:-]?\s*(.*)$`)
	legacyBox    = regexp.MustCompile(`^\s*[-*+]\s+\[(.)\]`)
	legacyVerdic = regexp.MustCompile(`(?i)^\s*(?:[-*>]\s*)?(?:\*\*)?Verdict(?:\*\*)?\s*:\s*(?:\*\*)?\s*(PASS|FAIL)`)
	commitRe     = regexp.MustCompile(`\b([0-9a-f]{7,40})\b`)
	periodRe     = regexp.MustCompile(`^(\d+)\s*([dhw])$`)
	clauseRe     = regexp.MustCompile(`^(B[0-9]+):\s+(.+)$`)
	clauseSplit  = regexp.MustCompile(`;\s+`)
	rowIDRe      = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)
	exitCodeRe   = regexp.MustCompile(`^(0|[1-9][0-9]{0,2})$`)
	treeHashRe   = regexp.MustCompile(`^[0-9a-f]{40}$`)
)

var (
	verdicts = []string{"PASS", "FAIL"}
	causes   = []string{"code", "harness", "dispute"}
)

// Event is one line of the ledger.
type Event struct {
	Seq     int    `json:"seq"`
	At      string `json:"at"`
	T       string `json:"t"`
	ID      string `json:"id,omitempty"`
	Text    string `json:"text,omitempty"`
	After   string `json:"after,omitempty"`
	Source  string `json:"source,omitempty"`
	Verdict string `json:"verdict,omitempty"`
	Cause   string `json:"cause,omitempty"`
	Rows    string `json:"rows,omitempty"`
	RowsSHA string `json:"rows_sha,omitempty"`
	Commit  string `json:"commit,omitempty"`
	Reason  string `json:"reason,omitempty"`
	By      string `json:"by,omitempty"`
	// Artefacts (R20): each event that references a file records its path and sha256.
	Brief     string `json:"brief,omitempty"`
	BriefSHA  string `json:"brief_sha,omitempty"`
	Checks    string `json:"checks,omitempty"`
	ChecksSHA string `json:"checks_sha,omitempty"`
	PrevSHA   string `json:"prev_checks_sha,omitempty"`
	Claims    string `json:"claims,omitempty"`
	ClaimsSHA string `json:"claims_sha,omitempty"`
	// The run (R7): the run id, and the sha256 of the run file it names.
	Run    string `json:"run,omitempty"`
	RunSHA string `json:"run_sha,omitempty"`
}

// Item is the derived state of one backlog entry.
type Item struct {
	ID        string
	Text      string
	After     string
	Source    string
	Status    string // open, closed, declined, parked
	BlockedBy string
	Rounds    []Event
	Runs      []Event
	Notes     []string
	Opened    bool
	Commit    string
	Reason    string
	FiledAt   string
	// The latest event that recorded each artefact (R24).
	Brief, BriefSHA   string
	Checks, ChecksSHA string
	Claims, ClaimsSHA string
	// The seq of the latest re-baseline: a re-baseline is a new premise, so it is recorded
	// as the premise it is. It resets no count — the round budget belongs to the item.
	RebaselineSeq int
}

// causeRounds counts the item's rounds of one cause over its whole life (R19). A re-baseline
// is a new premise, not a new item, so it resets no count.
func (i *Item) causeRounds(cause string) int {
	n := 0
	for _, r := range i.Rounds {
		if r.Cause == cause {
			n++
		}
	}
	return n
}

// codeFails counts the code rounds the item has spent over its whole life (R19).
func (i *Item) codeFails() int {
	return i.causeRounds("code")
}

// passRounds counts the item's PASS rounds over its whole life (R19). A PASS carries no
// cause, so it is counted here: a PASS is a round, and it spends the budget.
func (i *Item) passRounds() int {
	n := 0
	for _, r := range i.Rounds {
		if r.Verdict == "PASS" {
			n++
		}
	}
	return n
}

func (i *Item) last() *Event {
	if len(i.Rounds) == 0 {
		return nil
	}
	return &i.Rounds[len(i.Rounds)-1]
}

// Refusal is a call the ledger will not accept. Nothing is written.
// rule names the design rule the call violates, when there is one.
type Refusal struct{ msg, rule string }

func (r Refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return Refusal{msg: fmt.Sprintf(format, a...)} }

func refuseRule(rule, format string, a ...any) error {
	return Refusal{msg: fmt.Sprintf(format, a...), rule: rule}
}

// Corruption means the ledger on disk breaks its own invariants.
type Corruption struct{ msg string }

func (c Corruption) Error() string { return c.msg }

func corrupt(format string, a ...any) error { return Corruption{fmt.Sprintf(format, a...)} }

type paths struct {
	base, ledger, runlog, evidence, items, queue, record string
}

func newPaths(root string) paths {
	base := filepath.Join(root, backlogDir)
	return paths{
		base:     base,
		ledger:   filepath.Join(base, ledgerName),
		runlog:   filepath.Join(base, runlogName),
		evidence: filepath.Join(base, evidenceDir),
		items:    filepath.Join(base, itemsDir),
		queue:    filepath.Join(root, queueView),
		record:   filepath.Join(root, recordView),
	}
}

// itemFile is the repository-relative path of an item's artefact, as recorded in the ledger.
func itemFile(id, name string) string { return backlogDir + "/" + itemsDir + "/" + id + "/" + name }

func now() string { return time.Now().UTC().Format(time.RFC3339) }

func shaHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ── ledger io ────────────────────────────────────────────────────────────────

func readLedger(root string) ([]Event, error) {
	p := newPaths(root)
	f, err := os.Open(p.ledger)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var events []Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	line := 0
	last := 0
	for sc.Scan() {
		line++
		raw := strings.TrimSpace(sc.Text())
		if raw == "" {
			continue
		}
		var ev Event
		if err := json.Unmarshal([]byte(raw), &ev); err != nil {
			return nil, corrupt("%s:%d: not JSON (%v)", p.ledger, line, err)
		}
		if ev.T == "" || ev.Seq == 0 {
			return nil, corrupt("%s:%d: missing t/seq", p.ledger, line)
		}
		if ev.Seq <= last {
			return nil, corrupt("%s: seq is not strictly increasing", p.ledger)
		}
		last = ev.Seq
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return events, nil
}

func appendEvent(root string, ev Event) (Event, error) {
	p := newPaths(root)
	if err := os.MkdirAll(p.base, 0o755); err != nil {
		return ev, err
	}
	events, err := readLedger(root)
	if err != nil {
		return ev, err
	}
	ev.Seq = 1
	if len(events) > 0 {
		ev.Seq = events[len(events)-1].Seq + 1
	}
	ev.At = now()
	line, err := json.Marshal(ev)
	if err != nil {
		return ev, err
	}
	f, err := os.OpenFile(p.ledger, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ev, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return ev, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return ev, err
	}
	if err := f.Close(); err != nil {
		return ev, err
	}
	all := append(events, ev)
	return ev, render(root, all)
}

func logRun(root string, argv []string, code int, started time.Time) {
	p := newPaths(root)
	if st, err := os.Stat(p.base); err != nil || !st.IsDir() {
		return
	}
	rec := map[string]any{
		"at":   now(),
		"argv": argv,
		"exit": code,
		"ms":   time.Since(started).Milliseconds(),
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(p.runlog, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(line, '\n'))
}

// ── derived state ────────────────────────────────────────────────────────────

func derive(events []Event) ([]*Item, map[string]*Item, error) {
	var order []*Item
	byID := map[string]*Item{}
	for _, ev := range events {
		switch ev.T {
		case "file":
			key := strings.ToLower(ev.ID)
			if _, dup := byID[key]; dup {
				return nil, nil, corrupt("duplicate file event for %s", ev.ID)
			}
			it := &Item{
				ID: ev.ID, Text: ev.Text, After: ev.After, Source: ev.Source,
				Status: "open", FiledAt: ev.At,
			}
			byID[key] = it
			order = append(order, it)
			continue
		}
		it, ok := byID[strings.ToLower(ev.ID)]
		if !ok {
			return nil, nil, corrupt("%s names unknown item %s", ev.T, ev.ID)
		}
		switch ev.T {
		case "open":
			it.Opened = true
			if ev.Brief != "" {
				it.Brief, it.BriefSHA = ev.Brief, ev.BriefSHA
			}
		case "brief":
			it.Brief, it.BriefSHA = ev.Brief, ev.BriefSHA
		case "checks":
			if it.Claims != "" {
				return nil, nil, corrupt("checks event for %s after its claims", ev.ID)
			}
			it.Checks, it.ChecksSHA = ev.Checks, ev.ChecksSHA
		case "rebaseline":
			if it.Claims != "" {
				return nil, nil, corrupt("rebaseline event for %s after its claims", ev.ID)
			}
			it.Checks, it.ChecksSHA = ev.Checks, ev.ChecksSHA
			it.RebaselineSeq = ev.Seq
		case "claims":
			if it.Checks == "" {
				return nil, nil, corrupt("claims event for %s before any check list", ev.ID)
			}
			it.Claims, it.ClaimsSHA = ev.Claims, ev.ClaimsSHA
		case "round":
			it.Rounds = append(it.Rounds, ev)
		case "run":
			it.Runs = append(it.Runs, ev)
		case "close":
			it.Status, it.Commit, it.Opened = "closed", ev.Commit, false
		case "drop":
			it.Status, it.Reason, it.Opened = "declined", ev.Reason, false
		case "defer":
			it.Status, it.Reason, it.Opened = "parked", ev.Reason, false
		case "reopen":
			it.Status, it.Reason, it.Opened = "open", "", false
		case "note":
			it.Notes = append(it.Notes, ev.Text)
		}
	}
	for _, it := range order {
		if it.Status == "open" && it.After != "" {
			dep, ok := byID[strings.ToLower(it.After)]
			switch {
			case !ok:
				it.BlockedBy = it.After
			case dep.Status != "closed" && dep.Status != "declined":
				it.BlockedBy = dep.ID
			}
		}
	}
	return order, byID, nil
}

func pinned(order []*Item) (*Item, error) {
	var live []*Item
	for _, it := range order {
		if it.Opened && it.Status == "open" {
			live = append(live, it)
		}
	}
	if len(live) > 1 {
		ids := make([]string, len(live))
		for i, it := range live {
			ids[i] = it.ID
		}
		return nil, corrupt("more than one item open: %s", strings.Join(ids, ", "))
	}
	if len(live) == 0 {
		return nil, nil
	}
	return live[0], nil
}

func eligible(order []*Item) []*Item {
	var out []*Item
	for _, it := range order {
		if it.Status == "open" && it.BlockedBy == "" {
			out = append(out, it)
		}
	}
	return out
}

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
	if err := f.Sync(); err != nil {
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

// ── git ──────────────────────────────────────────────────────────────────────

func git(root string, args ...string) (string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	code := 0
	if err != nil {
		code = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	return string(out), code
}

func isGit(root string) bool {
	_, code := git(root, "rev-parse", "--git-dir")
	return code == 0
}

func isArtifact(rel string) bool {
	return strings.HasPrefix(rel, backlogDir+"/") || rel == queueView || rel == recordView
}

// ── validators ───────────────────────────────────────────────────────────────

func requireLedger(root string) error {
	if _, err := os.Stat(newPaths(root).ledger); err != nil {
		return refuse("no ledger at %s — run `bl init` first", newPaths(root).ledger)
	}
	return nil
}

func cleanText(value, field string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", refuse("%s is empty", field)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", refuse("%s must be one line — put detail in --rows or bl note", field)
	}
	return v, nil
}

func checkID(value string) (string, error) {
	v, err := cleanText(value, "id")
	if err != nil {
		return "", err
	}
	if !idPattern.MatchString(v) {
		return "", refuse("id %q does not match %s", v, idPattern.String())
	}
	return v, nil
}

func lookup(byID map[string]*Item, id string) (*Item, error) {
	it, ok := byID[strings.ToLower(id)]
	if !ok {
		return nil, refuse("unknown item %s", id)
	}
	return it, nil
}

func liveItem(byID map[string]*Item, id string) (*Item, error) {
	it, err := lookup(byID, id)
	if err != nil {
		return nil, err
	}
	if it.Status != "open" {
		return nil, refuse("%s is %s, not open", it.ID, it.Status)
	}
	return it, nil
}

// ── verbs ────────────────────────────────────────────────────────────────────

func cmdInit(root string, a args) error {
	p := newPaths(root)
	if _, err := os.Stat(p.ledger); err == nil {
		return refuse("%s already exists", p.ledger)
	}
	for _, view := range []string{p.queue, p.record} {
		data, err := os.ReadFile(view)
		if err == nil && !strings.Contains(string(data), marker) {
			return refuse("%s exists and was not rendered by bl — run `bl import` or move it aside first", filepath.Base(view))
		}
	}
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p.ledger, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := render(root, nil); err != nil {
		return err
	}
	fmt.Printf("bl: initialised %s (ledger, evidence, %s, %s)\n", p.base, queueView, recordView)
	return nil
}

func cmdFile(root string, a args) error {
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
	id, err := checkID(a.get("id"))
	if err != nil {
		return err
	}
	if existing, ok := byID[strings.ToLower(id)]; ok {
		return refuse("id %s already exists", existing.ID)
	}
	text, err := cleanText(a.get("text"), "text")
	if err != nil {
		return err
	}
	ev := Event{T: "file", ID: id, Text: text}
	if after := a.get("after"); after != "" {
		dep, err := checkID(after)
		if err != nil {
			return err
		}
		if strings.EqualFold(dep, id) {
			return refuse("an item cannot wait on itself")
		}
		if _, ok := byID[strings.ToLower(dep)]; !ok {
			return refuse("--after %s names no item", dep)
		}
		ev.After = dep
	}
	if src := a.get("source"); src != "" {
		s, err := cleanText(src, "source")
		if err != nil {
			return err
		}
		ev.Source = s
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if ev.After != "" {
		fmt.Printf("bl: filed %s (after %s)\n", id, ev.After)
	} else {
		fmt.Printf("bl: filed %s\n", id)
	}
	return nil
}

func cmdOpen(root string, a args) error {
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
	// R5: the brief is frozen with the claims. `bl open` writes the brief from the item text, so
	// opening an item that already has a claims event would replace the brief the claims were
	// recorded against, and a frozen row citing a clause of it would be left pointing at a
	// clause that no longer exists. The check comes before the brief is read or written, so a
	// refusal leaves the stored brief exactly as it was.
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the brief is frozen with the claims", it.ID)
	}
	if it.BlockedBy != "" {
		return refuse("%s is blocked by %s", it.ID, it.BlockedBy)
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur != nil {
		return refuse("%s is already in flight — close, drop or defer it first", cur.ID)
	}
	brief := briefText(it.Text)
	rel := itemFile(it.ID, briefName)
	// R20: the stored brief is what the ledger says it is. Re-hash it before it is overwritten —
	// the same helper every other verb that touches a frozen artefact uses, so there is one
	// comparison, not three. An item with no recorded brief has nothing to re-hash.
	if it.Brief != "" {
		if _, err := verifyArtefact(root, it.Brief, it.BriefSHA, "brief"); err != nil {
			return err
		}
	}
	if err := atomicWrite(filepath.Join(root, rel), brief); err != nil {
		return err
	}
	ev := Event{T: "open", ID: it.ID, Brief: rel, BriefSHA: shaHex([]byte(brief))}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s in flight\n", it.ID)
	return nil
}

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
	if err := requireInFlight(order, it); err != nil {
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
	rel := itemFile(it.ID, briefName)
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

// verifyArtefact re-reads a frozen artefact and refuses if it no longer matches the hash of the
// latest event that recorded it (R20). The message names the artefact and both hashes.
func verifyArtefact(root, rel, want, what string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, refuseRule("R20", "the %s %s is missing", what, rel)
	}
	if got := shaHex(data); got != want {
		return nil, refuseRule("R20", "the %s %s no longer matches the hash recorded in the ledger: recorded %s, on disk %s", what, rel, want, got)
	}
	return data, nil
}

// verifyFrozen re-hashes the item's frozen artefacts — the brief, the check list and the claims —
// and refuses when one of them no longer matches the hash of the latest event that recorded it
// (R20). It is the check every verb that reads a frozen artefact makes before it uses it.
func verifyFrozen(root string, it *Item) error {
	for _, a := range []struct{ what, rel, want string }{
		{"brief", it.Brief, it.BriefSHA},
		{"check list", it.Checks, it.ChecksSHA},
		{"claims", it.Claims, it.ClaimsSHA},
	} {
		if a.rel == "" {
			continue
		}
		if _, err := verifyArtefact(root, a.rel, a.want, a.what); err != nil {
			return err
		}
	}
	return nil
}

func requireInFlight(order []*Item, it *Item) error {
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur == nil || !strings.EqualFold(cur.ID, it.ID) {
		return refuse("%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
	}
	return nil
}

// verifyBriefRefs checks that every frozen row still resolves against the brief. This is the USE
// path — `bl round`, `bl check` and `bl close` — so the refusal is R24's: an amendment removed
// the clause the frozen row cites, and the row's criterion no longer exists in the brief it was
// written against.
//
// The WRITE path is R2's: `bl checks` refuses a list written with a clause that does not exist
// (validateR2), and it keeps R2. The two are different faults — a list that was never valid, and
// a list that a later amendment invalidated — and they are named differently.
//
// A row with an empty brief_ref is fine — a judge row and an advisory row may both carry one.
// Only a brief_ref that is present and names a clause the brief no longer holds is a refusal.
// The message names the row and the clause.
func verifyBriefRefs(root string, it *Item) error {
	if it.Brief == "" || it.Checks == "" {
		return nil
	}
	brief, err := verifyArtefact(root, it.Brief, it.BriefSHA, "brief")
	if err != nil {
		return err
	}
	list, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	clauses := briefClauses(string(brief))
	rows, err := parseChecks(string(list))
	if err != nil {
		return refuseRule("R1", "the stored check list %s does not parse: %v", it.Checks, err)
	}
	for _, r := range rows {
		ref := r.f["brief_ref"]
		if ref == "" || clauses[ref] {
			continue
		}
		return refuseRule("R24", "row %s: brief_ref %q names no clause in the brief %s — a brief amendment removed it, so the row's criterion no longer exists in the brief it was written against",
			r.f["id"], ref, it.Brief)
	}
	return nil
}

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
// commands cannot drift apart on what a list may say.
func validateChecks(root string, it *Item, rows []checkRow, amend bool) (string, error) {
	if err := validateR1(rows); err != nil {
		return "", err
	}
	// R34: the command's dialect. A cmd is a script run by /bin/sh -c, so a pipe in it is a
	// pipe; a backslash before a pipe is read by the shell as a literal pipe, and the row then
	// does not do what it says. The list is refused here, before it is frozen, so the mistake
	// cannot reach a round.
	if err := validateR34(rows); err != nil {
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
		if err := checkAmendment(old, rows); err != nil {
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
	if err := validateR8(rows); err != nil {
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
// every row. A cmd is run by /bin/sh -c, so a pipe in it is a pipe; a backslash before a pipe
// is read by the shell as a literal pipe, so the row does not do what it says and the list is
// refused before it can reach a round.
//
// Only the cmd field is checked. An expect pattern is Go's RE2, where a backslash before a
// pipe means a literal pipe — and there that is what the pattern says, not a fault.
func validateR34(rows []checkRow) error {
	for _, r := range rows {
		if strings.Contains(r.f["cmd"], `\|`) {
			return refuseRule("R34", "row %s: the command holds a backslash before a pipe — the shell reads that as a literal pipe; write the pipe as a pipe",
				r.f["id"])
		}
	}
	return nil
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

// validateR8 applies R8 — an expectation is one of five forms — to every row. Only a decider bl
// row carries an expectation.
func validateR8(rows []checkRow) error {
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

// checkAmendment allows exactly the two edits of R4: judge → bl, and filling an empty brief_ref
// on an advisory row. old and next are both already valid lists.
func checkAmendment(old, next []checkRow) error {
	if len(old) != len(next) {
		return refuseRule("R4", "an amendment never adds or removes a row (%d rows, was %d)", len(next), len(old))
	}
	changed := false
	for i := range old {
		o, n := old[i].f, next[i].f
		if o["id"] != n["id"] {
			return refuseRule("R4", "row %d: an amendment never changes or reorders ids (%s, was %s)", i+1, n["id"], o["id"])
		}
		id := o["id"]
		for _, col := range []string{"severity", "cmd", "criterion"} {
			if o[col] != n[col] {
				return refuseRule("R4", "row %s: an amendment never changes %s", id, col)
			}
		}
		switch {
		case o["decider"] == n["decider"]:
			for _, col := range []string{"expect", "fail_if", "why_not_mech"} {
				if o[col] != n[col] {
					return refuseRule("R4", "row %s: %s may change only when the row is reclassified from judge to bl", id, col)
				}
			}
		case o["decider"] == "judge" && n["decider"] == "bl":
			changed = true
		default:
			return refuseRule("R4", "row %s: the only reclassification an amendment may make is judge → bl", id)
		}
		if o["brief_ref"] != n["brief_ref"] {
			if o["brief_ref"] != "" || o["severity"] != "advisory" {
				return refuseRule("R4", "row %s: an amendment may only fill an empty brief_ref on an advisory row", id)
			}
			changed = true
		}
	}
	if !changed {
		return refuseRule("R4", "the amendment changes nothing")
	}
	return nil
}

func cmdChecks(root string, a args) error {
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
	if err := requireInFlight(order, it); err != nil {
		return err
	}
	// The state guards come FIRST, before the list is read: the claims freeze the list (R5) and
	// the list is written once (R4). A frozen item is therefore refused without waiting on
	// stdin, and a list that was never allowed to be written is not read at all.
	amend := a.flag("amend")
	// R5: the claims freeze the list.
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the check list is frozen", it.ID)
	}
	// R4: the list is written once, and only --amend may change it.
	if it.Checks != "" && !amend {
		return refuseRule("R4", "%s already has a check list — it is written once; only --amend may change it", it.ID)
	}
	if it.Checks == "" && amend {
		return refuseRule("R4", "%s has no check list to amend", it.ID)
	}
	// Only now is the list read — from --file, or from stdin.
	text, err := a.input("file")
	if err != nil {
		return err
	}
	rows, err := parseChecks(text)
	if err != nil {
		return err
	}
	// The list's own rules, in the order §4 gives: R1 (the schema) across all rows, then R34
	// (the command's dialect), then R4 (the amend scope), then R2 (the brief clause), then R3
	// (the judge's justification), then R8 (the expectation form) — so the refusal names the
	// earliest broken rule, and within one rule the first broken row in file order.
	prevSHA, err := validateChecks(root, it, rows, amend)
	if err != nil {
		return err
	}
	rel := itemFile(it.ID, checksName)
	ev := Event{T: "checks", ID: it.ID, Checks: rel, ChecksSHA: shaHex([]byte(text))}
	if amend {
		ev.PrevSHA = prevSHA
	}
	if err := atomicWrite(filepath.Join(root, rel), text); err != nil {
		return err
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if amend {
		fmt.Printf("bl: %s check list amended (%d rows)\n", it.ID, len(rows))
	} else {
		fmt.Printf("bl: %s check list frozen (%d rows)\n", it.ID, len(rows))
	}
	return nil
}

// cmdRebaseline replaces the frozen check list. The new list is validated with the same rules
// `bl checks` uses (R1, R34, R2, R3, R8), and the re-baseline is recorded as a new event with
// the old and the new hash; the earlier checks event keeps its own hash (R24). A re-baseline is
// a NEW PREMISE: it is visible in the ledger, it does not erase the rounds that came before it,
// and it resets the code-round counter so the new premise is not judged against the old one's
// rounds.
//
// The order is the design's, and it is the other way round from `bl checks`: the LIST's rules come
// before the state rules. A bad list is refused under its own rule — R1, R34, R2, R3 or R8 — even
// when the claims freeze the item; only a list that is good on its own reaches the state rules,
// and is then refused under R5 (the claims freeze the list) or R4 (the list is written once).
// R20 (the frozen artefacts are what they were) keeps its place: it is checked before anything
// else on this path.
func cmdRebaseline(root string, a args) error {
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
	if err := requireInFlight(order, it); err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were — before anything else on this path.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	// The list's own rules first, in the order §4 gives: R1 (the schema) across all rows, then
	// R34 (the command's dialect), then R2 (the brief clause), then R3 (the judge's
	// justification), then R8 (the expectation form).
	text, err := a.input("file")
	if err != nil {
		return err
	}
	rows, err := parseChecks(text)
	if err != nil {
		return err
	}
	if _, err := validateChecks(root, it, rows, false); err != nil {
		return err
	}
	// Then the state rules: the claims freeze the list (R5), and the list is written once (R4).
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the check list is frozen", it.ID)
	}
	if it.Checks == "" {
		return refuseRule("R4", "%s has no check list to re-baseline", it.ID)
	}
	rel := itemFile(it.ID, checksName)
	if err := atomicWrite(filepath.Join(root, rel), text); err != nil {
		return err
	}
	ev := Event{
		T: "rebaseline", ID: it.ID, Checks: rel,
		ChecksSHA: shaHex([]byte(text)), PrevSHA: it.ChecksSHA, Reason: reason,
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s check list re-baselined (%d rows)\n", it.ID, len(rows))
	return nil
}

func cmdClaims(root string, a args) error {
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
	if err := requireInFlight(order, it); err != nil {
		return err
	}
	if it.Checks == "" {
		return refuseRule("R5", "%s has no check list — the list is frozen before the claims; run `bl checks %s` first", it.ID, it.ID)
	}
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the claims are frozen once", it.ID)
	}
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	path := a.get("file")
	if path == "" {
		return refuse("--file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return refuse("--file %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return refuse("--file %s is empty", path)
	}
	rel := itemFile(it.ID, claimsName)
	if err := atomicWrite(filepath.Join(root, rel), string(data)); err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "claims", ID: it.ID, Claims: rel, ClaimsSHA: shaHex(data)}); err != nil {
		return err
	}
	fmt.Printf("bl: %s claims recorded\n", it.ID)
	return nil
}

// ── the run ──────────────────────────────────────────────────────────────────

// runTimeout is the timeout of §2: after it the process GROUP is killed, and the row is
// recorded with timed_out = true and exit = -1, which is distinct from any command's own exit
// code — a timeout can never be written up as a pass. It is a variable so a test can shorten it.
var runTimeout = 120 * time.Second

// runRow is one row's record in the run file (§2).
type runRow struct {
	ID           string `json:"id"`
	Cmd          string `json:"cmd"`
	Exit         int    `json:"exit"`
	TimedOut     bool   `json:"timed_out"`
	Signalled    bool   `json:"signalled"`
	Stdout       string `json:"stdout"`
	StdoutSHA    string `json:"stdout_sha256"`
	StdoutBinary bool   `json:"stdout_binary"`
	Stderr       string `json:"stderr"`
	StderrSHA    string `json:"stderr_sha256"`
	StderrBinary bool   `json:"stderr_binary"`
}

// runFile is the run file (§2): the item, the run id, the verified tree at the start and at the
// end of the run, the environment the rows ran in, and every row's record.
type runFile struct {
	ID        string            `json:"id"`
	Run       string            `json:"run"`
	TreeStart string            `json:"tree_start"`
	TreeEnd   string            `json:"tree_end"`
	Env       map[string]string `json:"env"`
	Rows      []runRow          `json:"rows"`
}

// cmdRun executes every row of the frozen check list under the fixed environment (§2, R21),
// writes the run file, and records the run event (R7).
//
// The order is P3's: R20 (the frozen artefacts are what they were) → R26 (the tree did not
// move) → R27 (the ledger did not move) → R1 (the list still parses) → R7 (every row ran). A
// run is refused on the first of these that fails, and no run file is written.
//
// R26 and R27 are comparisons, so they are made once the rows have run; the list is parsed
// before them, because the rows come out of the list and a list that does not parse cannot be
// run at all.
func cmdRun(root string, a args) error {
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
	it, err := liveItem(byID, a.pos(0))
	if err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were. The brief, the check list and the claims are
	// re-hashed and compared with the hash of the latest event that recorded each of them, so a
	// frozen artefact that was edited after the freeze is refused at the point of use.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	// R1: the frozen list still parses.
	if it.Checks == "" {
		return refuseRule("R1", "%s has no check list to run", it.ID)
	}
	list, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	rows, err := parseChecks(string(list))
	if err != nil {
		return err
	}
	if err := validateR1(rows); err != nil {
		return err
	}
	// The state before the run: the verified tree, computed from the FILESYSTEM, and the hash of
	// the whole of .backlog/.
	treeStart, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerBefore, err := backlogHash(root)
	if err != nil {
		return err
	}
	// The scratch directory: outside the repository, and removed after the run.
	scratch, err := scratchDir(root)
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch)
	home := filepath.Join(scratch, "home")
	tmpdir := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, tmpdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	// R21: the environment is fixed. PATH is inherited from the caller and recorded; HOME and
	// TMPDIR point at the scratch directory; LANG=C; TZ=UTC; and nothing else — in particular no
	// GIT_* and no other variable of the caller's.
	path := os.Getenv("PATH")
	env := []string{"PATH=" + path, "HOME=" + home, "TMPDIR=" + tmpdir, "LANG=C", "TZ=UTC"}
	// R7: every row runs, in the order of the frozen list, whatever the decider.
	records := make([]runRow, 0, len(rows))
	for _, row := range rows {
		records = append(records, execRow(root, row, env, runTimeout))
	}
	// The state after the run.
	treeEnd, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerAfter, err := backlogHash(root)
	if err != nil {
		return err
	}
	// R26: the tree did not move under the run. A row's command changed the tree, so the rows
	// were verified against a state that no longer exists.
	if treeEnd != treeStart {
		return refuseRule("R26", "the tree moved under the run: %s before, %s after — a row's command changed the tree the rows ran against", treeStart, treeEnd)
	}
	// R27: the ledger did not move under the run. A row's command wrote to the ledger, the
	// evidence or the item files, and the run's own record would have been written by the thing
	// it is recording.
	if ledgerAfter != ledgerBefore {
		return refuseRule("R27", "the %s directory changed under the run: %s before, %s after — a row's command wrote to the ledger, the evidence or the item files", backlogDir, ledgerBefore, ledgerAfter)
	}
	// The run file: .backlog/evidence/<id>-run<N>.json, N being the run's number for the item.
	n := len(it.Runs) + 1
	runID := fmt.Sprintf("run%d", n)
	name := fmt.Sprintf("%s-run%d.json", it.ID, n)
	rf := runFile{
		ID: it.ID, Run: runID,
		TreeStart: treeStart, TreeEnd: treeEnd,
		Env: map[string]string{
			"PATH": path, "HOME": home, "TMPDIR": tmpdir, "LANG": "C", "TZ": "UTC",
		},
		Rows: records,
	}
	data, err := encodeRun(rf)
	if err != nil {
		return err
	}
	p := newPaths(root)
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(p.evidence, name), string(data)); err != nil {
		return err
	}
	// The ledger records the run and the hash of the file it names (R20).
	ev := Event{T: "run", ID: it.ID, Run: runID, RunSHA: shaHex(data)}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	failed := 0
	for _, r := range records {
		if r.Exit != 0 || r.TimedOut {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("bl: %s %s written (%d rows, %d failed)\n", it.ID, runID, len(records), failed)
		return errFailed
	}
	fmt.Printf("bl: %s %s written (%d rows)\n", it.ID, runID, len(records))
	return nil
}

// encodeRun writes the run file as JSON: indented, and without HTML escaping, so the bytes the
// judge reads are the bytes the row produced.
func encodeRun(rf runFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// execRow runs one row's command and records what it saw: the exit code, whether it timed out,
// the full output of each stream, and the sha256 of each.
//
// A command that exits non-zero, a command that cannot be run at all and a death by a signal are
// each recorded as what they are — never swallowed, never turned into a pass.
func execRow(root string, row checkRow, env []string, timeout time.Duration) runRow {
	rec := runRow{ID: row.f["id"], Cmd: row.f["cmd"]}
	cmd := exec.Command("/bin/sh", "-c", row.f["cmd"])
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdin = nil // /dev/null
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// Its own process group, so a timeout kills the children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		// the command could not be run at all
		rec.Exit = 127
		rec.Stderr = err.Error() + "\n"
		rec.StderrSHA = shaHex([]byte(rec.Stderr))
		return rec
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		rec.Exit, rec.Signalled = exitCodeOf(err)
	case <-time.After(timeout):
		// the process GROUP is killed, and the row is recorded as a timeout: exit = -1, which
		// is distinct from any command's own exit code.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		rec.Exit = -1
		rec.TimedOut = true
	}
	rec.Stdout, rec.StdoutBinary = encodeStream(out.Bytes())
	rec.Stderr, rec.StderrBinary = encodeStream(errOut.Bytes())
	rec.StdoutSHA = shaHex(out.Bytes())
	rec.StderrSHA = shaHex(errOut.Bytes())
	return rec
}

// exitCodeOf reads the exit code of a finished command. A process killed by a signal is
// recorded as 128+signal, and a command that exits 137 on its own is recorded as 137 — the
// signalled flag is what tells the two apart.
func exitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 127, false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), true
	}
	code := ee.ExitCode()
	if code < 0 {
		return 127, false
	}
	return code, false
}

// encodeStream stores a captured stream as text. Output that is not valid UTF-8 is stored
// base64, with the binary flag set (§2).
func encodeStream(data []byte) (string, bool) {
	if utf8.Valid(data) {
		return string(data), false
	}
	return base64.StdEncoding.EncodeToString(data), true
}

// scratchDir creates the run's scratch directory: outside the repository, and removed after the
// run (§2). HOME and TMPDIR point into it, so a tool that caches under HOME cannot write into
// the tree.
func scratchDir(root string) (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp", "/private/tmp"} {
		if base == "" {
			continue
		}
		dir, err := os.MkdirTemp(base, "bl-run-")
		if err != nil {
			continue
		}
		if inside(root, dir) {
			os.RemoveAll(dir)
			continue
		}
		return dir, nil
	}
	return "", refuse("cannot create a scratch directory outside the repository")
}

// inside reports whether path lies under root.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// backlogHash hashes the whole of .backlog/ — every file's path and content — so that a row
// that writes to the ledger, the evidence or the item files is caught (R27).
func backlogHash(root string) (string, error) {
	base := filepath.Join(root, backlogDir)
	var files []string
	err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), shaHex(data))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// ── the verified tree ────────────────────────────────────────────────────────

// The modes git writes in a tree.
const (
	treeMode = "40000"  // a directory
	fileMode = "100644" // a regular file
	execMode = "100755" // a regular file with the owner's execute bit
	linkMode = "120000" // a symbolic link
)

// treeEntry is one entry of a tree: its mode, its name, and the sha of what it points at.
type treeEntry struct {
	name string
	mode string
	sha  string
}

// verifiedTree computes the verified tree from the FILESYSTEM (§2): every file of the working
// directory — except .git/, the artefacts of R25, and the paths .gitignore excludes — is hashed
// with `git hash-object`, and the entries are written as a tree.
//
// The index is never consulted, so a staged-but-unstaged path, a skip-worktree path and an
// assume-unchanged path cannot hide a change from the tree (R26).
func verifiedTree(root string) (string, error) {
	b := &treeBuilder{root: root, ignored: map[string]bool{}}
	if err := b.findIgnored(); err != nil {
		return "", err
	}
	sha, ok, err := b.tree("")
	if err != nil {
		return "", err
	}
	if !ok {
		// an empty working tree is git's empty tree
		return gitStdin(root, "tree", nil)
	}
	return sha, nil
}

// treeBuilder computes a tree from the files on disk.
type treeBuilder struct {
	root    string
	ignored map[string]bool
}

// findIgnored asks git which of the working tree's paths .gitignore excludes. They are asked
// for in one call: `git check-ignore -z --stdin`.
func (b *treeBuilder) findIgnored() error {
	var paths []string
	err := filepath.WalkDir(b.root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" || (p == filepath.Join(b.root, backlogDir)) {
				return fs.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(b.root, p)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	cmd := exec.Command("git", "check-ignore", "-z", "--stdin")
	cmd.Dir = b.root
	cmd.Stdin = strings.NewReader(strings.Join(paths, "\x00") + "\x00")
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && (ee.ExitCode() == 1 || ee.ExitCode() == 128) {
			// 1: nothing is ignored. 128: not a git repository, so there are no rules.
			return nil
		}
		return err
	}
	for _, p := range strings.Split(string(out), "\x00") {
		if p != "" {
			b.ignored[p] = true
		}
	}
	return nil
}

// tree computes the tree of one directory, from the files on disk. It returns false when the
// directory holds nothing git can store: git has no empty directories.
func (b *treeBuilder) tree(rel string) (string, bool, error) {
	dir := filepath.Join(b.root, filepath.FromSlash(rel))
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", false, err
	}
	var list []treeEntry
	for _, e := range ents {
		name := e.Name()
		if rel == "" && name == backlogDir {
			continue
		}
		if e.IsDir() && name == ".git" {
			// git's own metadata is never part of a tree
			continue
		}
		child := name
		if rel != "" {
			child = rel + "/" + name
		}
		if b.ignored[child] {
			continue
		}
		if e.IsDir() {
			sha, ok, err := b.tree(child)
			if err != nil {
				return "", false, err
			}
			if !ok {
				continue
			}
			list = append(list, treeEntry{name, treeMode, sha})
			continue
		}
		sha, mode, err := b.blob(child)
		if err != nil {
			return "", false, err
		}
		list = append(list, treeEntry{name, mode, sha})
	}
	if len(list) == 0 {
		return "", false, nil
	}
	sortTree(list)
	var buf bytes.Buffer
	for _, e := range list {
		fmt.Fprintf(&buf, "%s %s\x00", e.mode, e.name)
		raw, err := hex.DecodeString(e.sha)
		if err != nil {
			return "", false, err
		}
		buf.Write(raw)
	}
	sha, err := gitStdin(b.root, "tree", buf.Bytes())
	if err != nil {
		return "", false, err
	}
	return sha, true, nil
}

// blob hashes one file with `git hash-object` and names the mode git stores it under. A
// symbolic link is stored as the blob of its target.
func (b *treeBuilder) blob(rel string) (string, string, error) {
	full := filepath.Join(b.root, filepath.FromSlash(rel))
	st, err := os.Lstat(full)
	if err != nil {
		return "", "", err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(full)
		if err != nil {
			return "", "", err
		}
		sha, err := gitStdin(b.root, "blob", []byte(target))
		if err != nil {
			return "", "", err
		}
		return sha, linkMode, nil
	}
	if !st.Mode().IsRegular() {
		return "", "", refuse("%s is not a regular file — git cannot store it", rel)
	}
	mode := fileMode
	if st.Mode().Perm()&0o100 != 0 {
		mode = execMode
	}
	out, code := git(b.root, "hash-object", "--", rel)
	if code != 0 {
		return "", "", refuse("git hash-object %s: exit %d", rel, code)
	}
	return strings.TrimSpace(out), mode, nil
}

// sortTree orders the entries of a tree the way git does: by name, and a directory is compared
// as if its name ended in a slash, so "foo.bar" sorts before the directory "foo".
func sortTree(list []treeEntry) {
	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		n := len(a.name)
		if len(b.name) < n {
			n = len(b.name)
		}
		if c := strings.Compare(a.name[:n], b.name[:n]); c != 0 {
			return c < 0
		}
		ca, cb := byte(0), byte(0)
		if len(a.name) > n {
			ca = a.name[n]
		} else if a.mode == treeMode {
			ca = '/'
		}
		if len(b.name) > n {
			cb = b.name[n]
		} else if b.mode == treeMode {
			cb = '/'
		}
		return ca < cb
	})
}

// gitStdin hashes data as a git object of the given type WITHOUT writing it: `git hash-object
// -t <type> --stdin` computes the id and leaves the object store alone.
func gitStdin(root, typ string, data []byte) (string, error) {
	cmd := exec.Command("git", "hash-object", "-t", typ, "--stdin")
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git hash-object -t %s: exit %d: %s", typ, ee.ExitCode(), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func cmdRound(root string, a args) error {
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
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur == nil || !strings.EqualFold(cur.ID, it.ID) {
		return refuse("%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
	}
	// R20: the frozen artefacts are what they were when the round was written against.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	// R23 (P2): a harness round has no rows and stops here. The check sits right after R20 and
	// before everything else on this path, so a harness round on tampered frozen artefacts is
	// refused under R20, not R23.
	if err := harnessRowsRefused(a); err != nil {
		return err
	}
	// R24: an amendment that removed a clause a frozen row cites refuses the round — the row's
	// criterion no longer exists in the brief it was written against.
	if err := verifyBriefRefs(root, it); err != nil {
		return err
	}
	verdict := strings.ToUpper(strings.TrimSpace(a.get("verdict")))
	if verdict != "PASS" && verdict != "FAIL" {
		return refuse("--verdict must be one of %s", strings.Join(verdicts, ", "))
	}
	cause := strings.ToLower(strings.TrimSpace(a.get("cause")))
	if verdict == "FAIL" && cause != "code" && cause != "harness" && cause != "dispute" {
		return refuse("a FAIL needs --cause %s", strings.Join(causes, "|"))
	}
	if verdict == "PASS" && cause != "" {
		return refuse("a PASS carries no cause")
	}
	// R18: the item is complete. Its last round is a PASS, so the only legal next step is the
	// close, and a further round is refused — whatever is left of the budget. The guard comes
	// BEFORE the budget (R19), so the refusal names the state of the item and not the
	// arithmetic.
	if err := completeRefused(it); err != nil {
		return err
	}
	// R19: the item's whole round budget. Three code rounds, three harness rounds and three
	// dispute rounds per item, and six rounds in total, of any cause — a PASS round counts
	// too. The counts are over the item's whole life, so a re-baseline resets none of them.
	if err := budgetRefused(it, cause); err != nil {
		return err
	}
	// R23: a harness round carries no rows — the verifier did not run, so there is nothing to
	// record and nothing to hash. A rows file with content was already refused at the R23-harness
	// step, so what reaches here is a harness round with no rows file or with an empty one, and
	// it is recorded with no rows. A code or a dispute round is a verdict about rows, so it needs
	// them, and they must not be empty. A PASS round carries no cause and no finding: it may be
	// written against its evidence, and it may be recorded without any.
	rows := ""
	if cause != "harness" {
		if a.get("rows") != "" || verdict == "FAIL" {
			var err error
			rows, err = a.rows()
			if err != nil {
				return err
			}
			rows = strings.TrimRight(rows, "\r\n")
			if strings.TrimSpace(rows) == "" {
				return refuse("--rows is empty: a verdict exists only after commands have run")
			}
		}
	}
	p := newPaths(root)
	ev := Event{T: "round", ID: it.ID, Verdict: verdict, Cause: cause}
	// a round that carries no rows — a harness round, or a PASS written without any — has no
	// evidence file to hash, so none is written.
	if rows != "" {
		if err := os.MkdirAll(p.evidence, 0o755); err != nil {
			return err
		}
		name := fmt.Sprintf("%s-r%d.txt", it.ID, len(it.Rounds)+1)
		if err := atomicWrite(filepath.Join(p.evidence, name), rows+"\n"); err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(rows + "\n"))
		ev.Rows = backlogDir + "/" + evidenceDir + "/" + name
		ev.RowsSHA = hex.EncodeToString(sum[:])
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if cause != "" {
		fmt.Printf("bl: %s r%d %s (%s)\n", it.ID, len(it.Rounds)+1, verdict, cause)
	} else {
		fmt.Printf("bl: %s r%d %s\n", it.ID, len(it.Rounds)+1, verdict)
	}
	return nil
}

// harnessRowsRefused is R23's harness clause, at the place P2 gives it: a harness round has no
// rows and stops here. A harness round carries no rows because the verifier did not run, so a
// rows file with content is refused and the round is not recorded at all. A harness round with no
// rows file, or with an empty one, is accepted and is recorded with no rows.
//
// The rows file is read to see whether it holds rows. A file that cannot be read holds none, so
// it is the design's "absent" case: the round is accepted and recorded with no rows, as it was
// before.
func harnessRowsRefused(a args) error {
	if strings.ToLower(strings.TrimSpace(a.get("cause"))) != "harness" {
		return nil
	}
	if strings.ToUpper(strings.TrimSpace(a.get("verdict"))) != "FAIL" {
		// a PASS round carries no cause at all, and that refusal is raised where the verdict is
		// read — this clause is about a harness round, and a PASS round is not one.
		return nil
	}
	path := a.get("rows")
	if path == "" {
		return nil
	}
	rows, err := a.rows()
	if err != nil || strings.TrimSpace(rows) == "" {
		return nil
	}
	what := "the rows file " + path
	if path == "-" {
		what = "the rows on stdin"
	}
	return refuseRule("R23", "a harness round carries no rows — the verifier did not run, so %s must be empty", what)
}

// completeRefused is R18: the item is complete. Its last round is a PASS, so the only legal
// next step is the close, and a further round is refused. The refusal names R18 and says the
// item already passed.
//
// The guard does not look at the budget: a PASS ends the item's rounds, whatever is left of
// the six (R19). What is left of the budget is R19's business, not the state's.
func completeRefused(it *Item) error {
	last := it.last()
	if last == nil || last.Verdict != "PASS" {
		return nil
	}
	return refuseRule("R18", "%s already passed — close it", it.ID)
}

// budgetRefused is R19: the round budget of one item, counted over its whole life. Three
// code rounds, three harness rounds and three dispute rounds, and six rounds in total, of any
// cause. A re-baseline is a new premise, not a new item, so it resets no count, and a PASS
// round counts towards the total because it is a round. A refusal names R19 and says the item
// escalates to a human; it writes nothing.
func budgetRefused(it *Item, cause string) error {
	if cause != "" {
		if n := it.causeRounds(cause); n >= roundCap {
			return refuseRule("R19", "%s has spent %d %s rounds (cap %d) — escalate to a human", it.ID, n, cause, roundCap)
		}
	}
	if n := len(it.Rounds); n >= totalCap {
		return refuseRule("R19", "%s has spent %d rounds in total (cap %d) — escalate to a human", it.ID, n, totalCap)
	}
	return nil
}

func cmdClose(root string, a args) error {
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
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur == nil || !strings.EqualFold(cur.ID, it.ID) {
		return refuse("%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
	}
	// R20: the frozen artefacts are what they were when the round was written against.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	// R24: the rows the last round was written against must still resolve against the brief, or
	// the round closed on a criterion the brief no longer holds.
	if err := verifyBriefRefs(root, it); err != nil {
		return err
	}
	if last := it.last(); last == nil || last.Verdict != "PASS" {
		return refuse("%s has no PASS round — nothing to close", it.ID)
	}
	commit, err := cleanText(a.get("commit"), "commit")
	if err != nil {
		return err
	}
	if !isGit(root) {
		return refuse("not a git repository — a close needs a commit to point at")
	}
	out, code := git(root, "rev-parse", "--verify", commit+"^{commit}")
	if code != 0 {
		return refuse("commit %s does not exist in this repository", commit)
	}
	commit = strings.TrimSpace(out)
	statusOut, _ := git(root, "status", "--porcelain")
	var dirty []string
	for _, line := range strings.Split(statusOut, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rel := strings.TrimSpace(line[3:])
		if !isArtifact(rel) {
			dirty = append(dirty, line)
		}
	}
	if len(dirty) > 0 {
		shown := dirty
		if len(shown) > 3 {
			shown = shown[:3]
		}
		return refuse("working tree is dirty — commit or stash before closing: %s", strings.Join(shown, "; "))
	}
	if _, err := appendEvent(root, Event{T: "close", ID: it.ID, Commit: commit}); err != nil {
		return err
	}
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Printf("bl: closed %s at %s\n", it.ID, short)
	return nil
}

func cmdDrop(root string, a args) error {
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
	it, err := liveItem(byID, a.pos(0))
	if err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	by := a.get("by")
	if by == "" {
		by = "human"
	}
	if _, err := appendEvent(root, Event{T: "drop", ID: it.ID, Reason: reason, By: by}); err != nil {
		return err
	}
	fmt.Printf("bl: declined %s\n", it.ID)
	return nil
}

func cmdDefer(root string, a args) error {
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
	it, err := liveItem(byID, a.pos(0))
	if err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "defer", ID: it.ID, Reason: reason}); err != nil {
		return err
	}
	fmt.Printf("bl: parked %s\n", it.ID)
	return nil
}

func cmdReopen(root string, a args) error {
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
	if it.Status != "declined" && it.Status != "parked" {
		return refuse("%s is %s — only a declined or parked item can be reopened", it.ID, it.Status)
	}
	ev := Event{T: "reopen", ID: it.ID}
	if r := a.get("reason"); r != "" {
		reason, err := cleanText(r, "reason")
		if err != nil {
			return err
		}
		ev.Reason = reason
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s back in the queue\n", it.ID)
	return nil
}

func cmdNote(root string, a args) error {
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
	text, err := cleanText(a.get("text"), "text")
	if err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "note", ID: it.ID, Text: text}); err != nil {
		return err
	}
	fmt.Printf("bl: noted on %s\n", it.ID)
	return nil
}

func cmdNext(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	order, _, err := derive(events)
	if err != nil {
		return err
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur != nil {
		fmt.Printf("%s\t%s\n", cur.ID, cur.Text)
		return nil
	}
	rows := eligible(order)
	if len(rows) == 0 {
		fmt.Println("none")
		return nil
	}
	fmt.Printf("%s\t%s\n", rows[0].ID, rows[0].Text)
	return nil
}

func cmdShow(root string, a args) error {
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
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(it)
	}
	fmt.Printf("%s — %s\n", it.ID, it.Text)
	status := it.Status
	if it.BlockedBy != "" {
		status += " (blocked by " + it.BlockedBy + ")"
	}
	fmt.Printf("status: %s\n", status)
	if it.After != "" {
		fmt.Printf("after: %s\n", it.After)
	}
	if it.Source != "" {
		fmt.Printf("source: %s\n", it.Source)
	}
	if it.Commit != "" {
		fmt.Printf("commit: %s\n", it.Commit)
	}
	if it.Reason != "" {
		fmt.Printf("reason: %s\n", it.Reason)
	}
	fmt.Printf("%s\n", roundCounts(it))
	for i, r := range it.Rounds {
		cause := ""
		if r.Cause != "" {
			cause = " cause=" + r.Cause
		}
		rows := r.Rows
		if rows == "" {
			rows = "no rows"
		}
		fmt.Printf("  r%d %s%s %s\n", i+1, r.Verdict, cause, rows)
	}
	for _, n := range it.Notes {
		fmt.Printf("  note: %s\n", n)
	}
	return nil
}

func cmdStatus(root string, a args) error {
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
	counts := map[string]int{}
	for _, it := range order {
		key := it.Status
		if it.BlockedBy != "" {
			key = "blocked"
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, counts[k])
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	inFlight := "none"
	if cur != nil {
		inFlight = cur.ID
	}
	if len(parts) == 0 {
		fmt.Printf("items %d\n", len(order))
	} else {
		fmt.Printf("items %d · %s\n", len(order), strings.Join(parts, " · "))
	}
	fmt.Printf("in flight: %s\n", inFlight)
	// R19: the round counts are part of the status — the total and how many of each cause,
	// so the harness and dispute rounds are visible.
	if id := a.pos(0); id != "" {
		it, err := lookup(byID, id)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", it.ID, roundCounts(it))
		return nil
	}
	for _, it := range order {
		if len(it.Rounds) == 0 {
			continue
		}
		fmt.Printf("%s %s\n", it.ID, roundCounts(it))
	}
	return nil
}

// roundCounts is the item's round budget as spent: the rounds in total and how many of each
// cause (R19). A PASS carries no cause, so it is counted on its own — the total is every
// round of every cause, and a PASS is a round. A re-baseline resets none of these counts.
func roundCounts(it *Item) string {
	return fmt.Sprintf("rounds: %d (code %d, harness %d, dispute %d, pass %d)",
		len(it.Rounds), it.causeRounds("code"), it.causeRounds("harness"),
		it.causeRounds("dispute"), it.passRounds())
}

func cmdRender(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	if err := render(root, events); err != nil {
		return err
	}
	fmt.Printf("bl: rendered %s and %s\n", queueView, recordView)
	return nil
}

func cmdCheck(root string, a args) error {
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
	var problems []string
	for _, it := range order {
		// R20: re-hash the frozen artefacts and refuse when one no longer matches the hash of
		// the latest event that recorded it.
		if err := verifyFrozen(root, it); err != nil {
			return err
		}
		// R24: a frozen row whose brief_ref names a clause the current brief no longer holds is
		// a refusal — an amendment removed it, so the row's criterion no longer exists.
		if err := verifyBriefRefs(root, it); err != nil {
			return err
		}
		if it.Status == "closed" {
			if last := it.last(); last == nil || last.Verdict != "PASS" {
				problems = append(problems, it.ID+" is closed without a PASS round")
			}
		}
		for _, r := range it.Rounds {
			if r.Rows == "" {
				// a harness round carries no rows, so there is no evidence file to verify
				continue
			}
			sum := sha256.Sum256(mustRead(filepath.Join(root, r.Rows)))
			if _, err := os.Stat(filepath.Join(root, r.Rows)); err != nil {
				problems = append(problems, it.ID+" round "+r.Rows+" is missing")
			} else if hex.EncodeToString(sum[:]) != r.RowsSHA {
				problems = append(problems, it.ID+" round "+r.Rows+" does not match its hash")
			}
		}
		// R20: the run file is a frozen artefact too — it is hashed into the ledger event that
		// names it, and `bl check` re-hashes it.
		for _, r := range it.Runs {
			rel, ok := runFileRel(it.ID, r.Run)
			if !ok {
				problems = append(problems, it.ID+" run "+r.Run+" names no run file")
				continue
			}
			data, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				problems = append(problems, it.ID+" run "+rel+" is missing")
			} else if shaHex(data) != r.RunSHA {
				problems = append(problems, it.ID+" run "+rel+" does not match its hash")
			}
		}
		if it.BlockedBy != "" {
			if _, ok := byID[strings.ToLower(it.BlockedBy)]; !ok {
				problems = append(problems, it.ID+" waits on unknown item "+it.BlockedBy)
			}
		}
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if a.flag("views") {
		p := newPaths(root)
		want := map[string]string{
			p.queue:  renderQueue(root, order),
			p.record: renderRecords(root, order),
		}
		names := make([]string, 0, len(want))
		for path := range want {
			names = append(names, path)
		}
		sort.Strings(names)
		for _, path := range names {
			data, err := os.ReadFile(path)
			if err != nil {
				problems = append(problems, path+" is missing")
				continue
			}
			if string(data) != want[path] {
				problems = append(problems, path+" is stale or hand-edited")
			}
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Printf("FAIL %s\n", p)
		}
		return errFailed
	}
	inFlight := "none"
	if cur != nil {
		inFlight = cur.ID
	}
	checked := "not checked"
	if a.flag("views") {
		checked = "checked"
	}
	fmt.Printf("PASS %d events · %d items · in flight %s · views %s\n", len(events), len(order), inFlight, checked)
	return nil
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

// errFailed signals a check that found problems without refusing the call.
var errFailed = errors.New("check found problems")

// runFileRel is the path of a run file: .backlog/evidence/<id>-run<N>.json, derived from the run
// id `run<N>`. The run event does not carry the path — it is derivable, and one derivation is
// one thing to get right.
func runFileRel(id, run string) (string, bool) {
	if !strings.HasPrefix(run, "run") || strings.TrimPrefix(run, "run") == "" {
		return "", false
	}
	return backlogDir + "/" + evidenceDir + "/" + id + "-" + run + ".json", true
}

// ── schema ───────────────────────────────────────────────────────────────────

func schemaJSON() map[string]any {
	return map[string]any{
		"version":    1,
		"ledger":     backlogDir + "/" + ledgerName,
		"runlog":     backlogDir + "/" + runlogName,
		"id_pattern": idPattern.String(),
		"verdicts":   verdicts,
		"causes":     causes,
		"round_cap":  roundCap,
		"statuses":   []string{"open", "blocked", "closed", "declined", "parked"},
		"events": map[string]map[string][]string{
			"file":       {"required": {"id", "text"}, "optional": {"after", "source"}},
			"open":       {"required": {"id"}, "optional": {"brief", "brief_sha"}},
			"brief":      {"required": {"id", "brief", "brief_sha"}, "optional": {}},
			"checks":     {"required": {"id", "checks", "checks_sha"}, "optional": {"prev_checks_sha"}},
			"rebaseline": {"required": {"id", "checks", "checks_sha"}, "optional": {"prev_checks_sha", "reason"}},
			"claims":     {"required": {"id", "claims", "claims_sha"}, "optional": {}},
			"run":        {"required": {"id", "run", "run_sha"}, "optional": {}},
			"round":      {"required": {"id", "verdict"}, "optional": {"cause", "rows", "rows_sha"}},
			"close":      {"required": {"id", "commit"}, "optional": {}},
			"drop":       {"required": {"id", "reason"}, "optional": {"by"}},
			"defer":      {"required": {"id", "reason"}, "optional": {}},
			"reopen":     {"required": {"id"}, "optional": {"reason"}},
			"note":       {"required": {"id", "text"}, "optional": {}},
		},
	}
}

func cmdSchema(root string, a args) error {
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(schemaJSON())
	}
	order := []string{"file", "open", "brief", "checks", "rebaseline", "claims", "run", "round", "close", "drop", "defer", "reopen", "note"}
	spec := schemaJSON()["events"].(map[string]map[string][]string)
	// The count is part of the output: a reader (and a grep) must be able to tell 13 kinds
	// from 12 without counting the lines.
	fmt.Printf("bl schema v1 — %s/%s · %d event kinds\n", backlogDir, ledgerName, len(order))
	for _, name := range order {
		s := spec[name]
		opt := strings.Join(s["optional"], ", ")
		if opt == "" {
			opt = "—"
		}
		fmt.Printf("  %-7s required: %-28s optional: %s\n", name, strings.Join(s["required"], ", "), opt)
	}
	return nil
}

// ── audit ────────────────────────────────────────────────────────────────────

func parseSince(value string) (time.Duration, error) {
	m := periodRe.FindStringSubmatch(strings.TrimSpace(value))
	if m == nil {
		return 0, refuse("--since expects e.g. 2d, 36h, 1w")
	}
	n, _ := strconv.Atoi(m[1])
	switch m[2] {
	case "h":
		return time.Duration(n) * time.Hour, nil
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	default:
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	}
}

type runRecord struct {
	At   string   `json:"at"`
	Argv []string `json:"argv"`
	Exit int      `json:"exit"`
	MS   int64    `json:"ms"`
}

func readRunlog(root string) []runRecord {
	data, err := os.ReadFile(newPaths(root).runlog)
	if err != nil {
		return nil
	}
	var out []runRecord
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rec runRecord
		if json.Unmarshal([]byte(line), &rec) == nil {
			out = append(out, rec)
		}
	}
	return out
}

const auditCadence = 48 * time.Hour

// lastAudit finds the newest report in the audit directory.
func lastAudit(dir string) (time.Time, bool) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	var newest time.Time
	found := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var report struct {
			Generated string `json:"generated"`
		}
		if json.Unmarshal(data, &report) != nil || report.Generated == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, report.Generated)
		if err != nil {
			continue
		}
		if !found || t.After(newest) {
			newest, found = t, true
		}
	}
	return newest, found
}

func humanAge(d time.Duration) string {
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func cmdAudit(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
	}
	outDir := filepath.Join(root, defaultStr(a.get("out"), "audit"))
	if a.flag("due") {
		last, ok := lastAudit(outDir)
		if !ok {
			fmt.Println("due: no audit yet")
			return nil
		}
		age := time.Since(last)
		if age >= auditCadence {
			fmt.Printf("due: last audit %s ago\n", humanAge(age))
		} else {
			fmt.Printf("not due: last audit %s ago (cadence %s)\n", humanAge(age), humanAge(auditCadence))
		}
		return nil
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	order, _, err := derive(events)
	if err != nil {
		return err
	}
	window, err := parseSince(defaultStr(a.get("since"), "2d"))
	if err != nil {
		return err
	}
	cutoff := time.Now().UTC().Add(-window)
	inWindow := func(at string) bool {
		t, err := time.Parse(time.RFC3339, at)
		return err == nil && !t.Before(cutoff)
	}
	counts := map[string]int{}
	for _, ev := range events {
		if inWindow(ev.At) {
			counts[ev.T]++
			if ev.T == "round" && ev.Verdict == "FAIL" {
				counts["fail_"+ev.Cause]++
			}
		}
	}
	var runs []runRecord
	refusals := 0
	var slowest []int64
	for _, r := range readRunlog(root) {
		if inWindow(r.At) {
			runs = append(runs, r)
			if r.Exit == 2 {
				refusals++
			}
			slowest = append(slowest, r.MS)
		}
	}
	sort.Slice(slowest, func(i, j int) bool { return slowest[i] > slowest[j] })
	if len(slowest) > 5 {
		slowest = slowest[:5]
	}
	roundsPerItem := map[string]int{}
	var neverFailed, flips []string
	for _, it := range order {
		for _, r := range it.Rounds {
			if inWindow(r.At) {
				roundsPerItem[it.ID]++
			}
		}
		if len(it.Rounds) >= 2 {
			allPass := true
			for _, r := range it.Rounds {
				if r.Verdict != "PASS" {
					allPass = false
				}
			}
			if allPass {
				neverFailed = append(neverFailed, it.ID)
			}
			for i := 0; i+1 < len(it.Rounds); i++ {
				if it.Rounds[i].Verdict == "PASS" && it.Rounds[i+1].Verdict == "FAIL" {
					flips = append(flips, it.ID)
					break
				}
			}
		}
	}
	report := map[string]any{
		"since":                    a.get("since"),
		"generated":                now(),
		"events":                   sumOf(counts),
		"items_filed":              counts["file"],
		"items_closed":             counts["close"],
		"rounds":                   counts["round"],
		"code_fails":               counts["fail_code"],
		"harness_fails":            counts["fail_harness"],
		"refused_calls":            refusals,
		"rounds_per_item":          roundsPerItem,
		"checks_that_never_failed": neverFailed,
		"verdict_flips":            flips,
		"declined":                 counts["drop"],
		"parked":                   counts["defer"],
		"slowest_calls_ms":         slowest,
		"calls":                    len(runs),
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	stamp := time.Now().UTC().Format("2006-01-02")
	blob, _ := json.MarshalIndent(report, "", "  ")
	if err := atomicWrite(filepath.Join(outDir, stamp+".json"), string(blob)+"\n"); err != nil {
		return err
	}
	var md strings.Builder
	fmt.Fprintf(&md, "# audit — %s (last %s)\n\n## arithmetic over the ledger\n\n", stamp, a.get("since"))
	for _, key := range []string{"events", "items_filed", "items_closed", "rounds", "code_fails", "harness_fails", "refused_calls", "declined", "parked"} {
		fmt.Fprintf(&md, "- %s: %v\n", strings.ReplaceAll(key, "_", " "), report[key])
	}
	md.WriteString("\n## candidates for review\n\n")
	any := false
	if len(neverFailed) > 0 {
		fmt.Fprintf(&md, "- checks that never failed: %s — are they doing work?\n", strings.Join(neverFailed, ", "))
		any = true
	}
	if len(flips) > 0 {
		fmt.Fprintf(&md, "- verdict flips on the same item: %s — flaky or real?\n", strings.Join(flips, ", "))
		any = true
	}
	if refusals > 0 {
		fmt.Fprintf(&md, "- %d refused calls — protocol friction or wrong schema?\n", refusals)
		any = true
	}
	if counts["fail_harness"] > 0 {
		fmt.Fprintf(&md, "- %d harness failures — fix the harness, not the item\n", counts["fail_harness"])
		any = true
	}
	if !any {
		md.WriteString("- nothing anomalous in this window\n")
	}
	md.WriteString("\n## the model's half\n\n")
	md.WriteString("Read this plus the raw window, then file what you recommend as ordinary items:\n")
	md.WriteString("`bl file --id O<n> --text \"…\" --source audit:<date>` then `bl defer O<n> --reason \"awaiting triage\"`.\n")
	md.WriteString("Dedupe first: skip findings that already exist as open or parked items with an `audit:` source.\n")
	if err := atomicWrite(filepath.Join(outDir, stamp+".md"), md.String()); err != nil {
		return err
	}
	if a.flag("json") {
		fmt.Println(string(blob))
		return nil
	}
	fmt.Printf("bl: audit %s → %s/%s.md · %d rounds, %d refusals, %d code FAIL\n",
		a.get("since"), outDir, stamp, counts["round"], refusals, counts["fail_code"])
	return nil
}

func sumOf(m map[string]int) int {
	n := 0
	for _, v := range m {
		n += v
	}
	return n
}

func defaultStr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

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

// ── cli ──────────────────────────────────────────────────────────────────────

type args struct {
	flags      map[string]string
	boolFlags  map[string]bool
	positional []string
}

func (a args) get(name string) string { return a.flags[name] }
func (a args) flag(name string) bool  { return a.boolFlags[name] }

func (a args) pos(i int) string {
	if i < len(a.positional) {
		return a.positional[i]
	}
	return ""
}

func (a args) rows() (string, error) {
	path := a.get("rows")
	if path == "" {
		return "", refuse("--rows is required")
	}
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", refuse("--rows %s: %v", path, err)
	}
	return string(data), nil
}

// input reads the file named by a flag, or stdin when the flag is absent.
func (a args) input(name string) (string, error) {
	path := a.get(name)
	if path == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", refuse("--%s %s: %v", name, path, err)
	}
	return string(data), nil
}

var boolFlagNames = map[string]bool{"json": true, "views": true, "due": true, "amend": true}

func parseArgs(raw []string) (args, error) {
	a := args{flags: map[string]string{}, boolFlags: map[string]bool{}}
	for i := 0; i < len(raw); i++ {
		tok := raw[i]
		if strings.HasPrefix(tok, "--") {
			name := strings.TrimPrefix(tok, "--")
			if eq := strings.Index(name, "="); eq >= 0 {
				a.flags[name[:eq]] = name[eq+1:]
				continue
			}
			if boolFlagNames[name] {
				a.boolFlags[name] = true
				continue
			}
			if i+1 >= len(raw) {
				return a, refuse("--%s needs a value", name)
			}
			a.flags[name] = raw[i+1]
			i++
			continue
		}
		a.positional = append(a.positional, tok)
	}
	return a, nil
}

var handlers = map[string]func(string, args) error{
	"init":       cmdInit,
	"file":       cmdFile,
	"open":       cmdOpen,
	"brief":      cmdBrief,
	"checks":     cmdChecks,
	"rebaseline": cmdRebaseline,
	"claims":     cmdClaims,
	"run":        cmdRun,
	"round":      cmdRound,
	"close":      cmdClose,
	"drop":       cmdDrop,
	"defer":      cmdDefer,
	"reopen":     cmdReopen,
	"note":       cmdNote,
	"next":       cmdNext,
	"show":       cmdShow,
	"status":     cmdStatus,
	"render":     cmdRender,
	"check":      cmdCheck,
	"schema":     cmdSchema,
	"audit":      cmdAudit,
	"import":     cmdImport,
	"lint":       cmdLint,
}

const usage = `usage: bl [--root DIR] [--json] VERB [args]

verbs:
  init | file | open | brief | checks | rebaseline | claims | run | round | close | drop | defer
  reopen | note | next | show | status | check | render | schema | audit | import | lint

state: .backlog/ledger.jsonl  ·  views: TODO.md, PROGRESS.md  ·  log: .backlog/run.log
exit: 0 ok · 2 refused (nothing written) · 3 corrupt ledger`

func run(argv []string) int {
	started := time.Now()
	root, _ := os.Getwd()
	rest := argv
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
		switch {
		case rest[0] == "--root" && len(rest) > 1:
			root = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(rest[0], "--root="):
			root = strings.TrimPrefix(rest[0], "--root=")
			rest = rest[1:]
		case rest[0] == "--help" || rest[0] == "-h":
			fmt.Println(usage)
			return 0
		default:
			goto verb
		}
	}
verb:
	if len(rest) == 0 {
		fmt.Println(usage)
		return 2
	}
	verb := rest[0]
	handler, ok := handlers[verb]
	if !ok {
		fmt.Fprintf(os.Stderr, "bl: unknown verb %q\n%s\n", verb, usage)
		return 2
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	a, err := parseArgs(rest[1:])
	code := 0
	if err != nil {
		code = report(err)
	} else {
		err = handler(abs, a)
		code = report(err)
	}
	logRun(abs, rest[1:], code, started)
	return code
}

func report(err error) int {
	if err == nil {
		return 0
	}
	var refusal Refusal
	var corruption Corruption
	switch {
	case errors.As(err, &refusal):
		if refusal.rule != "" {
			fmt.Fprintf(os.Stderr, "bl: refused (%s): %s\n", refusal.rule, refusal.msg)
		} else {
			fmt.Fprintf(os.Stderr, "bl: refused: %s\n", refusal.msg)
		}
		return 2
	case errors.As(err, &corruption):
		fmt.Fprintf(os.Stderr, "bl: corrupt ledger: %s\n", corruption.msg)
		return 3
	case errors.Is(err, errFailed):
		return 1
	default:
		fmt.Fprintf(os.Stderr, "bl: %v\n", err)
		return 1
	}
}

func main() { os.Exit(run(os.Args[1:])) }
