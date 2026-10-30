package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
	totalCap    = 6
	marker      = "rendered from " + backlogDir + "/" + ledgerName + " by bl"
	itemsDir    = "items"
	briefName   = "brief.md"
	checksName  = "checks.tsv"
	testsName   = "tests.py"
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
	Seq          int      `json:"seq"`
	At           string   `json:"at"`
	T            string   `json:"t"`
	MS           *int64   `json:"ms,omitempty"`
	ID           string   `json:"id,omitempty"`
	Text         string   `json:"text,omitempty"`
	Note         string   `json:"note,omitempty"`
	After        string   `json:"after,omitempty"`
	Source       string   `json:"source,omitempty"`
	Verdict      string   `json:"verdict,omitempty"`
	Cause        string   `json:"cause,omitempty"`
	ReportSHA    string   `json:"report_sha,omitempty"`
	Dispositions []string `json:"dispositions,omitempty"`
	Rows         string   `json:"rows,omitempty"`
	RowsSHA      string   `json:"rows_sha,omitempty"`
	Commit       string   `json:"commit,omitempty"`
	Reason       string   `json:"reason,omitempty"`
	By           string   `json:"by,omitempty"`
	// The order of work (I79): the ids a bump or unbump names, in the order given; the ranking
	// a rank records; and a file that puts its item at the front of tier 1.
	IDs     []string `json:"ids,omitempty"`
	Ranking []Ranked `json:"ranking,omitempty"`
	Urgent  bool     `json:"urgent,omitempty"`
	// Artefacts (R20): each event that references a file records its path and sha256.
	Brief     string `json:"brief,omitempty"`
	BriefSHA  string `json:"brief_sha,omitempty"`
	Checks    string `json:"checks,omitempty"`
	ChecksSHA string `json:"checks_sha,omitempty"`
	PrevSHA   string `json:"prev_checks_sha,omitempty"`
	// The check list's format (I89, B1): 1 for a list frozen before the test file existed, 2
	// for a list that may hold test rows. An event written before the field is format 1.
	Format int `json:"format,omitempty"`
	// The frozen test file and its hash (I89, B1): present exactly when a test file is frozen
	// after the command that wrote the event.
	Tests     string `json:"tests,omitempty"`
	TestsSHA  string `json:"tests_sha,omitempty"`
	Claims    string `json:"claims,omitempty"`
	ClaimsSHA string `json:"claims_sha,omitempty"`
	// The run (R7): the run id, and the sha256 of the run file it names.
	Run    string `json:"run,omitempty"`
	RunSHA string `json:"run_sha,omitempty"`
	// The item's scope and the before-state (R28): the scope paths, the ignored files the
	// item may write, the tree the item was opened against, and the hash of every ignored
	// file that existed under the scope when it was opened.
	Scope          []string `json:"scope,omitempty"`
	ScopeIgnored   []string `json:"scope_ignored,omitempty"`
	BeforeTree     string   `json:"before_tree,omitempty"`
	PrevBeforeTree string   `json:"prev_before_tree,omitempty"`
	BaselinePaths  []string `json:"baseline_paths,omitempty"`
	IgnoredBefore  []string `json:"ignored_before,omitempty"`
	// The tree the round verified (R6) and the finding it raised (R13).
	Tree    string `json:"tree,omitempty"`
	Finding string `json:"finding,omitempty"`
	// The second check (R16): the row it decides, the verdict it computed, and the command
	// and expectation it used.
	Row      string `json:"row,omitempty"`
	Computed string `json:"computed,omitempty"`
	Cmd      string `json:"cmd,omitempty"`
	Expect   string `json:"expect,omitempty"`
	// The restart (R36): the failure class it records, the reason and the change, the number of
	// the attempt it ends, and the failure record it wrote for that attempt. Attempt is a
	// pointer so that the number 1 is recorded rather than omitted — an event that carries no
	// attempt does not carry the field at all.
	Attempt   *int   `json:"attempt,omitempty"`
	Why       string `json:"why,omitempty"`
	Change    string `json:"change,omitempty"`
	Record    string `json:"record,omitempty"`
	RecordSHA string `json:"record_sha,omitempty"`
	// A verifier packet (R39) records the window it opens: the ledger and run log prefixes and
	// the .backlog/ digest. The lengths are pointers so that a 0 is recorded, as with Attempt.
	LedgerLen  *int   `json:"ledger_len,omitempty"`
	LedgerSHA  string `json:"ledger_sha,omitempty"`
	RunlogLen  *int   `json:"runlog_len,omitempty"`
	RunlogSHA  string `json:"runlog_sha,omitempty"`
	BacklogSHA string `json:"backlog_sha,omitempty"`
}

