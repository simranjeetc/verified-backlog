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
//	bl open A1
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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	backlogDir  = ".backlog"
	ledgerName  = "ledger.jsonl"
	runlogName  = "run.log"
	evidenceDir = "evidence"
	queueView   = "TODO.md"
	recordView  = "PROGRESS.md"
	roundCap    = 3
	marker      = "rendered from " + backlogDir + "/" + ledgerName + " by bl"
)

var (
	idPattern    = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9]*(?:[._-][A-Za-z0-9]+)*$`)
	legacyItemRe = regexp.MustCompile(`^\s*[-*+]\s+\[([ xX~])\]\s*(?:\*\*)?([A-Z][A-Z0-9]{0,9}-?\d+(?:\.\d+)*[A-Za-z]?)(?:\*\*)?\s*[—–:-]?\s*(.+)$`)
	legacyHead   = regexp.MustCompile(`^#{2,6}\s+(?:\*\*)?([A-Z][A-Z0-9]{0,9}-?\d+(?:\.\d+)*[A-Za-z]?)(?:\*\*)?\s*[—–:-]?\s*(.*)$`)
	legacyBox    = regexp.MustCompile(`^\s*[-*+]\s+\[(.)\]`)
	legacyVerdic = regexp.MustCompile(`(?i)^\s*(?:[-*>]\s*)?(?:\*\*)?Verdict(?:\*\*)?\s*:\s*(?:\*\*)?\s*(PASS|FAIL)`)
	commitRe     = regexp.MustCompile(`\b([0-9a-f]{7,40})\b`)
	periodRe     = regexp.MustCompile(`^(\d+)\s*([dhw])$`)
)

var (
	verdicts = []string{"PASS", "FAIL"}
	causes   = []string{"code", "harness"}
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
	Notes     []string
	Opened    bool
	Commit    string
	Reason    string
	FiledAt   string
}