// Ranked is one line of the loop's ranking: an item and why it ranks where it does.
type Ranked struct {
	ID     string `json:"id"`
	Reason string `json:"reason"`
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
	// The check list's format and the frozen test file (I89, B1): a list frozen before the
	// test file existed has no format and is format 1.
	ChecksFormat int
	Tests        string
	TestsSHA     string
	// The scope and the before-state (R28): the scope paths, the ignored files the item may
	// write, the tree the item was opened against, and the ignored files under the scope
	// with the hash each had then.
	Scope               []string
	ScopeIgnored        []string
	CarriedScope        []string
	CarriedScopeIgnored []string
	CarriedScopeAttempt int
	BeforeTree          string
	IgnoredBefore       []string
	// The second checks (R16) the item has.
	SecondChecks []Event
	// The seq of the latest re-baseline: a re-baseline is a new premise, so it is recorded
	// as the premise it is. It resets no count — the round budget belongs to the item.
	RebaselineSeq int
	BaselineSeq   int
	// The attempt (R36). Every item starts in attempt 1; each accepted restart adds 1. The
	// restarts of the attempts the item has ended are kept in order, and restartSeq is the seq
	// of the latest of them: a round, a run or a second check at or before it belongs to an
	// attempt the restart voided.
	Attempt    int
	Restarts   []Event
	restartSeq int
}

// attemptRounds returns the rounds of the item's current attempt (R36): every round after the
// latest restart. A restart voids the rounds of the attempt it ends, so the round cap, the
// state rules and `bl status` read these, not the item's whole history.
func (i *Item) attemptRounds() []Event {
	var out []Event
	for _, r := range i.Rounds {
		if r.Seq > i.restartSeq {
			out = append(out, r)
		}
	}
	return out
}

// attemptRuns returns the runs of the item's current attempt (R36). Run numbers stay item-wide,
// so the numbering continues after a restart; only the runs the new attempt may cite are here.
func (i *Item) attemptRuns() []Event {
	var out []Event
	for _, r := range i.Runs {
		if r.Seq > i.restartSeq && r.Seq > i.BaselineSeq {
			out = append(out, r)
		}
	}
	return out
}

// void reports whether a round belongs to an attempt a restart ended (R36). A void round stays
// in the item's history — which is why the numbering continues — and satisfies no rule of the
// attempt that replaced it.
func (i *Item) void(ev Event) bool { return ev.Seq <= i.restartSeq }

// causeRounds counts the item's rounds of one cause in the current attempt (R19, R36). A
// re-baseline is a new premise and resets no count; a restart ends the attempt, so the count
// starts again with the next one.
func (i *Item) causeRounds(cause string) int {
	n := 0
	for _, r := range i.attemptRounds() {
		if r.Cause == cause {
			n++
		}
	}
	return n
}

// codeFails counts the code rounds the current attempt has spent (R19).
func (i *Item) codeFails() int {
	return i.causeRounds("code")
}

// passRounds counts the current attempt's PASS rounds (R19). A PASS carries no cause, so it is
// counted here: a PASS is a round, and it spends the budget.
func (i *Item) passRounds() int {
	n := 0
	for _, r := range i.attemptRounds() {
		if r.Verdict == "PASS" {
			n++
		}
	}
	return n
}

func (i *Item) last() *Event {
	rounds := i.attemptRounds()
	if len(rounds) == 0 {
		return nil
	}
	return &rounds[len(rounds)-1]
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

// attemptFile is the path of one attempt's artefact (R36): attempt 1 keeps the path it has
// always had, and every later attempt's files live under a<n>/, so a later attempt never
// rewrites an earlier one's file.
func attemptFile(id string, attempt int, name string) string {
	if attempt <= 1 {
		return itemFile(id, name)
	}
	return itemFile(id, "a"+strconv.Itoa(attempt)+"/"+name)
}

// failureFile is the path of the record a restart writes for the attempt it ends (R36). It sits
// beside the attempt's directory rather than in it: the record is what attempt n left behind,
// and attempt n+1's brief quotes it.
func failureFile(id string, attempt int) string {
	return itemFile(id, "a"+strconv.Itoa(attempt)+"-failure.md")
}

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
				Status: "open", FiledAt: ev.At, Attempt: 1,
			}
			byID[key] = it
			order = append(order, it)
			continue
		case "bump", "unbump", "rank":
			// These name items but change none: they only order the work (see workOrder).
			for _, id := range namedIDs(ev) {
				if _, ok := byID[strings.ToLower(id)]; !ok {
					return nil, nil, corrupt("%s names unknown item %s", ev.T, id)
				}
			}
			continue
		case "packet":
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
			// The scope and the before-state are recorded once, at `bl open` (R28).
			if len(ev.Scope) > 0 {
				it.Scope = ev.Scope
			}
			if len(ev.ScopeIgnored) > 0 {
				it.ScopeIgnored = ev.ScopeIgnored
			}
			if ev.BeforeTree != "" {
				it.BeforeTree = ev.BeforeTree
			}
			if len(ev.IgnoredBefore) > 0 {
				it.IgnoredBefore = ev.IgnoredBefore
			}
		case "brief":
			it.Brief, it.BriefSHA = ev.Brief, ev.BriefSHA
		case "checks":
			// An amendment records the hash of the list it amends (R4). After the claims it is
			// the only checks event the ledger accepts (R5), and only over the current list.
			if ev.PrevSHA != "" && ev.PrevSHA != it.ChecksSHA {
				return nil, nil, corrupt("checks event for %s amends list %s, but the current list is %s", ev.ID, ev.PrevSHA, it.ChecksSHA)
			}
			if it.Claims != "" && ev.PrevSHA == "" {
				return nil, nil, corrupt("checks event for %s after its claims", ev.ID)
			}
			it.Checks, it.ChecksSHA = ev.Checks, ev.ChecksSHA
			it.ChecksFormat = ev.Format
			if it.ChecksFormat == 0 {
				it.ChecksFormat = 1
			}
			it.Tests, it.TestsSHA = ev.Tests, ev.TestsSHA
		case "rebaseline":
			if it.Claims != "" {
				return nil, nil, corrupt("rebaseline event for %s after its claims", ev.ID)
			}
			it.Checks, it.ChecksSHA = ev.Checks, ev.ChecksSHA
			it.ChecksFormat = ev.Format
			if it.ChecksFormat == 0 {
				it.ChecksFormat = 1
			}
			it.Tests, it.TestsSHA = ev.Tests, ev.TestsSHA
			it.RebaselineSeq = ev.Seq
		case "baseline":
			if !it.Opened || len(it.Scope) == 0 || it.passRounds() != 0 || ev.PrevBeforeTree != it.BeforeTree || !treeHashRe.MatchString(ev.BeforeTree) || !treeHashRe.MatchString(ev.Commit) || strings.TrimSpace(ev.Reason) == "" || len(ev.BaselinePaths) == 0 {
				return nil, nil, corrupt("invalid baseline event for %s", ev.ID)
			}
			for _, p := range ev.BaselinePaths {
				if inScope(it.Scope, p) || isArtifact(p) || p == "" {
					return nil, nil, corrupt("invalid baseline path for %s", ev.ID)
				}
			}
			it.BeforeTree, it.BaselineSeq = ev.BeforeTree, ev.Seq
			it.Notes = append(it.Notes, fmt.Sprintf("baseline recovery: %s -> %s; HEAD %s; paths %s; reason: %s; fresh run and verifier required", ev.PrevBeforeTree, ev.BeforeTree, ev.Commit, strings.Join(ev.BaselinePaths, ", "), ev.Reason))
		case "claims":
			if it.Checks == "" {
				return nil, nil, corrupt("claims event for %s before any check list", ev.ID)
			}
			it.Claims, it.ClaimsSHA = ev.Claims, ev.ClaimsSHA
		case "round":
			it.Rounds = append(it.Rounds, ev)
		case "run":
			it.Runs = append(it.Runs, ev)
		case "second-check":
			it.SecondChecks = append(it.SecondChecks, ev)
		case "close":
			it.Status, it.Commit, it.Opened = "closed", ev.Commit, false
		case "drop":
			it.Status, it.Reason, it.Opened = "declined", ev.Reason, false
		case "defer":
			it.Status, it.Reason, it.Opened = "parked", ev.Reason, false
		case "reopen":
			it.Status, it.Reason, it.Opened = "open", "", false
		case "restart":
			// R36: a restart ends the attempt it names and starts the next one. Everything that
			// belongs to the attempt goes — the brief, the check list, its format, the test file,
			// the claims, the scope and the before-state — and the item leaves flight. The rounds,
			// the runs and the second checks stay in the history, where they are void: the numbering
			// continues and the evidence of the attempt that ended is still on disk to be checked.
			if ev.Attempt == nil {
				return nil, nil, corrupt("restart event for %s names no attempt", ev.ID)
			}
			// A restart ends the attempt the item is in — never another one, or the void markers
			// and the numbering would disagree about which rounds belong to which attempt.
			if *ev.Attempt != it.Attempt {
				return nil, nil, corrupt("restart event for %s ends attempt %d, but the item is in attempt %d", ev.ID, *ev.Attempt, it.Attempt)
			}
			it.CarriedScope, it.CarriedScopeIgnored, it.CarriedScopeAttempt = nil, nil, 0
			if len(it.Scope) > 0 || len(it.ScopeIgnored) > 0 {
				it.CarriedScope = append([]string(nil), it.Scope...)
				it.CarriedScopeIgnored = append([]string(nil), it.ScopeIgnored...)
				it.CarriedScopeAttempt = *ev.Attempt
			}
			it.Attempt, it.restartSeq = *ev.Attempt+1, ev.Seq
			it.Restarts = append(it.Restarts, ev)
			it.Opened = false
			it.Brief, it.BriefSHA = "", ""
			it.Checks, it.ChecksSHA = "", ""
			it.ChecksFormat = 0
			it.Tests, it.TestsSHA = "", ""
			it.Claims, it.ClaimsSHA = "", ""
			it.Scope, it.ScopeIgnored = nil, nil
			it.BeforeTree, it.IgnoredBefore = "", nil
			it.RebaselineSeq = 0
			it.BaselineSeq = 0
			// a closed item is reopened by the restart, and the close's commit goes with the
			// attempt it recorded
			if it.Status == "closed" {
				it.Status, it.Commit = "open", ""
			}
			it.Reason = ""
		case "note":
			it.Notes = append(it.Notes, ev.Text)
		case "signoff":
			// Sign-offs are audit events; their existence is checked by commands that need it.
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