func (i *Item) codeFails() int {
	n := 0
	for _, r := range i.Rounds {
		if r.Verdict == "FAIL" && r.Cause == "code" {
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
type Refusal struct{ msg string }

func (r Refusal) Error() string { return r.msg }

func refuse(format string, a ...any) error { return Refusal{fmt.Sprintf(format, a...)} }

// Corruption means the ledger on disk breaks its own invariants.
type Corruption struct{ msg string }

func (c Corruption) Error() string { return c.msg }

func corrupt(format string, a ...any) error { return Corruption{fmt.Sprintf(format, a...)} }

type paths struct {
	base, ledger, runlog, evidence, queue, record string
}

func newPaths(root string) paths {
	base := filepath.Join(root, backlogDir)
	return paths{
		base:     base,
		ledger:   filepath.Join(base, ledgerName),
		runlog:   filepath.Join(base, runlogName),
		evidence: filepath.Join(base, evidenceDir),
		queue:    filepath.Join(root, queueView),
		record:   filepath.Join(root, recordView),
	}
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

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
		case "round":
			it.Rounds = append(it.Rounds, ev)
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
	if _, err := appendEvent(root, Event{T: "open", ID: it.ID}); err != nil {
		return err
	}
	fmt.Printf("bl: %s in flight\n", it.ID)
	return nil
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
	verdict := strings.ToUpper(strings.TrimSpace(a.get("verdict")))
	if verdict != "PASS" && verdict != "FAIL" {
		return refuse("--verdict must be one of %s", strings.Join(verdicts, ", "))
	}
	cause := strings.ToLower(strings.TrimSpace(a.get("cause")))
	if verdict == "FAIL" && cause != "code" && cause != "harness" {
		return refuse("a FAIL needs --cause %s", strings.Join(causes, "|"))
	}
	if verdict == "PASS" && cause != "" {
		return refuse("a PASS carries no cause")
	}
	if last := it.last(); last != nil && last.Verdict == "PASS" {
		return refuse("%s already passed — close it", it.ID)
	}
	if verdict == "FAIL" && cause == "code" && it.codeFails() >= roundCap {
		return refuse("%s has spent %d code rounds (cap %d) — escalate to a human", it.ID, it.codeFails(), roundCap)
	}
	rows, err := a.rows()
	if err != nil {
		return err
	}
	rows = strings.TrimRight(rows, "\r\n")
	if strings.TrimSpace(rows) == "" {
		return refuse("--rows is empty: a verdict exists only after commands have run")
	}
	p := newPaths(root)
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-r%d.txt", it.ID, len(it.Rounds)+1)
	if err := atomicWrite(filepath.Join(p.evidence, name), rows+"\n"); err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(rows + "\n"))
	ev := Event{
		T: "round", ID: it.ID, Verdict: verdict,
		Rows:    backlogDir + "/" + evidenceDir + "/" + name,
		RowsSHA: hex.EncodeToString(sum[:]),
		Cause:   cause,
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
	fmt.Printf("rounds: %d (code failures %d/%d)\n", len(it.Rounds), it.codeFails(), roundCap)
	for i, r := range it.Rounds {
		cause := ""
		if r.Cause != "" {
			cause = " cause=" + r.Cause
		}
		fmt.Printf("  r%d %s%s %s\n", i+1, r.Verdict, cause, r.Rows)
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
	order, _, err := derive(events)
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
	return nil
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
		if it.Status == "closed" {
			if last := it.last(); last == nil || last.Verdict != "PASS" {
				problems = append(problems, it.ID+" is closed without a PASS round")
			}
		}
		for _, r := range it.Rounds {
			sum := sha256.Sum256(mustRead(filepath.Join(root, r.Rows)))
			if _, err := os.Stat(filepath.Join(root, r.Rows)); err != nil {
				problems = append(problems, it.ID+" round "+r.Rows+" is missing")
			} else if hex.EncodeToString(sum[:]) != r.RowsSHA {
				problems = append(problems, it.ID+" round "+r.Rows+" does not match its hash")
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
			"file":   {"required": {"id", "text"}, "optional": {"after", "source"}},
			"open":   {"required": {"id"}, "optional": {}},
			"round":  {"required": {"id", "verdict", "rows"}, "optional": {"cause"}},
			"close":  {"required": {"id", "commit"}, "optional": {}},
			"drop":   {"required": {"id", "reason"}, "optional": {"by"}},
			"defer":  {"required": {"id", "reason"}, "optional": {}},
			"reopen": {"required": {"id"}, "optional": {"reason"}},
			"note":   {"required": {"id", "text"}, "optional": {}},
		},
	}
}

func cmdSchema(root string, a args) error {
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(schemaJSON())
	}
	fmt.Printf("bl schema v1 — %s/%s\n", backlogDir, ledgerName)
	order := []string{"file", "open", "round", "close", "drop", "defer", "reopen", "note"}
	spec := schemaJSON()["events"].(map[string]map[string][]string)
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

var boolFlagNames = map[string]bool{"json": true, "views": true, "due": true}

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
	"init":   cmdInit,
	"file":   cmdFile,
	"open":   cmdOpen,
	"round":  cmdRound,
	"close":  cmdClose,
	"drop":   cmdDrop,
	"defer":  cmdDefer,
	"reopen": cmdReopen,
	"note":   cmdNote,
	"next":   cmdNext,
	"show":   cmdShow,
	"status": cmdStatus,
	"render": cmdRender,
	"check":  cmdCheck,
	"schema": cmdSchema,
	"audit":  cmdAudit,
	"import": cmdImport,
	"lint":   cmdLint,
}

const usage = `usage: bl [--root DIR] [--json] VERB [args]

verbs:
  init | file | open | round | close | drop | defer | reopen | note
  next | show | status | check | render | schema | audit | import | lint

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
		fmt.Fprintf(os.Stderr, "bl: refused: %s\n", refusal.msg)
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