// namedIDs is every id a bump, unbump or rank event names, in the order it names them.
func namedIDs(ev Event) []string {
	if ev.T != "rank" {
		return ev.IDs
	}
	ids := make([]string, len(ev.Ranking))
	for i, r := range ev.Ranking {
		ids[i] = r.ID
	}
	return ids
}

// tierOne is the human's priority list, as lower-cased ids: every bump newest first, and within
// one bump the order given, each id counted at its first appearance — so a later bump overrides
// an earlier one, and an unbump newer than every bump of an id takes it out. `bl file --urgent`
// counts as a bump of its item at the point it was filed.
func tierOne(events []Event) []string {
	seen := map[string]bool{}
	var out []string
	for i := len(events) - 1; i >= 0; i-- {
		ev := events[i]
		var ids []string
		keep := true
		switch {
		case ev.T == "bump":
			ids = ev.IDs
		case ev.T == "unbump":
			ids, keep = ev.IDs, false
		case ev.T == "file" && ev.Urgent:
			ids = []string{ev.ID}
		default:
			continue
		}
		for _, id := range ids {
			key := strings.ToLower(id)
			if seen[key] {
				continue
			}
			seen[key] = true
			if keep {
				out = append(out, key)
			}
		}
	}
	return out
}

// tierTwo is the loop's latest ranking. A later rank replaces an earlier one as a whole.
func tierTwo(events []Event) []Ranked {
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].T == "rank" {
			return events[i].Ranking
		}
	}
	return nil
}

// workOrder is every item in the order `bl next` works in: tier 1 (the human's bumps), then
// tier 2 (the loop's ranking), then tier 3 — every other item, the ones the most open items wait
// on first, then in filing order. It does not filter: eligible does.
func workOrder(events []Event, order []*Item, byID map[string]*Item) []*Item {
	var out []*Item
	placed := map[*Item]bool{}
	put := func(id string) {
		if it, ok := byID[strings.ToLower(id)]; ok && !placed[it] {
			placed[it] = true
			out = append(out, it)
		}
	}
	for _, id := range tierOne(events) {
		put(id)
	}
	for _, r := range tierTwo(events) {
		put(r.ID)
	}
	waiting := map[string]int{}
	for _, it := range order {
		if it.Status == "open" && it.After != "" {
			waiting[strings.ToLower(it.After)]++
		}
	}
	var rest []*Item
	for _, it := range order {
		if !placed[it] {
			rest = append(rest, it)
		}
	}
	sort.SliceStable(rest, func(i, j int) bool {
		return waiting[strings.ToLower(rest[i].ID)] > waiting[strings.ToLower(rest[j].ID)]
	})
	return append(out, rest...)
}
