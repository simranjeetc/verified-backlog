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
//	bl file --id A1 --text "..." [--after B2] [--source human] [--urgent]
//	bl open A1                                            (writes .backlog/items/A1/brief.md)
//	bl brief A1 --file brief.md                           (amends the brief; a new event)
//	bl checks A1 [--file checks.tsv] [--tests tests.py] [--amend]  (list from stdin without --file)
//	bl rebaseline A1 --file checks.tsv [--tests tests.py] --reason "..."  (replaces the frozen list)
//	bl claims A1 --file claims.md
//	bl run A1                                            (runs the frozen rows; writes the run file)
//	bl round A1 --verdict FAIL --cause code --rows -      (rows from stdin)
//	bl round A1 --verdict PASS --rows checks.txt
//	bl close A1 --commit 6742cf6
//	bl drop D9 --reason "cosmetic; not worth it"
//	bl defer O4 --reason "awaiting triage"
//	bl reopen O4
//	bl restart A1 --reason bad-check --why "..." --change "..."  (ends the attempt, starts the next)
//	bl note A1 --text "deferred because ..."
//	bl bump A3 A1 | bl unbump A3                          (the human's priority list: tier 1)
//	bl rank --file ranking.tsv                            (the loop's ranking: tier 2)
//	bl next | bl show A1 | bl status | bl check [--views] | bl render
//	bl audit [--since 2d] [--out .backlog/audit]
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
	"sync"
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
	Seq     int    `json:"seq"`
	At      string `json:"at"`
	T       string `json:"t"`
	MS      *int64 `json:"ms,omitempty"`
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
	Scope         []string `json:"scope,omitempty"`
	ScopeIgnored  []string `json:"scope_ignored,omitempty"`
	BeforeTree    string   `json:"before_tree,omitempty"`
	IgnoredBefore []string `json:"ignored_before,omitempty"`
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
		if r.Seq > i.restartSeq {
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
			// a closed item is reopened by the restart, and the close's commit goes with the
			// attempt it recorded
			if it.Status == "closed" {
				it.Status, it.Commit = "open", ""
			}
			it.Reason = ""
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

// isArtifact reports whether a repository-relative path is one of the artefacts of R25: a path
// under the ledger's own directory, or one of the two rendered views, TODO.md and PROGRESS.md.
// The artefacts are excluded from the verified tree and from the round's diff.
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
	// --urgent is a bump of the new item, recorded on its own file event: one call, one append.
	if a.flag("urgent") {
		ev.Urgent, ev.By = true, "human"
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
	brief := attemptBrief(briefText(it.Text), failureRecords(root, it))
	rel := attemptFile(it.ID, it.Attempt, briefName)
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
	// The scope and the before-state (R28, R10). The scope is the set of paths the item may
	// write, recorded here — once. The before-tree is the tree the item was opened against:
	// the round's diff is taken against it, and the ignored files under the scope are
	// recorded with the hash each had then.
	scope := a.list("scope")
	scopeIgnored := a.list("scope-ignored")
	carried := it.Attempt > 1 && len(a.lists["scope"]) == 0 && len(a.lists["scope-ignored"]) == 0 && it.CarriedScopeAttempt > 0
	if carried {
		scope = append([]string(nil), it.CarriedScope...)
		scopeIgnored = append([]string(nil), it.CarriedScopeIgnored...)
	}
	before, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	ignored, err := ignoredUnderScope(root, scope)
	if err != nil {
		return err
	}
	ev := Event{
		T: "open", ID: it.ID, Brief: rel, BriefSHA: shaHex([]byte(brief)),
		Scope: scope, ScopeIgnored: scopeIgnored, BeforeTree: before, IgnoredBefore: ignored,
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if carried {
		fmt.Printf("bl: %s scope carried from attempt %d: %s\n", it.ID, it.CarriedScopeAttempt, strings.Join(scope, ", "))
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

// verifyTests re-hashes the item's frozen test file, when the latest `checks` or `rebaseline`
// event carries `tests_sha` (I89, B8). It is the same comparison R31 requires, through the same
// helper, so the test file joins the brief, the check list and the claims as a frozen artefact.
// An item without a frozen test file has nothing to re-hash.
func verifyTests(root string, it *Item) error {
	if it.TestsSHA == "" {
		return nil
	}
	_, err := verifyArtefact(root, it.Tests, it.TestsSHA, "test file")
	return err
}

// requireInFlight refuses when the item is not the item in flight. The rule argument names the
// design rule the refusal is reported under: "R35" for `bl run` and `bl round`, the two verbs
// the design's rule R35 covers. The other paths that hold the lock — brief, checks, claims,
// rebaseline and second-check — pass no rule, so their refusals keep their unnamed wording.
func requireInFlight(order []*Item, it *Item, rule string) error {
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur == nil || !strings.EqualFold(cur.ID, it.ID) {
		if rule == "" {
			return refuse("%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
		}
		return refuseRule(rule, "%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
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
// commands cannot drift apart on what a list may say. format is the format the event will
// carry: R34 and R8 are the format-1 rules, and a format-2 `decider: bl` row is R37's.
func validateChecks(root string, it *Item, rows []checkRow, amend bool, format int) (string, error) {
	if err := validateR1(rows); err != nil {
		return "", err
	}
	// R34: the command's dialect. A cmd is a script run by /bin/sh -c, so a pipe in it is a
	// pipe; a pipe with an odd number of backslashes before it is read by the shell as a
	// literal pipe, and the row then does not do what it says. The list is refused here, before
	// it is frozen, so the mistake cannot reach a round.
	if err := validateR34(rows, format); err != nil {
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
		if _, err := checkAmendment(old, rows); err != nil {
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
	if err := validateR8(rows, format); err != nil {
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
// every row. A cmd is run by /bin/sh -c, so a pipe in it is a pipe; a pipe is ESCAPED when an
// odd number of backslashes precedes it, because the shell then reads it as a literal pipe, so
// the row does not do what it says and the list is refused before it can reach a round.
//
// The check counts the backslashes rather than looking for the pair: an even number of
// backslashes before a pipe is an escaped backslash followed by a real pipe, and that row is
// fine.
//
// Only the cmd field is checked. An expect pattern is Go's RE2, where a backslash before a
// pipe means a literal pipe — and there that is what the pattern says, not a fault.
//
// R34 does not reach a format-2 `decider: bl` row (I89, B10): a format-2 test row holds no
// shell syntax, and a format-2 shell row is governed by B6's simple-command scan instead.
func validateR34(rows []checkRow, format int) error {
	for _, r := range rows {
		if format == 2 && r.f["decider"] == "bl" {
			continue
		}
		if n, ok := escapedPipe(r.f["cmd"]); ok {
			return refuseRule("R34", "row %s: the command holds an odd number of backslashes (%d) before a pipe — the shell reads that as a literal pipe; write the pipe as a pipe",
				r.f["id"], n)
		}
	}
	return nil
}

// escapedPipe reports the number of backslashes immediately before the first pipe the shell
// would read as a literal pipe, and whether there is one at all. It reads the command the way
// the shell does: inside a single-quoted or double-quoted string a pipe is text the pattern
// carries, not a pipe, so it is ignored there along with any backslash before it. Outside quotes
// a pipe is ESCAPED when an ODD number of backslashes precedes it: the shell reads `\|` as a
// literal pipe. An EVEN number is an escaped backslash followed by a real pipe, and a pipe with
// no backslash before it is a real pipe too — so only the odd count is a fault.
func escapedPipe(cmd string) (int, bool) {
	var quote byte // 0 outside quotes, else the quote that is open
	run := 0       // backslashes immediately before this character, outside quotes
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++ // a backslash in double quotes escapes the next character
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			run++
			continue
		case (c == '\'' || c == '"') && run%2 == 0:
			quote = c
		case c == '|' && run%2 == 1:
			return run, true
		}
		run = 0
	}
	return 0, false
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

// validateR8 applies R8 — an expectation is one of five forms — to every row of a format-1
// list. R8's text is unchanged, but it no longer applies to a format-2 list: there a
// `decider: bl` row's expect is R37's (I89, B10), and a judge row carries no expect at all.
func validateR8(rows []checkRow, format int) error {
	if format != 1 {
		return nil
	}
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

// checkAmendment allows exactly the three edits of R4: judge → bl, filling an empty brief_ref on
// an advisory row, and ADDING a row at the end of the list. old and next are both already valid
// lists, so an added row's id is already known not to repeat one the list holds (R1). It reports
// whether the amendment CHANGES a row already in the list: the claims freeze the list's content
// (R5), so only an amendment that changes a row is refused after them.
func checkAmendment(old, next []checkRow) (bool, error) {
	if len(next) < len(old) {
		return false, refuseRule("R4", "an amendment never removes a row (%d rows, was %d)", len(next), len(old))
	}
	changes := false
	for i := range old {
		o, n := old[i].f, next[i].f
		if o["id"] != n["id"] {
			return false, refuseRule("R4", "row %d: an amendment never changes or reorders ids (%s, was %s)", i+1, n["id"], o["id"])
		}
		id := o["id"]
		for _, col := range []string{"severity", "cmd", "criterion"} {
			if o[col] != n[col] {
				return false, refuseRule("R4", "row %s: an amendment never changes %s", id, col)
			}
		}
		switch {
		case o["decider"] == n["decider"]:
			for _, col := range []string{"expect", "fail_if", "why_not_mech"} {
				if o[col] != n[col] {
					return false, refuseRule("R4", "row %s: %s may change only when the row is reclassified from judge to bl", id, col)
				}
			}
		case o["decider"] == "judge" && n["decider"] == "bl":
			changes = true
		default:
			return false, refuseRule("R4", "row %s: the only reclassification an amendment may make is judge → bl", id)
		}
		if o["brief_ref"] != n["brief_ref"] {
			if o["brief_ref"] != "" || o["severity"] != "advisory" {
				return false, refuseRule("R4", "row %s: an amendment may only fill an empty brief_ref on an advisory row", id)
			}
			changes = true
		}
	}
	if !changes && len(next) == len(old) {
		return false, refuseRule("R4", "the amendment changes nothing")
	}
	return changes, nil
}

// amendGuards applies the state rules of an amendment once its scope is known: R5 (the claims
// freeze the list's content, so an amendment that changes a row is refused after them — adding
// a row is not), then R4 (an amendment is refused once the item has a PASS round). R5 comes
// first, so an amendment that changes a row says R5 even after a PASS round.
func amendGuards(root string, it *Item, rows []checkRow) error {
	prev, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	old, err := parseChecks(string(prev))
	if err != nil {
		return err
	}
	changes, err := checkAmendment(old, rows)
	if err != nil {
		return err
	}
	if changes && it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the check list's content is frozen; only an amendment that adds a row is allowed", it.ID)
	}
	for _, r := range it.attemptRounds() {
		if r.Verdict == "PASS" {
			return refuseRule("R4", "%s already has a PASS round — an amendment is refused once the item has passed", it.ID)
		}
	}
	return nil
}

// ── R37: the test file, and the format-2 row ─────────────────────────────────

// testRowRe matches a test row's cmd exactly (I89, B2): `python3 <tests path> <name>`, with
// single spaces, the path being the item's frozen test file, and the name a Python function
// name that does not start with an underscore.
var testRowRe = regexp.MustCompile(`^python3 (\S+) ([A-Za-z][A-Za-z0-9_]*)$`)

// testPath is the frozen test file's repository-relative path (I89, B1): beside the attempt's
// checks.tsv, so attempt n >= 2 keeps its file under a<n>/ (R36) and attempt 1 keeps the path
// it has always had, `.backlog/items/<id>/tests.py`.
func testPath(id string, attempt int) string { return attemptFile(id, attempt, testsName) }

// testRowName reports whether a row of the given format is a test row (I89, B2) and, if it is,
// the test function it names. A test row is a format-2 `decider: bl` row whose cmd is exactly
// `python3 <the item's tests path> <name>`.
func testRowName(format int, it *Item, r checkRow) (string, bool) {
	if format != 2 || r.f["decider"] != "bl" {
		return "", false
	}
	m := testRowRe.FindStringSubmatch(r.f["cmd"])
	if m == nil || m[1] != testPath(it.ID, it.Attempt) {
		return "", false
	}
	return m[2], true
}

// testRowAttempt reports whether a row is a test-row ATTEMPT (I89, B2): a format-2
// `decider: bl` row whose cmd's first word is `python3` and which contains `.backlog/items/`.
// Such a row must be exactly the test-row form; it is never treated as a shell row.
func testRowAttempt(format int, it *Item, r checkRow) bool {
	if format != 2 || r.f["decider"] != "bl" {
		return false
	}
	fields := strings.Fields(r.f["cmd"])
	return len(fields) > 0 && fields[0] == "python3" && strings.Contains(r.f["cmd"], backlogDir+"/"+itemsDir+"/")
}

// validExitExpect reports whether an expect is B6's `exit <code>`: the integer 0..255 form R8
// defines, checked under R37's tag.
func validExitExpect(expect string) bool {
	if !strings.HasPrefix(expect, "exit ") {
		return false
	}
	code := strings.TrimPrefix(expect, "exit ")
	n, err := strconv.Atoi(code)
	return err == nil && exitCodeRe.MatchString(code) && n <= 255
}

// shellMeta reports the first character outside quotes that makes a format-2 shell row more
// than one simple command (I89, B6), and whether there is one. It reads quotes and backslashes
// exactly as escapedPipe (R34) does: inside a single-quoted string everything is text; inside
// a double-quoted string a backslash escapes the next character and a quote of the other kind
// is text; outside quotes a backslash escapes the next character.
func shellMeta(cmd string) (string, bool) {
	var quote byte
	for i := 0; i < len(cmd); i++ {
		c := cmd[i]
		switch {
		case quote == '\'':
			if c == '\'' {
				quote = 0
			}
		case quote == '"':
			if c == '\\' {
				i++
			} else if c == '"' {
				quote = 0
			}
		case c == '\\':
			i++
		case c == '\'' || c == '"':
			quote = c
		case c == '|' || c == ';' || c == '&' || c == '<' || c == '>' || c == '`' || c == '\n':
			return string(c), true
		case c == '$' && i+1 < len(cmd) && cmd[i+1] == '(':
			return "$(", true
		}
	}
	return "", false
}

// testFile is what bl reads of a test file without running it (I89, B3, B4): its top-level
// defs and classes, the paths each def's `@_covers(...)` names, and the line of the
// module-level `if __name__ == "__main__":` block.
type testFile struct {
	defs     map[string]testDef
	classes  map[string]bool
	mainLine int
}

type testDef struct{ covers []string }

// testFileScript reads the file with python3's ast and prints one JSON object. It never runs
// the file: the run contract is the template's main block, not bl's.
const testFileScript = `import ast, json, sys

path = sys.argv[1]
try:
    with open(path, "rb") as fh:
        src = fh.read()
    tree = ast.parse(src, filename=path)
except SyntaxError as exc:
    print(json.dumps({"ok": False, "error": "%s (line %s)" % (exc.msg, exc.lineno)}))
    sys.exit(0)


def string_args(node):
    out = []
    for a in node.args:
        if isinstance(a, ast.Constant) and isinstance(a.value, str):
            out.append(a.value)
    return out


defs = []
classes = []
main_line = 0
lines = src.split(b"\n")
for node in tree.body:
    if isinstance(node, (ast.FunctionDef, ast.AsyncFunctionDef)):
        covers = []
        for d in node.decorator_list:
            if isinstance(d, ast.Call) and isinstance(d.func, ast.Name) and d.func.id == "_covers":
                covers.extend(string_args(d))
        defs.append({"name": node.name, "line": node.lineno, "covers": covers})
    elif isinstance(node, ast.ClassDef):
        classes.append({"name": node.name, "line": node.lineno})
    elif isinstance(node, ast.If) and main_line == 0 and 0 < node.lineno <= len(lines):
        text = lines[node.lineno - 1].decode("utf-8", "replace").rstrip()
        if "#" in text:
            text = text.split("#", 1)[0].rstrip()
        if text == 'if __name__ == "__main__":':
            main_line = node.lineno
print(json.dumps({"ok": True, "defs": defs, "classes": classes, "main_line": main_line}))
`

// testFileReport is the JSON testFileScript prints.
type testFileReport struct {
	OK    bool   `json:"ok"`
	Error string `json:"error"`
	Defs  []struct {
		Name   string   `json:"name"`
		Line   int      `json:"line"`
		Covers []string `json:"covers"`
	} `json:"defs"`
	Classes []struct {
		Name string `json:"name"`
		Line int    `json:"line"`
	} `json:"classes"`
	MainLine int `json:"main_line"`
}

// parseTestFile reads a test file with python3's ast, without running it (I89, B3, B4).
func parseTestFile(root, rel string) (testFile, error) {
	tf := testFile{defs: map[string]testDef{}, classes: map[string]bool{}}
	cmd := exec.Command("python3", "-c", testFileScript, filepath.Join(root, filepath.FromSlash(rel)))
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	if err := cmd.Run(); err != nil {
		return tf, refuseRule("R37", "python3 could not read the test file %s: %v: %s", rel, err, strings.TrimSpace(errOut.String()))
	}
	var rep testFileReport
	if err := json.Unmarshal(out.Bytes(), &rep); err != nil {
		return tf, refuseRule("R37", "python3 returned no test-file report for %s: %v", rel, err)
	}
	if !rep.OK {
		return tf, refuseRule("R37", "the test file %s does not parse: %s", rel, rep.Error)
	}
	for _, d := range rep.Defs {
		tf.defs[d.Name] = testDef{covers: d.Covers}
	}
	for _, c := range rep.Classes {
		tf.classes[c.Name] = true
	}
	tf.mainLine = rep.MainLine
	return tf, nil
}

// mainLineOffset returns the byte offset of the start of line n (1-based) in data, which is
// where B8's insert-only amendment puts its text.
func mainLineOffset(data []byte, line int) (int, bool) {
	if line < 1 {
		return 0, false
	}
	off := 0
	for n := 1; n < line; n++ {
		i := bytes.IndexByte(data[off:], '\n')
		if i < 0 {
			return 0, false
		}
		off += i + 1
	}
	return off, true
}

// checkInsertOnly is B8's amendment rule: the new test file is the frozen one with new text
// inserted at exactly one point, immediately before the frozen file's main-block line.
func checkInsertOnly(frozen, next []byte, mainLine int) error {
	off, ok := mainLineOffset(frozen, mainLine)
	if mainLine == 0 || !ok {
		return refuseRule("R37", "the frozen test file has no `if __name__ == \"__main__\":` line to insert before")
	}
	if !bytes.HasPrefix(next, frozen[:off]) || !bytes.HasSuffix(next, frozen[off:]) {
		return refuseRule("R37", "an amended test file may only insert text at one point, immediately before the frozen file's main-block line — it may not change or remove the frozen text")
	}
	return nil
}

// validateR37Rows applies R37's row rules to a list: B2 (a test row's exact form and its
// `exit 0` expect), B6 (a shell row is one simple command with an `exit <code>` expect), and
// the test-row attempt. In a format-1 list no row is a test row and none of this applies.
func validateR37Rows(it *Item, rows []checkRow, format int) error {
	for _, r := range rows {
		if format != 2 || r.f["decider"] != "bl" {
			continue
		}
		if _, ok := testRowName(format, it, r); ok {
			if r.f["expect"] != "exit 0" {
				return refuseRule("R37", "row %s: a test row's expect is exactly `exit 0`, not %q", r.f["id"], r.f["expect"])
			}
			continue
		}
		if testRowAttempt(format, it, r) {
			return refuseRule("R37", "row %s: a test row is exactly `python3 %s <name>` — single spaces, the item's test file, and a name matching [A-Za-z][A-Za-z0-9_]*, not %q", r.f["id"], testPath(it.ID, it.Attempt), r.f["cmd"])
		}
		if !validExitExpect(r.f["expect"]) {
			return refuseRule("R37", "row %s: a format-2 shell row's expect is exactly `exit <code>` for 0-255, not %q", r.f["id"], r.f["expect"])
		}
		if meta, ok := shellMeta(r.f["cmd"]); ok {
			return refuseRule("R37", "row %s: a format-2 shell row is one simple command — %q is outside quotes", r.f["id"], meta)
		}
	}
	return nil
}

// validateR37Presence is B9: a test row needs a frozen test file, and a frozen test file needs
// at least one test row. A format-2 list of judge and shell rows only carries no test file.
func validateR37Presence(it *Item, rows []checkRow, format int, willFreeze bool) error {
	n := 0
	for _, r := range rows {
		if _, ok := testRowName(format, it, r); ok {
			n++
		}
	}
	if n > 0 && !willFreeze {
		return refuseRule("R37", "%s: the list has a test row, so a test file must be frozen — pass --tests", it.ID)
	}
	if willFreeze && n == 0 {
		return refuseRule("R37", "%s: a frozen test file must be used by at least one test row — this list has none", it.ID)
	}
	return nil
}

// validateR37File applies B3 to a test file: every test row names a top-level def, no two rows
// name the same def, and every public top-level def or class is named by a row. A helper's
// name starts with `_`, and a helper is never named by a row.
func validateR37File(it *Item, rows []checkRow, format int, tf testFile) error {
	named := map[string]bool{}
	for _, r := range rows {
		name, ok := testRowName(format, it, r)
		if !ok {
			continue
		}
		if named[name] {
			return refuseRule("R37", "rows name the same test function %q twice", name)
		}
		if _, ok := tf.defs[name]; !ok {
			if tf.classes[name] {
				return refuseRule("R37", "row %s names %q, which is a class, not a top-level def", r.f["id"], name)
			}
			return refuseRule("R37", "row %s names %q, which is not a top-level def in the test file", r.f["id"], name)
		}
		named[name] = true
	}
	defs := make([]string, 0, len(tf.defs))
	for name := range tf.defs {
		defs = append(defs, name)
	}
	sort.Strings(defs)
	for _, name := range defs {
		if strings.HasPrefix(name, "_") || named[name] {
			continue
		}
		return refuseRule("R37", "the test file defines %q, which no test row names — a helper's name starts with _", name)
	}
	classes := make([]string, 0, len(tf.classes))
	for name := range tf.classes {
		classes = append(classes, name)
	}
	sort.Strings(classes)
	for _, name := range classes {
		if strings.HasPrefix(name, "_") {
			continue
		}
		return refuseRule("R37", "the test file defines the public class %q — a helper's name starts with _", name)
	}
	return nil
}

// probeTests is B5: every test row of the list runs once on the current tree, exactly as
// `bl run` runs a row, and the tree and .backlog/ must not move. A test exits 0 when its
// assertion holds and 1 when it is false; any other exit, a timeout, or a missing
// `bl-test: <name>` line means the test itself is broken and the list is refused. The probe
// writes no run file and no event.
func probeTests(root string, it *Item, rows []checkRow, format int) error {
	var probe []checkRow
	for _, r := range rows {
		if _, ok := testRowName(format, it, r); ok {
			probe = append(probe, r)
		}
	}
	if len(probe) == 0 {
		return nil
	}
	treeBefore, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerBefore, err := backlogHash(root)
	if err != nil {
		return err
	}
	env, cleanup, err := execEnv(root)
	if err != nil {
		return err
	}
	defer cleanup()
	var rowErr error
	for _, r := range probe {
		name, _ := testRowName(format, it, r)
		rec := execRow(root, r, env, runTimeout)
		switch {
		case rec.TimedOut:
			rowErr = refuseRule("R37", "row %s: timeout — the test did not finish", r.f["id"])
		case rec.Exit != 0 && rec.Exit != 1:
			rowErr = refuseRule("R37", "row %s: exit %d — a test exits 0 or 1; any other code means the test itself is broken", r.f["id"], rec.Exit)
		case !hasTestLine(rec.Stdout, name):
			rowErr = refuseRule("R37", "row %s: no bl-test line — the test did not print `bl-test: %s`", r.f["id"], name)
		}
		if rowErr != nil {
			break
		}
	}
	// The state checks run after the last row that ran, in every case; the refusal names the
	// first failing condition: the row, then the tree, then .backlog/.
	treeAfter, err := verifiedTree(root)
	if err != nil {
		return err
	}
	ledgerAfter, err := backlogHash(root)
	if err != nil {
		return err
	}
	if rowErr != nil {
		return rowErr
	}
	if treeAfter != treeBefore {
		return refuseRule("R37", "the tree moved under the test probe: %s before, %s after — a test changed the tree it ran against", treeBefore, treeAfter)
	}
	if ledgerAfter != ledgerBefore {
		return refuseRule("R37", "the %s directory changed under the test probe: %s before, %s after — a test wrote to the ledger, the evidence or the item files", backlogDir, ledgerBefore, ledgerAfter)
	}
	return nil
}

// hasTestLine reports whether stdout holds the line `bl-test: <name>` the run contract prints
// as its first action (I89, B4, B5).
func hasTestLine(stdout, name string) bool {
	want := "bl-test: " + name
	for _, line := range strings.Split(stdout, "\n") {
		if strings.TrimSuffix(line, "\r") == want {
			return true
		}
	}
	return false
}

// frozenTests is the test file a freeze writes (I89, B1): whether one is frozen at all, and
// the bytes the next `checks` or `rebaseline` event records a hash of.
type frozenTests struct {
	set  bool
	data []byte
}

// validateR37 applies R37's block to a list that is about to be frozen, in the order B5 gives:
// the row rules (B2, B6, B9), then the test file's parse and amendment checks (B3, B8), then
// the freeze probe (B5). It returns the test file the command will freeze, if one is frozen.
//
// supplied is the file given with --tests; suppliedSet says the flag was given, so an empty
// file is still supplied. carry says a list already frozen may lend its test file to the new
// event (an amendment or a re-baseline without --tests). insertOnly is the amendment rule of
// B8: a new test file may only insert text before the frozen file's main-block line.
func validateR37(root string, it *Item, rows []checkRow, format int, supplied []byte, suppliedSet, carry, insertOnly bool) (frozenTests, error) {
	var frozen frozenTests
	// B9: a test file is format 2's, and only a re-baseline can move a format-1 list to it.
	if suppliedSet && format == 1 {
		return frozen, refuseRule("R37", "%s: a test file needs a format-2 list — only `bl rebaseline` can move a list to format 2", it.ID)
	}
	// B8: an amendment may grow the frozen test file in one point, never introduce one.
	if suppliedSet && insertOnly && it.TestsSHA == "" {
		return frozen, refuseRule("R37", "%s has no frozen test file — an amendment may only grow one; only `bl rebaseline` can introduce one", it.ID)
	}
	if err := validateR37Rows(it, rows, format); err != nil {
		return frozen, err
	}
	willFreeze := format == 2 && (suppliedSet || (carry && it.TestsSHA != ""))
	if err := validateR37Presence(it, rows, format, willFreeze); err != nil {
		return frozen, err
	}
	if !willFreeze {
		return frozen, nil
	}
	rel := testPath(it.ID, it.Attempt)
	abs := filepath.Join(root, rel)
	before, readErr := os.ReadFile(abs)
	existed := readErr == nil
	// B8: the insertion point is the FROZEN file's main-block line, so it is read before the
	// new file replaces it.
	mainLine := 0
	if insertOnly && suppliedSet {
		tf, err := parseTestFile(root, rel)
		if err != nil {
			return frozen, err
		}
		mainLine = tf.mainLine
	}
	if suppliedSet {
		if err := atomicWrite(abs, string(supplied)); err != nil {
			return frozen, err
		}
	}
	restore := func() {
		if existed {
			_ = atomicWrite(abs, string(before))
		} else {
			_ = os.Remove(abs)
		}
	}
	// B3: the file that will be frozen after this command is the supplied one, or the frozen
	// one carried over.
	data := supplied
	if !suppliedSet {
		d, err := os.ReadFile(abs)
		if err != nil {
			restore()
			return frozen, refuseRule("R20", "the test file %s is missing", rel)
		}
		if shaHex(d) != it.TestsSHA {
			restore()
			return frozen, refuseRule("R20", "the test file %s no longer matches the hash recorded in the ledger", rel)
		}
		data = d
	}
	if insertOnly && suppliedSet {
		if err := checkInsertOnly(before, supplied, mainLine); err != nil {
			restore()
			return frozen, err
		}
	}
	tf, err := parseTestFile(root, rel)
	if err != nil {
		restore()
		return frozen, err
	}
	if err := validateR37File(it, rows, format, tf); err != nil {
		restore()
		return frozen, err
	}
	if err := probeTests(root, it, rows, format); err != nil {
		restore()
		return frozen, err
	}
	frozen.set, frozen.data = true, data
	return frozen, nil
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
	if err := requireInFlight(order, it, ""); err != nil {
		return err
	}
	// The state guards come FIRST, before the list is read: the claims freeze the list (R5) and
	// the list is written once (R4). A frozen item is therefore refused without waiting on
	// stdin, and a list that was never allowed to be written is not read at all.
	amend := a.flag("amend")
	// The format the event will carry (I89, B1): a plain `bl checks` writes format 2, and an
	// amendment keeps the format of the list it amends.
	format := 2
	if amend {
		format = it.ChecksFormat
		if format == 0 {
			format = 1
		}
	}
	// R5: the claims freeze the list. An amendment is judged on what it does to the rows, so it
	// is refused below, once its list is read; a list written plainly is refused here.
	if it.Claims != "" && !(amend && it.Checks != "") {
		return refuseRule("R5", "%s already has a claims event — the check list is frozen", it.ID)
	}
	// R4: the list is written once, and only --amend may change it.
	if it.Checks != "" && !amend {
		return refuseRule("R4", "%s already has a check list — it is written once; only --amend may change it", it.ID)
	}
	if it.Checks == "" && amend {
		return refuseRule("R4", "%s has no check list to amend", it.ID)
	}
	// B8: the frozen test file is re-hashed before the list is read (R20), so an edited test
	// file is refused at the point of use. It is a state guard, so it keeps this place.
	if err := verifyTests(root, it); err != nil {
		return err
	}
	suppliedSet := a.get("tests") != ""
	var supplied []byte
	if suppliedSet {
		data, err := os.ReadFile(a.get("tests"))
		if err != nil {
			return refuse("--tests %s: %v", a.get("tests"), err)
		}
		supplied = data
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
	if amend {
		// The amendment's scope is read off the rows (R1, R34, R4) before the state rules, so an
		// amendment that changes a row is refused under R5 after the claims, whatever else is wrong.
		if err := validateR1(rows); err != nil {
			return err
		}
		if err := validateR34(rows, format); err != nil {
			return err
		}
		if err := amendGuards(root, it, rows); err != nil {
			return err
		}
	}
	// The list's own rules, in the order §4 gives: R1 (the schema) across all rows, then R34
	// (the command's dialect), then R4 (the amend scope), then R2 (the brief clause), then R3
	// (the judge's justification), then R8 (the expectation form) — so the refusal names the
	// earliest broken rule, and within one rule the first broken row in file order.
	prevSHA, err := validateChecks(root, it, rows, amend, format)
	if err != nil {
		return err
	}
	// R37: the test file and the format-2 rows. The existing rules keep their place; this block
	// runs after all of them, and its probe runs immediately before the write (I89, B5).
	tests, err := validateR37(root, it, rows, format, supplied, suppliedSet, amend, amend && suppliedSet)
	if err != nil {
		return err
	}
	rel := attemptFile(it.ID, it.Attempt, checksName)
	ev := Event{T: "checks", ID: it.ID, Checks: rel, ChecksSHA: shaHex([]byte(text)), Format: format}
	if amend {
		ev.PrevSHA = prevSHA
	}
	if tests.set {
		ev.Tests, ev.TestsSHA = testPath(it.ID, it.Attempt), shaHex(tests.data)
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
	if err := requireInFlight(order, it, ""); err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were — before anything else on this path. The
	// frozen test file is one of them (I89, B8).
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	if err := verifyTests(root, it); err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	// The format the event will carry (I89, B9): --tests moves the list to format 2; without it
	// the list keeps its current format, and a format-2 list keeps its frozen test file.
	format := it.ChecksFormat
	if format == 0 {
		format = 1
	}
	suppliedSet := a.get("tests") != ""
	var supplied []byte
	if suppliedSet {
		data, err := os.ReadFile(a.get("tests"))
		if err != nil {
			return refuse("--tests %s: %v", a.get("tests"), err)
		}
		supplied = data
		format = 2
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
	if _, err := validateChecks(root, it, rows, false, format); err != nil {
		return err
	}
	// Then the state rules: the claims freeze the list (R5), and the list is written once (R4).
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the check list is frozen", it.ID)
	}
	if it.Checks == "" {
		return refuseRule("R4", "%s has no check list to re-baseline", it.ID)
	}
	// R37's block runs after every existing rule, and its probe immediately before the write.
	tests, err := validateR37(root, it, rows, format, supplied, suppliedSet, true, false)
	if err != nil {
		return err
	}
	rel := attemptFile(it.ID, it.Attempt, checksName)
	if err := atomicWrite(filepath.Join(root, rel), text); err != nil {
		return err
	}
	ev := Event{
		T: "rebaseline", ID: it.ID, Checks: rel,
		ChecksSHA: shaHex([]byte(text)), PrevSHA: it.ChecksSHA, Reason: reason, Format: format,
	}
	if tests.set {
		ev.Tests, ev.TestsSHA = testPath(it.ID, it.Attempt), shaHex(tests.data)
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
	if err := requireInFlight(order, it, ""); err != nil {
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
	if err := verifyTests(root, it); err != nil {
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
	rel := attemptFile(it.ID, it.Attempt, claimsName)
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
	MS           int64  `json:"ms"`
	TimedOut     bool   `json:"timed_out"`
	Signalled    bool   `json:"signalled"`
	Stdout       string `json:"stdout"`
	StdoutSHA    string `json:"stdout_sha256"`
	StdoutBinary bool   `json:"stdout_binary"`
	Stderr       string `json:"stderr"`
	StderrSHA    string `json:"stderr_sha256"`
	StderrBinary bool   `json:"stderr_binary"`
	// Verdict is the row-verdict the binary computed for a `decider: bl` row, from the row's
	// expectation and the values observed above (R7). A `decider: judge` row carries none: its
	// row-verdict is the judge's, and the judge reads this file to write it.
	Verdict string `json:"verdict,omitempty"`
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
// The order is P3's: R35 (the item is the one in flight) → R20 (the frozen artefacts are what
// they were) → R1 (the list still parses) → R7 (every row ran and its row-verdict was computed;
// R8, the expectation's form, is refused inside it) → R26 (the tree did not move) →
// R27 (the ledger did not move). A run is refused on the first of these that fails, and no run
// file is written.
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
	order, byID, err := derive(events)
	if err != nil {
		return err
	}
	// R35 (P3): the item must be the one in flight. The check comes FIRST on this path — before
	// the item's own state (a declined item, a missing check list) and before R20 — so that a run
	// on an item that is not in flight is refused under R35 and under no other rule.
	it, err := lookup(byID, a.pos(0))
	if err != nil {
		return err
	}
	if err := requireInFlight(order, it, "R35"); err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were. The brief, the check list and the claims are
	// re-hashed and compared with the hash of the latest event that recorded each of them, so a
	// frozen artefact that was edited after the freeze is refused at the point of use.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	// R20: the frozen test file is a frozen artefact too (I89, B8).
	if err := verifyTests(root, it); err != nil {
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
	// the whole of .backlog/. The tree is written to the object store, so that the round can
	// diff it against the tree the round verifies.
	treeStart, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	ledgerBefore, err := backlogHash(root)
	if err != nil {
		return err
	}
	// The scratch directory: outside the repository, and removed after the run.
	env, cleanup, err := execEnv(root)
	if err != nil {
		return err
	}
	defer cleanup()
	path := os.Getenv("PATH")
	home := envHome(env)
	tmpdir := envTmp(env)
	// R7: every row runs, in the order of the frozen list, whatever the decider. A
	// `decider: bl` row's row-verdict is computed HERE, from the row's expectation and the
	// values the run observed: the exit code is not the verdict, and a row whose command
	// refused is a PASS row when its expectation matches what the refusal printed. A
	// `decider: judge` row's row-verdict is the judge's, and the judge reads this file.
	records := make([]runRow, 0, len(rows))
	for _, row := range rows {
		rec := execRow(root, row, env, runTimeout)
		if row.f["decider"] == "bl" {
			v, err := expectVerdict(root, row.f["id"], row.f["expect"], rec, treeStart)
			if err != nil {
				return err
			}
			if rec.TimedOut {
				// R8: a timeout is recorded with exit = -1, and -1 is not a valid
				// expectation, so a timeout can never be written up as a pass.
				v = "FAIL"
			}
			rec.Verdict = v
		}
		records = append(records, rec)
	}
	// The state after the run.
	treeEnd, err := verifiedTreeWriting(root)
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
	// The run's exit code follows the rows' verdicts: a run whose rows all PASS exits 0, even
	// when a row's command exited non-zero because the binary refused. A judge row has no
	// verdict yet — the judge writes it — so it cannot fail the run.
	failed := 0
	for _, r := range records {
		if r.Verdict == "FAIL" {
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

// execEnv prepares the environment of R21 and the scratch directory it points at: PATH is
// inherited from the caller, HOME and TMPDIR point into a fresh scratch directory outside the
// repository, LANG=C, TZ=UTC, and nothing else — in particular no GIT_* and no other variable
// of the caller's. The cleanup removes the scratch directory.
func execEnv(root string) ([]string, func(), error) {
	scratch, err := scratchDir(root)
	if err != nil {
		return nil, nil, err
	}
	home := filepath.Join(scratch, "home")
	tmpdir := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, tmpdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			os.RemoveAll(scratch)
			return nil, nil, err
		}
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
		"LANG=C",
		"TZ=UTC",
	}
	return env, func() { os.RemoveAll(scratch) }, nil
}

// envHome and envTmp read the HOME and the TMPDIR out of the fixed environment.
func envHome(env []string) string { return envValue(env, "HOME") }
func envTmp(env []string) string  { return envValue(env, "TMPDIR") }

func envValue(env []string, name string) string {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return strings.TrimPrefix(e, name+"=")
		}
	}
	return ""
}

// execRow runs one row's command and records what it saw: the exit code, whether it timed out,
// the full output of each stream, and the sha256 of each.
//
// A command that exits non-zero, a command that cannot be run at all and a death by a signal are
// each recorded as what they are — never swallowed, never turned into a pass.
func execRow(root string, row checkRow, env []string, timeout time.Duration) runRow {
	rec := execCmd(root, row.f["cmd"], env, timeout)
	rec.ID = row.f["id"]
	return rec
}

// execCmd runs one command under the fixed environment and records what it saw.
func execCmd(root string, command string, env []string, timeout time.Duration) runRow {
	rec := runRow{Cmd: command}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdin = nil // /dev/null
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// Its own process group, so a timeout kills the children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		// the command could not be run at all
		rec.Exit = 127
		rec.Stderr = err.Error() + "\n"
		rec.StderrSHA = shaHex([]byte(rec.Stderr))
		rec.MS = max(0, time.Since(started).Milliseconds())
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
	rec.MS = max(0, time.Since(started).Milliseconds())
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
// directory — except .git/, the artefacts of R25 (the ledger's own directory, and the two
// rendered views, TODO.md and PROGRESS.md), and the paths .gitignore excludes — is hashed
// with `git hash-object`, and the entries are written as a tree.
//
// The index is never consulted, so a staged-but-unstaged path, a skip-worktree path and an
// assume-unchanged path cannot hide a change from the tree (R26).
func verifiedTree(root string) (string, error) {
	return buildTree(root, false)
}

// verifiedTreeWriting computes the same tree and leaves the objects in the repository's store,
// so that `git diff` can take the difference between two trees: the round's diff is taken
// between the before-tree and the verified tree, and both have to be objects git can read.
func verifiedTreeWriting(root string) (string, error) {
	return buildTree(root, true)
}

func buildTree(root string, write bool) (string, error) {
	// R25: the artefacts are never part of a verified tree, and a repository that tracks them is
	// a repository error — bl says so, wherever a tree is computed.
	reportTrackedArtefact(root)
	b := &treeBuilder{root: root, ignored: map[string]bool{}, write: write}
	if err := b.findIgnored(); err != nil {
		return "", err
	}
	sha, ok, err := b.tree("")
	if err != nil {
		return "", err
	}
	if !ok {
		// an empty working tree is git's empty tree
		return gitStdin(root, "tree", nil, write)
	}
	return sha, nil
}

// treeBuilder computes a tree from the files on disk.
type treeBuilder struct {
	root    string
	ignored map[string]bool
	// write leaves the objects in the repository's store, so that git can diff two trees.
	write bool
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
		if d.Name() == ".git" {
			// a nested .git is never part of the tree, so it is not asked about
			return nil
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
		if name == ".git" {
			// git's own metadata is never part of a tree, and neither is a nested one: a
			// submodule's gitfile is skipped as well, the way git skips it — git refuses a
			// tree that holds a .git entry, so hashing one would fail the whole tree.
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
		// R25: the two rendered views, TODO.md and PROGRESS.md, are artefacts: every event in
		// the ledger rewrites them, so they are never part of a verified tree.
		if rel == "" && (name == queueView || name == recordView) {
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
	sha, err := gitStdin(b.root, "tree", buf.Bytes(), b.write)
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
		sha, err := gitStdin(b.root, "blob", []byte(target), b.write)
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
	sha := strings.TrimSpace(out)
	if b.write {
		// leave the blob in the store, so that git can diff two trees
		if wout, wcode := git(b.root, "hash-object", "-w", "--", rel); wcode == 0 {
			sha = strings.TrimSpace(wout)
		}
	}
	return sha, mode, nil
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
// -t <type> --stdin` computes the id and leaves the object store alone. With write, the object
// is left in the store, so that git can read it back — the round's diff is taken between two
// trees, and both have to be objects git can find. Outside a git repository the write fails
// and the id is computed without it; the id is the same either way.
func gitStdin(root, typ string, data []byte, write bool) (string, error) {
	if write {
		sha, err := hashStdin(root, typ, data, true)
		if err == nil {
			return sha, nil
		}
	}
	return hashStdin(root, typ, data, false)
}

func hashStdin(root, typ string, data []byte, write bool) (string, error) {
	args := []string{"hash-object", "-t", typ}
	if write {
		args = append(args, "-w")
	}
	args = append(args, "--stdin")
	cmd := exec.Command("git", args...)
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

// ── the diff (R9, R10) ───────────────────────────────────────────────────────

// hunk is one contiguous block of changed lines in the round's diff (§2): its file, and its
// new-side range — the start line through the last line of the block. A deleted-only hunk has
// an empty new-side range.
type hunk struct {
	file  string
	start int
	count int
}

// contains reports whether a line lies in the hunk's new-side range.
func (h hunk) contains(line int) bool {
	return h.count > 0 && line >= h.start && line < h.start+h.count
}

// roundDiff computes the round's diff: the before-tree against the verified tree (§2). It
// returns the hunks and the files that changed with no textual hunk — a binary file, an empty
// file, a mode change — which only a blocking row naming the path can cover.
func roundDiff(root, before, after string) ([]hunk, []string) {
	if before == "" || after == "" {
		return nil, nil
	}
	out, code := git(root, "diff", "--no-renames", "--no-ext-diff", "--no-textconv",
		"--diff-algorithm=myers", "--unified=0", before, after)
	if code != 0 {
		// the trees are not in the store, or this is not a git repository: there is no diff
		// to check.
		return nil, nil
	}
	var hunks []hunk
	var files []string
	seen := map[string]bool{}
	cur := ""
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			cur = diffPath(strings.TrimPrefix(line, "diff --git "))
			// The ledger's own artefacts are not part of the diff the round checks: the views
			// are rendered from the ledger, so they change on every append (R25).
			if isArtifact(cur) {
				cur = ""
				continue
			}
			if !seen[cur] {
				seen[cur] = true
				files = append(files, cur)
			}
		case strings.HasPrefix(line, "@@ "):
			start, count, ok := hunkRange(line)
			if ok && cur != "" {
				hunks = append(hunks, hunk{file: cur, start: start, count: count})
			}
		}
	}
	var bare []string
	for _, f := range files {
		if !fileHasHunk(hunks, f) {
			bare = append(bare, f)
		}
	}
	return hunks, bare
}

// fileHasHunk reports whether a file carries at least one hunk.
func fileHasHunk(hunks []hunk, file string) bool {
	for _, h := range hunks {
		if h.file == file {
			return true
		}
	}
	return false
}

// diffPath reads the file path out of a `diff --git a/<p> b/<p>` line. With --no-renames the
// two paths are the same, so the path is the middle of the line.
func diffPath(rest string) string {
	if strings.HasPrefix(rest, "\"") {
		_, tail, ok := cutQuoted(rest)
		if ok {
			second, _, _ := cutQuoted(tail)
			return strings.TrimPrefix(second, "b/")
		}
		return rest
	}
	if len(rest) < 5 {
		return rest
	}
	// the line is `a/<p> b/<p>`: with --no-renames the two paths are the same.
	if (len(rest)-5)%2 == 0 {
		p := (len(rest) - 5) / 2
		first := rest[2 : 2+p]
		second := rest[3+p:]
		if strings.HasPrefix(second, "b/") && strings.TrimPrefix(second, "b/") == first {
			return first
		}
	}
	if i := strings.Index(rest, " b/"); i >= 0 {
		return rest[i+3:]
	}
	return rest
}

// cutQuoted reads one quoted string off the front of s and returns the rest.
func cutQuoted(s string) (string, string, bool) {
	if !strings.HasPrefix(s, "\"") {
		return "", s, false
	}
	end := strings.Index(s[1:], "\"")
	if end < 0 {
		return "", s, false
	}
	return s[1 : 1+end], strings.TrimSpace(s[2+end:]), true
}

// hunkRange reads the new-side range of a `@@ -a,b +c,d @@` line.
func hunkRange(line string) (int, int, bool) {
	i := strings.Index(line, "+")
	if i < 0 {
		return 0, 0, false
	}
	j := strings.Index(line[i:], " @@")
	if j < 0 {
		return 0, 0, false
	}
	spec := line[i+1 : i+j]
	count := 1
	if k := strings.Index(spec, ","); k >= 0 {
		count, _ = strconv.Atoi(spec[k+1:])
		spec = spec[:k]
	}
	start, err := strconv.Atoi(spec)
	if err != nil {
		return 0, 0, false
	}
	return start, count, true
}

// ── the scope (R28) ──────────────────────────────────────────────────────────

// inScope reports whether a repository-relative path is inside the item's scope. A scope path
// ending in `/` names every file under that directory; a path naming no file is still a scope
// entry, because a file that does not exist yet is in scope. An item with no scope is not
// constrained: the whole repository is in scope.
func inScope(scope []string, path string) bool {
	if len(scope) == 0 {
		return true
	}
	for _, s := range scope {
		s = strings.TrimSuffix(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}

// scopeIgnored reports whether the item declared an ignored file as one it may write.
func scopeIgnored(it *Item, path string) bool {
	for _, s := range it.ScopeIgnored {
		s = strings.TrimSuffix(strings.TrimSpace(s), "/")
		if s == "" {
			continue
		}
		if path == s || strings.HasPrefix(path, s+"/") {
			return true
		}
	}
	return false
}

// ignoredUnderScope lists the ignored files under the item's scope paths, with the sha256 of
// each: the before-state of R28. An ignored file is not in the verified tree, so a change to
// it is invisible to the diff — this is the list that makes it visible to the record.
func ignoredUnderScope(root string, scope []string) ([]string, error) {
	b := &treeBuilder{root: root, ignored: map[string]bool{}}
	if err := b.findIgnored(); err != nil {
		return nil, err
	}
	var out []string
	for path := range b.ignored {
		if !inScope(scope, path) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			continue
		}
		out = append(out, path+"\t"+shaHex(data))
	}
	sort.Strings(out)
	return out, nil
}

// treesMatch reports whether two trees are the same tree, ignoring the ledger's own
// artefacts. The two views are rendered from the ledger, so they change on every append.
//
// The verified tree excludes the artefacts (§2, R25), so the comparison is the narrow one the
// design describes: a ledger event between the run and the round — R14 files the advisory items
// there — rewrites the views, and the tree the round verifies does not move.
func treesMatch(root, a, b string) bool {
	if a == b {
		return true
	}
	if a == "" || b == "" {
		return false
	}
	out, code := git(root, "diff", "--no-renames", "--name-only", a, b)
	if code != 0 {
		// the trees cannot be compared: they are not the same tree.
		return false
	}
	for _, p := range strings.Fields(out) {
		if !isArtifact(p) {
			return false
		}
	}
	return true
}

// ── the round's rules ────────────────────────────────────────────────────────

// computedVerdicts reads the row-verdicts the binary computed for the `decider: bl` rows of a
// run (R7). They are computed by `bl run`, from each row's expectation and the values the run
// observed, and they are the row-verdicts of record: the judge's word for a binary row is not
// the row's verdict, and the round's derived verdict does not rest on it.
//
// A judge row has none: its row-verdict is the judge's, and it comes from the verdicts file.
func computedVerdicts(frozen []checkRow, rf *runFile) (map[string]string, error) {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	out := map[string]string{}
	for _, row := range frozen {
		if row.f["decider"] != "bl" {
			continue
		}
		id := row.f["id"]
		rec, ok := observed[id]
		if !ok {
			return nil, refuseRule("R6", "the run has no record of row %s — the round cannot take its row-verdict from the run", id)
		}
		if rec.Verdict != "PASS" && rec.Verdict != "FAIL" {
			return nil, refuseRule("R6", "the run carries no computed row-verdict for row %s — the row-verdict is the binary's, and it is computed by `bl run`", id)
		}
		out[id] = rec.Verdict
	}
	return out, nil
}

// rowVerdict is a row's row-verdict: the binary's computed verdict for a `decider: bl` row
// (R7), and the judge's for a `decider: judge` row.
func rowVerdict(id string, frozen []checkRow, vf *verdictsFile, computed map[string]string) string {
	for _, row := range frozen {
		if row.f["id"] != id {
			continue
		}
		if row.f["decider"] == "bl" {
			return computed[id]
		}
		break
	}
	return vf.rows[id].verdict
}

// checkAnchors is R9: a judge row is anchored, and only the form the anchor is written in is
// checked. An output anchor's text must appear in the row's captured stdout or stderr; a
// source anchor's file must be in the item's scope, its line must exist, its text must be at
// that line, and the line must lie in a hunk of the round's diff.
func checkAnchors(root string, it *Item, vf *verdictsFile, frozen []checkRow, rf *runFile, hunks []hunk) error {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	for _, row := range frozen {
		if row.f["decider"] != "judge" {
			continue
		}
		id := row.f["id"]
		v := vf.rows[id]
		if strings.TrimSpace(v.anchor) == "" {
			return refuseRule("R9", "row %s is a judge row: its anchor must not be empty", id)
		}
		a, err := parseAnchor(id, v.anchor)
		if err != nil {
			return err
		}
		switch a.form {
		case anchorOutput:
			rec, ok := observed[id]
			if !ok {
				return refuseRule("R9", "row %s: the run has no record of it", id)
			}
			if !strings.Contains(rec.Stdout, a.text) && !strings.Contains(rec.Stderr, a.text) {
				return refuseRule("R9", "row %s: the anchor's text %q is not in the row's captured output", id, a.text)
			}
		case anchorSource:
			if !inScope(it.Scope, a.file) {
				return refuseRule("R9", "row %s: the anchor names %s, which is not in the item's scope", id, a.file)
			}
			line, ok := sourceLine(root, a.file, a.line)
			if !ok {
				return refuseRule("R9", "row %s: %s has no line %d", id, a.file, a.line)
			}
			if !strings.Contains(line, a.text) {
				return refuseRule("R9", "row %s: the anchor's text %q is not at %s:%d", id, a.text, a.file, a.line)
			}
			if !inHunk(hunks, a.file, a.line) {
				return refuseRule("R9", "row %s: %s:%d lies in no hunk of the round's diff", id, a.file, a.line)
			}
		}
	}
	return nil
}

// sourceLine reads one line of a file of the verified tree — the working tree, which is what
// the verified tree is computed from.
func sourceLine(root, file string, line int) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(file)))
	if err != nil {
		return "", false
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	if line < 1 || line > len(lines) {
		return "", false
	}
	return lines[line-1], true
}

// inHunk reports whether a line lies in the new-side range of a hunk of the given file.
func inHunk(hunks []hunk, file string, line int) bool {
	for _, h := range hunks {
		if h.file == file && h.contains(line) {
			return true
		}
	}
	return false
}

// testCovers reads the frozen test file and returns, per test row id, the repository paths its
// function's `@_covers(...)` decorator names (I89, B7). A test row covers a hunk or a bare path
// through these, never through its cmd. The file parsed when it was frozen and B8 re-hashes it
// at the top of the round, so a parse failure here is a defect of the frozen file and is
// refused under R37 rather than silently covering nothing.
func testCovers(root string, it *Item, frozen []checkRow) (map[string]map[string]bool, error) {
	out := map[string]map[string]bool{}
	if it.ChecksFormat != 2 || it.TestsSHA == "" {
		return out, nil
	}
	tf, err := parseTestFile(root, it.Tests)
	if err != nil {
		return nil, err
	}
	for _, r := range frozen {
		name, ok := testRowName(it.ChecksFormat, it, r)
		if !ok {
			continue
		}
		paths := map[string]bool{}
		for _, p := range tf.defs[name].covers {
			paths[p] = true
		}
		out[r.f["id"]] = paths
	}
	return out, nil
}

// checkCoverage is R10: every hunk in the round's diff must be covered by at least one
// blocking row. A row covers a hunk if it is a judge row whose source anchor lies in the
// hunk's new-side range, or if its cmd contains the hunk's file path as a literal substring.
// A format-2 test row covers a hunk only through its function's `@_covers(...)` (I89, B7):
// its cmd is `python3 <test file> <name>`, which names no repository file, and it is never
// read as a path by substring. Advisory rows do not count.
func checkCoverage(vf *verdictsFile, frozen []checkRow, hunks []hunk, bare []string, covers map[string]map[string]bool) error {
	for _, h := range hunks {
		if coveredHunk(vf, frozen, h, covers) {
			continue
		}
		return refuseRule("R10", "the hunk in %s at line %d is covered by no blocking row", h.file, h.start)
	}
	for _, f := range bare {
		if coveredPath(frozen, f, covers) {
			continue
		}
		return refuseRule("R10", "the change in %s is covered by no blocking row", f)
	}
	return nil
}

// coveredHunk reports whether a blocking row covers a hunk. covers holds, for each test row,
// the paths its function's `@_covers(...)` names; a test row's cmd covers nothing.
func coveredHunk(vf *verdictsFile, frozen []checkRow, h hunk, covers map[string]map[string]bool) bool {
	for _, row := range frozen {
		if row.f["severity"] != "blocking" {
			continue
		}
		if paths, ok := covers[row.f["id"]]; ok {
			if paths[h.file] {
				return true
			}
			continue
		}
		if strings.Contains(row.f["cmd"], h.file) {
			return true
		}
		if row.f["decider"] != "judge" {
			continue
		}
		a, err := parseAnchor(row.f["id"], vf.rows[row.f["id"]].anchor)
		if err != nil {
			continue
		}
		if a.form == anchorSource && a.file == h.file && h.contains(a.line) {
			return true
		}
	}
	return false
}

// coveredPath reports whether a blocking row names a file path in its cmd. A format-2 test row
// names the path in its `@_covers(...)` instead (I89, B7).
func coveredPath(frozen []checkRow, path string, covers map[string]map[string]bool) bool {
	for _, row := range frozen {
		if row.f["severity"] != "blocking" {
			continue
		}
		if paths, ok := covers[row.f["id"]]; ok {
			if paths[path] {
				return true
			}
			continue
		}
		if strings.Contains(row.f["cmd"], path) {
			return true
		}
	}
	return false
}

// checkR14 is R14: every advisory row whose row-verdict is FAIL must already be filed as a
// backlog item, with the row id appearing in the item's text as a whitespace-delimited token,
// and that item being the most recently filed item mentioning the id.
//
// The row-verdict of a `decider: bl` row is the one the binary computed (R7), so an advisory
// binary row that failed on its own values has to be filed too.
func checkR14(vf *verdictsFile, frozen []checkRow, computed map[string]string, order []*Item) error {
	for _, row := range frozen {
		if row.f["severity"] != "advisory" {
			continue
		}
		id := row.f["id"]
		if rowVerdict(id, frozen, vf, computed) != "FAIL" {
			continue
		}
		latest := latestMention(order, id)
		if latest == nil {
			return refuseRule("R14", "the advisory row %s failed and is not filed as an item yet — file it with `bl file --id <id> --text \"... %s ...\"`", id, id)
		}
		if !hasToken(latest.Text, id) {
			return refuseRule("R14", "the advisory row %s failed: the most recently filed item mentioning it (%s) does not name %s as a whole token", id, latest.ID, id)
		}
	}
	return nil
}

// latestMention returns the most recently filed item whose text mentions the id at all.
func latestMention(order []*Item, id string) *Item {
	var latest *Item
	for _, it := range order {
		if !strings.Contains(it.Text, id) {
			continue
		}
		if latest == nil || it.FiledAt >= latest.FiledAt {
			latest = it
		}
	}
	return latest
}

// hasToken reports whether the text holds the token as a whole word.
func hasToken(text, token string) bool {
	for _, f := range strings.Fields(text) {
		if f == token {
			return true
		}
	}
	return false
}

// checkR16 is R16: a second check that disagrees with the row's recorded verdict refuses the
// round. A second-check event computed against an earlier run is stale and is ignored.
//
// The row's recorded verdict is the binary's computed verdict for a `decider: bl` row (R7) and
// the judge's for a `decider: judge` row.
func checkR16(root string, it *Item, vf *verdictsFile, frozen []checkRow, computed map[string]string, runID string) error {
	for _, ev := range it.SecondChecks {
		if ev.Run != runID {
			continue
		}
		if _, ok := vf.rows[ev.Row]; !ok {
			continue
		}
		recorded := rowVerdict(ev.Row, frozen, vf, computed)
		if ev.Computed == recorded {
			continue
		}
		// A misclassified event is the one kind of refusal that writes to the ledger: it is
		// not a round and it consumes no code round (R16).
		if _, err := appendEvent(root, Event{
			T: "misclassified", ID: it.ID, Row: ev.Row, Computed: ev.Computed, Run: ev.Run,
			Cmd: ev.Cmd, Expect: ev.Expect,
		}); err != nil {
			return err
		}
		return refuseRule("R16", "the second check computed %s for row %s and the verdicts file records %s — the row is misclassified", ev.Computed, ev.Row, recorded)
	}
	return nil
}

// checkR28 is R28: an ignored file under the item's scope may not change, unless the item
// declared it as one it may write.
func checkR28(root string, it *Item) error {
	if len(it.IgnoredBefore) == 0 {
		return nil
	}
	want := map[string]string{}
	for _, entry := range it.IgnoredBefore {
		if i := strings.Index(entry, "\t"); i > 0 {
			want[entry[:i]] = entry[i+1:]
		}
	}
	now, err := ignoredUnderScope(root, it.Scope)
	if err != nil {
		return err
	}
	got := map[string]string{}
	for _, entry := range now {
		if i := strings.Index(entry, "\t"); i > 0 {
			got[entry[:i]] = entry[i+1:]
		}
	}
	for path, sha := range want {
		if got[path] == sha {
			continue
		}
		if scopeIgnored(it, path) {
			continue
		}
		return refuseRule("R28", "the ignored file %s changed under the item's scope — the item did not declare it as one it may write", path)
	}
	return nil
}

// ── R25: the artefacts ───────────────────────────────────────────────────────

// trackedArtefact says whether the repository tracks the artefact directory `.backlog/`. The
// artefacts are never part of a verified tree, and a repository that tracks them is a repository
// error.
func trackedArtefact(root string) bool {
	out, code := git(root, "ls-files", "--", backlogDir)
	return code == 0 && strings.TrimSpace(out) != ""
}

// trackedArtefactMessage is the one sentence R25 uses about a repository that tracks `.backlog/`:
// it is said wherever a tree is computed, and it is what the round refuses under.
func trackedArtefactMessage() string {
	return fmt.Sprintf("the %s directory is tracked in this repository — it is an artefact directory and must never be tracked", backlogDir)
}

// artefactReported remembers the repositories whose tracked artefacts have already been said, so
// that a command that computes several trees says it once.
var artefactReported = struct {
	sync.Mutex
	reported map[string]bool
}{reported: map[string]bool{}}

// reportTrackedArtefact says so when `.backlog/` is tracked: the artefacts are excluded from every
// verified tree, and a repository that tracks them is a repository error (R25). It is a report and
// not a refusal — the command that computed the tree carries on and writes its evidence as usual.
func reportTrackedArtefact(root string) {
	if !trackedArtefact(root) {
		return
	}
	artefactReported.Lock()
	said := artefactReported.reported[root]
	artefactReported.reported[root] = true
	artefactReported.Unlock()
	if said {
		return
	}
	fmt.Fprintf(os.Stderr, "bl: %s\n", trackedArtefactMessage())
}

// checkR25 is R25: `.backlog/` is never part of a verified tree, and a repository that tracks it
// is a repository error — so the round refuses rather than comparing trees that include it.
func checkR25(root string) error {
	if !trackedArtefact(root) {
		return nil
	}
	return refuseRule("R25", "%s", trackedArtefactMessage())
}

// ── the evidence file ────────────────────────────────────────────────────────

// evidenceText writes the evidence file of a round (§2): one line per frozen check id, in the
// order the list was frozen, with the run's observed values — so the claim that a command ran
// is never something a model typed into a cell.
//
// The row_verdict column holds the row's row-verdict: the binary's computed verdict for a
// `decider: bl` row (R7) and the judge's for a `decider: judge` row.
func evidenceText(vf *verdictsFile, frozen []checkRow, rf *runFile, computed map[string]string) string {
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	var b strings.Builder
	b.WriteString("id\tcmd\texit\tsignalled\ttimed_out\tobserved\texcerpt\trow_verdict\n")
	for _, row := range frozen {
		id := row.f["id"]
		rec := observed[id]
		v := vf.rows[id]
		seen := fmt.Sprintf("stdout sha %s, stderr sha %s", rec.StdoutSHA, rec.StderrSHA)
		if v.anchor != "" {
			seen += "; anchor " + v.anchor
		}
		fmt.Fprintf(&b, "%s\t%s\t%d\t%t\t%t\t%s\t%s\t%s\n",
			id, rec.Cmd, rec.Exit, rec.Signalled, rec.TimedOut, seen, excerpt(rec), rowVerdict(id, frozen, vf, computed))
	}
	return b.String()
}

// excerpt is the human-readable excerpt of a row's output: the first 4096 bytes of each
// stream, with the control characters escaped so the evidence file stays one line per row.
func excerpt(rec runRow) string {
	out := flattenStream(rec.Stdout, rec.StdoutBinary)
	if rec.Stderr != "" {
		if out != "" {
			out += " | "
		}
		out += flattenStream(rec.Stderr, rec.StderrBinary)
	}
	return out
}

func flattenStream(s string, binary bool) string {
	if binary {
		return "(binary output)"
	}
	const limit = 4096
	if len(s) > limit {
		s = s[:limit]
	}
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '\n':
			b.WriteString("\\n")
		case '\r':
			b.WriteString("\\r")
		case '\t':
			b.WriteString("\\t")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// ── the expectation (R8) ─────────────────────────────────────────────────────

// expectVerdict computes a row's verdict from the observed values and an expectation of R8's
// five forms. It is what `bl second-check` records (R16).
func expectVerdict(root, id, expect string, rec runRow, tree string) (string, error) {
	switch {
	case strings.HasPrefix(expect, "exit "):
		code := strings.TrimPrefix(expect, "exit ")
		n, err := strconv.Atoi(code)
		if err != nil {
			return "", refuseRule("R8", "row %s: `exit` takes an integer 0-255, not %q", id, code)
		}
		if rec.Exit == n {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "stdout matches "):
		re, err := regexp.Compile(expect[len("stdout matches "):])
		if err != nil {
			return "", refuseRule("R8", "row %s: regex %q is not valid RE2: %v", id, expect, err)
		}
		if re.MatchString(rec.Stdout) {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "stderr matches "):
		re, err := regexp.Compile(expect[len("stderr matches "):])
		if err != nil {
			return "", refuseRule("R8", "row %s: regex %q is not valid RE2: %v", id, expect, err)
		}
		if re.MatchString(rec.Stderr) {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "file exists "):
		path := strings.TrimPrefix(expect, "file exists ")
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); err == nil {
			return "PASS", nil
		}
		return "FAIL", nil
	case strings.HasPrefix(expect, "tree == "):
		hash := strings.TrimPrefix(expect, "tree == ")
		if tree == hash {
			return "PASS", nil
		}
		return "FAIL", nil
	}
	return "", refuseRule("R8", "row %s: expect %q is not one of: exit <code>, stdout matches <regex>, stderr matches <regex>, file exists <path>, tree == <hash>", id, expect)
}

// cmdSecondCheck is R16's verb: the orchestrator dispatches a second judge to write a command
// that decides a `decider: judge` row, and records it here. The command runs under R21's fixed
// environment, the verdict is computed from it with R8's expectation forms, and the computed
// verdict is recorded in a `second-check` event naming the run it was computed against.
//
// `bl second-check` must run BEFORE `bl round`: the round refuses if a second check computed
// against that run disagrees with the row's recorded row-verdict.
func cmdSecondCheck(root string, a args) error {
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
	// R20: the frozen artefacts are what they were.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	if err := verifyTests(root, it); err != nil {
		return err
	}
	row := strings.TrimSpace(a.get("row"))
	if row == "" {
		return refuse("--row is required")
	}
	command := a.get("cmd")
	if strings.TrimSpace(command) == "" {
		return refuse("--cmd is required")
	}
	expect := strings.TrimSpace(a.get("expect"))
	if expect == "" {
		return refuse("--expect is required")
	}
	// The row must be one of the frozen list's rows.
	frozen, err := frozenRows(root, it)
	if err != nil {
		return err
	}
	found := false
	for _, r := range frozen {
		if r.f["id"] == row {
			found = true
		}
	}
	if !found {
		return refuseRule("R1", "row %s is not in the frozen check list", row)
	}
	// R8: the expectation is one of the five forms.
	if err := checkExpect(row, expect); err != nil {
		return err
	}
	// The run the check is computed against.
	runEv, ok := theRun(it)
	if !ok {
		return refuseRule("R6", "%s has no run — a second check is computed against a run", it.ID)
	}
	env, cleanup, err := execEnv(root)
	if err != nil {
		return err
	}
	defer cleanup()
	rec := execCmd(root, command, env, runTimeout)
	tree, err := verifiedTree(root)
	if err != nil {
		return err
	}
	computed, err := expectVerdict(root, row, expect, rec, tree)
	if err != nil {
		return err
	}
	ev := Event{
		T: "second-check", ID: it.ID, Row: row, Run: runEv.Run,
		Computed: computed, Cmd: command, Expect: expect, MS: &rec.MS,
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s second check for row %s computed %s against %s\n", it.ID, row, computed, runEv.Run)
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
	// R35 (P2): the item must be the one in flight. The shared helper is the one `bl run` uses,
	// so both verbs refuse the same way and name the same rule.
	if err := requireInFlight(order, it, "R35"); err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were when the round was written against.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	if err := verifyTests(root, it); err != nil {
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
	// The flags. The verdict is derived (R12), so --verdict is optional; the cause is derived
	// on a dispute round (R13), so --cause is optional there too.
	verdict := strings.ToUpper(strings.TrimSpace(a.get("verdict")))
	if verdict != "" && verdict != "PASS" && verdict != "FAIL" {
		return refuse("--verdict must be one of %s", strings.Join(verdicts, ", "))
	}
	cause := strings.ToLower(strings.TrimSpace(a.get("cause")))
	if cause != "" && cause != "code" && cause != "harness" && cause != "dispute" {
		return refuse("--cause must be one of %s", strings.Join(causes, "|"))
	}
	finding := strings.TrimSpace(a.get("finding"))
	// A harness round carries no rows: the verifier did not run, so there is nothing to
	// record and nothing to hash. It skips R6, R9, R10, R12 and R14 (P2), and stops here.
	if cause == "harness" {
		if verdict != "FAIL" {
			return refuseRule("R23", "a harness round is a failure — it needs --verdict FAIL")
		}
		return recordHarnessRound(root, it, a)
	}
	// R6: the verdicts file. It must match the frozen list, name the item's latest run, and
	// name the tree the round verifies.
	if a.get("rows") == "" {
		return refuseRule("R6", "a round needs the verdicts file: --rows <file>")
	}
	text, err := a.rows()
	if err != nil {
		return err
	}
	frozen, err := frozenRows(root, it)
	if err != nil {
		return err
	}
	vf, err := parseVerdicts(text, frozen)
	if err != nil {
		return err
	}
	runEv, ok := theRun(it)
	if !ok {
		return refuseRule("R6", "%s has no run — the verdicts file must name the item's latest run", it.ID)
	}
	if vf.run != runEv.Run {
		return refuseRule("R6", "the verdicts file names run %s, but the item's latest run is %s", vf.run, runEv.Run)
	}
	rel, ok := runFileRel(it.ID, runEv.Run)
	if !ok {
		return refuseRule("R6", "the run %s names no run file", runEv.Run)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return refuseRule("R6", "the run file %s is missing", rel)
	}
	if shaHex(data) != runEv.RunSHA {
		return refuseRule("R6", "the run file %s no longer matches the hash recorded in the ledger", rel)
	}
	var rf runFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return refuseRule("R6", "the run file %s is not JSON: %v", rel, err)
	}
	// The tree pin: the round verifies the tree itself, from the filesystem, the same way
	// `bl run` does, and the verdicts file must name it.
	tree, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	if !treesMatch(root, vf.tree, tree) {
		return refuseRule("R6", "the verdicts file names the tree %s, but the current verified tree is %s", vf.tree, tree)
	}
	if !treesMatch(root, vf.tree, rf.TreeStart) {
		return refuseRule("R6", "the verdicts file names the tree %s, but the run %s verified the tree %s", vf.tree, runEv.Run, rf.TreeStart)
	}
	if it.Claims == "" {
		return refuseRule("R6", "%s has no claims event — the claims are frozen before the round", it.ID)
	}
	for _, r := range it.attemptRounds() {
		if r.Run == runEv.Run {
			return refuseRule("R6", "the run %s has already been consumed by a round", runEv.Run)
		}
	}
	// R28: no ignored file under the item's scope changed.
	if err := checkR28(root, it); err != nil {
		return err
	}
	// R25: the artefacts are excluded from the tree.
	if err := checkR25(root); err != nil {
		return err
	}
	// R7: the round consumes the run, and the row-verdicts of the `decider: bl` rows are the
	// ones the binary computed from the run's observed values. The judge's word for a binary
	// row is not the row's verdict, so the derivation in R12 does not rest on it.
	computed, err := computedVerdicts(frozen, &rf)
	if err != nil {
		return err
	}
	// The round's diff: the before-tree against the verified tree.
	hunks, bare := roundDiff(root, it.BeforeTree, tree)
	// R9: the anchors.
	if err := checkAnchors(root, it, vf, frozen, &rf, hunks); err != nil {
		return err
	}
	// R10: the hunk coverage. A format-2 test row covers through its `@_covers(...)` (B7).
	covers, err := testCovers(root, it, frozen)
	if err != nil {
		return err
	}
	if err := checkCoverage(vf, frozen, hunks, bare, covers); err != nil {
		return err
	}
	// R16: a second check that disagrees with the row's recorded verdict.
	if err := checkR16(root, it, vf, frozen, computed, runEv.Run); err != nil {
		return err
	}
	// R13: a finding is a dispute, whatever R12 derived.
	dispute := false
	if vf.found {
		if vf.finding == "" {
			return refuseRule("R13", "the verdicts file carries a `# finding:` line with no finding text")
		}
		dispute = true
	}
	if finding != "" {
		dispute = true
	}
	if dispute && finding == "" {
		finding = vf.finding
	}
	// R14: a failing advisory row must already be filed.
	if err := checkR14(vf, frozen, computed, order); err != nil {
		return err
	}
	// R12: the verdict is derived. FAIL if any blocking row's row-verdict is FAIL. The
	// row-verdict of a `decider: bl` row is the one the binary computed (R7); the row-verdict
	// of a `decider: judge` row is the judge's.
	derived := "PASS"
	blockingFailed := false
	for _, row := range frozen {
		if row.f["severity"] != "blocking" {
			continue
		}
		if rowVerdict(row.f["id"], frozen, vf, computed) == "FAIL" {
			derived = "FAIL"
			blockingFailed = true
			break
		}
	}
	if dispute {
		// R13 overrides the derived verdict: a dispute is never a PASS.
		derived = "FAIL"
	}
	if verdict != "" && verdict != derived {
		return refuseRule("R12", "the derived verdict is %s — a submitted --verdict %s is refused", derived, verdict)
	}
	// R23: the cause is constrained.
	switch {
	case dispute:
		if cause == "code" || cause == "harness" {
			return refuseRule("R23", "the cause is derived: a round with a finding is a dispute — a supplied --cause %s is refused", cause)
		}
		cause = "dispute"
	case derived == "FAIL":
		if cause == "" {
			return refuseRule("R23", "a FAIL needs --cause %s", strings.Join(causes, "|"))
		}
		if cause == "dispute" {
			return refuseRule("R23", "--cause dispute is accepted only when the round carries a finding")
		}
		if cause == "harness" {
			return refuseRule("R23", "--cause harness is accepted only when the verdicts file is absent or carries no rows")
		}
		if !blockingFailed {
			return refuseRule("R23", "--cause code is accepted only when a blocking row has row-verdict FAIL")
		}
	default:
		if cause != "" {
			return refuseRule("R23", "a PASS round carries no cause")
		}
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
	// The evidence file: one line per frozen check id, in the order the list was frozen, with
	// the run's observed values.
	p := newPaths(root)
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	name := fmt.Sprintf("%s-r%d.txt", it.ID, len(it.Rounds)+1)
	body := evidenceText(vf, frozen, &rf, computed)
	if err := atomicWrite(filepath.Join(p.evidence, name), body); err != nil {
		return err
	}
	ev := Event{
		T: "round", ID: it.ID, Verdict: derived, Cause: cause,
		Run: runEv.Run, Tree: tree,
		Rows:    backlogDir + "/" + evidenceDir + "/" + name,
		RowsSHA: shaHex([]byte(body)),
	}
	if finding != "" {
		ev.Finding = finding
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if cause != "" {
		fmt.Printf("bl: %s r%d %s (%s)\n", it.ID, len(it.Rounds)+1, derived, cause)
	} else {
		fmt.Printf("bl: %s r%d %s\n", it.ID, len(it.Rounds)+1, derived)
	}
	return nil
}

// frozenRows reads the item's frozen check list: the rows the round is written against.
func frozenRows(root string, it *Item) ([]checkRow, error) {
	if it.Checks == "" {
		return nil, refuseRule("R1", "%s has no check list", it.ID)
	}
	list, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return nil, err
	}
	rows, err := parseChecks(string(list))
	if err != nil {
		return nil, refuseRule("R1", "the stored check list %s does not parse: %v", it.Checks, err)
	}
	return rows, nil
}

// theRun returns the current attempt's latest run: the event that recorded it (R36). A run of an
// attempt a restart ended is void, and a round of the new attempt cannot cite it.
func theRun(it *Item) (Event, bool) {
	runs := it.attemptRuns()
	if len(runs) == 0 {
		return Event{}, false
	}
	return runs[len(runs)-1], true
}

// recordHarnessRound records a harness round: the verifier did not run, so the round carries
// no rows and no evidence file (R23).
func recordHarnessRound(root string, it *Item, a args) error {
	if err := completeRefused(it); err != nil {
		return err
	}
	if err := budgetRefused(it, "harness"); err != nil {
		return err
	}
	ev := Event{T: "round", ID: it.ID, Verdict: "FAIL", Cause: "harness"}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s r%d FAIL (harness)\n", it.ID, len(it.Rounds)+1)
	return nil
}

// harnessRowsRefused is R23's harness clause, at the place P2 gives it: a harness round has no
// rows and stops here. A harness round carries no rows because the verifier did not run, so a
// rows file with content is refused and the round is not recorded at all. A harness round with no
// rows file, or with an empty one, is accepted and is recorded with no rows.
//
// A harness round carries no FINDING either: the verifier did not run, so it found nothing, and
// a finding is never silently ignored (R13). A `--finding`, or a `# finding:` line in the rows
// file, is refused under R23 for the same reason the rows are.
//
// The rows file is read to see whether it holds rows. ABSENT means the flag was not given, or
// the path does not exist: an absent file holds no rows, and the round is accepted and recorded
// with no rows. A path that exists and cannot be read as a file — a directory, or a file that
// cannot be opened — is refused, not treated as empty: a caller who meant to hand over rows and
// handed over something else has made a mistake, and a mistake is not a run that never happened.
func harnessRowsRefused(a args) error {
	if strings.ToLower(strings.TrimSpace(a.get("cause"))) != "harness" {
		return nil
	}
	if strings.ToUpper(strings.TrimSpace(a.get("verdict"))) != "FAIL" {
		// a PASS round carries no cause at all, and that refusal is raised where the verdict is
		// read — this clause is about a harness round, and a PASS round is not one.
		return nil
	}
	if strings.TrimSpace(a.get("finding")) != "" {
		return refuseRule("R23", "a harness round carries no finding — the verifier did not run, so it found nothing")
	}
	path := a.get("rows")
	if path == "" {
		// absent: the flag was not given
		return nil
	}
	what := "the rows file " + path
	if path == "-" {
		what = "the rows on stdin"
	}
	rows, err := harnessRowsText(path)
	if err != nil {
		return err
	}
	if strings.TrimSpace(rows) == "" {
		return nil
	}
	if hasFindingComment(rows) {
		return refuseRule("R23", "a harness round carries no finding — the verifier did not run, so it found nothing")
	}
	return refuseRule("R23", "a harness round carries no rows — the verifier did not run, so %s must be empty", what)
}

// harnessRowsText reads the rows a harness round was handed, or refuses under R23 when the path
// exists and cannot be read as a file.
//
// Absent means the flag was not given, or the path does not exist: a path that does not exist
// holds no rows, and it is not a mistake. A path that exists and cannot be read as a file — a
// directory, or a file that cannot be opened — is refused: a caller who meant to hand over rows
// and handed over something else has made a mistake, and a mistake is not a run that never
// happened.
func harnessRowsText(path string) (string, error) {
	if path == "-" {
		// stdin is not a path: it is read as it comes, and the design's clause is about a path.
		// A stdin that cannot be read holds no rows, as it did before.
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", nil
		}
		return string(data), nil
	}
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			// absent: the path does not exist
			return "", nil
		}
		return "", refuseRule("R23", "the rows file %s cannot be read as a file (%v) — a harness round reads its rows from a file, and a path that exists but cannot be read as a file is refused, not absent", path, err)
	}
	if info.IsDir() {
		return "", refuseRule("R23", "the rows file %s is a directory — a harness round reads its rows from a file, and a path that exists but cannot be read as a file is refused, not absent", path)
	}
	if !info.Mode().IsRegular() {
		return "", refuseRule("R23", "the rows file %s is not a regular file — a harness round reads its rows from a file, and a path that exists but cannot be read as a file is refused, not absent", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", refuseRule("R23", "the rows file %s cannot be opened (%v) — a harness round reads its rows from a file, and a path that exists but cannot be read as a file is refused, not absent", path, err)
	}
	return string(data), nil
}

// hasFindingComment reports whether a text carries a `# finding:` comment line — the line R13
// reads.
func hasFindingComment(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "# finding:") {
			return true
		}
	}
	return false
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
	if err := verifyTests(root, it); err != nil {
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

// ── the restart (R36) ────────────────────────────────────────────────────────

// maxAttempts is the number of attempts one item may have: the first one and two restarts. A
// restart of an item in its last attempt parks it for a human instead.
const maxAttempts = 3

// restartClasses are the four failure classes a restart records (R36). The class is recorded, not
// cross-checked against the evidence: it says what the producer concluded, and the failure record
// beside it holds the rows and the damaged files the next reader can check.
var restartClasses = []string{"bad-check", "extra-file", "damaged-evidence", "wrong-premise"}

func validRestartClass(class string) bool {
	for _, c := range restartClasses {
		if c == class {
			return true
		}
	}
	return false
}

// hasFailRound reports whether the current attempt has a FAIL round of any cause (R36).
func hasFailRound(it *Item) bool {
	for _, r := range it.attemptRounds() {
		if r.Verdict == "FAIL" {
			return true
		}
	}
	return false
}

// cmdRestart ends the item's current attempt and starts the next one (R36). It writes the failure
// record of the attempt it ends, appends one `restart` event naming the record, and leaves the
// item open and out of flight, with everything that belongs to the attempt cleared — so the next
// attempt opens its own brief, freezes its own list and makes its own claims, and none of the
// earlier attempt's files is rewritten.
//
// The refusals are in B8's order, one rule per call: an unknown id, the item's state, the class,
// the texts, the limit, and whether the attempt has anything to teach. The limit is the one
// refusal that writes: it parks the item for a human, because a fourth attempt is not the
// binary's call. A restart never refuses because an artefact's hash does not match — the record
// it is about to write is what excuses that damage.
func cmdRestart(root string, a args) error {
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
	class := strings.TrimSpace(a.get("reason"))
	switch it.Status {
	case "declined":
		return refuseRule("R36", "%s is declined — a restart ends an attempt that was worked, and a decline was never worked", it.ID)
	case "parked":
		return refuseRule("R36", "%s is parked — run `bl reopen %s` first; a reopen does not change the attempt", it.ID, it.ID)
	}
	if it.Status == "closed" && class != "damaged-evidence" {
		return refuseRule("R36", "%s is closed — only `--reason damaged-evidence` restarts a closed item, because the close recorded a PASS its evidence no longer supports", it.ID)
	}
	if !validRestartClass(class) {
		return refuseRule("R36", "--reason must be one of %s, not %q", strings.Join(restartClasses, "|"), class)
	}
	why, err := cleanText(a.get("why"), "why")
	if err != nil {
		return err
	}
	change, err := cleanText(a.get("change"), "change")
	if err != nil {
		return err
	}
	for _, r := range it.Restarts {
		if strings.TrimSpace(r.Change) == change {
			return refuseRule("R36", "%s already restarted with the change %q — a restart that changes nothing new is a fix round in disguise", it.ID, change)
		}
	}
	// The limit: three attempts. The refusal that records it parks the item, so a fourth attempt
	// is a human's decision; it comes before the "nothing to learn" check, so an item in its last
	// attempt is parked whether or not it has a FAIL round.
	if it.Attempt >= maxAttempts {
		if it.Status == "closed" {
			return refuseRule("R36", "%s is closed and in attempt %d — the restart limit is %d attempts, so a human decides what happens next", it.ID, it.Attempt, maxAttempts)
		}
		reason := fmt.Sprintf("restart limit: attempt %d is the last of %d, so a human decides what happens next", it.Attempt, maxAttempts)
		if _, err := appendEvent(root, Event{T: "defer", ID: it.ID, Reason: reason}); err != nil {
			return err
		}
		return refuseRule("R36", "%s has spent its two restarts (attempt %d) — the restart limit parks it for a human to decide", it.ID, it.Attempt)
	}
	// Nothing to learn from: no FAIL round of any cause, and nothing the artefact scan would list.
	// A closed item's passing round is not a reason to restart it — only damage is.
	damaged := damagedArtefacts(root, events, it)
	if it.Status == "closed" {
		if len(damaged) == 0 {
			return refuseRule("R36", "%s is closed and its artefacts are intact — there is no damage for a restart to excuse", it.ID)
		}
	} else if !hasFailRound(it) && len(damaged) == 0 {
		return refuseRule("R36", "%s attempt %d has no FAIL round and no damaged artefact — fix the item instead of restarting it", it.ID, it.Attempt)
	}
	// The failure record is written before the event that names it, so an event never names a
	// record that is not there.
	n := it.Attempt
	rel := failureFile(it.ID, n)
	body := failureRecordText(root, events, it, class, why, change)
	if err := atomicWrite(filepath.Join(root, rel), body); err != nil {
		return err
	}
	ev := Event{
		T: "restart", ID: it.ID, Reason: class, Why: why, Change: change,
		Attempt: &n, Record: rel, RecordSHA: shaHex([]byte(body)),
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s restarted as attempt %d\n", it.ID, n+1)
	return nil
}

// failureRecordText is the record a restart writes for the attempt it ends (R36): the class, the
// reason, the change, the failing blocking rows of the attempt's last FAIL round that carries
// rows, and every artefact of the attempt whose bytes no longer match the hash its latest event
// recorded. It holds no timestamp, so the same attempt written twice is the same bytes.
func failureRecordText(root string, events []Event, it *Item, class, why, change string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Failure record: %s attempt %d\n", it.ID, it.Attempt)
	fmt.Fprintf(&b, "class: %s\n", class)
	fmt.Fprintf(&b, "why: %s\n", why)
	fmt.Fprintf(&b, "change: %s\n", change)
	b.WriteString("rows:\n")
	rows := failureRows(root, it)
	if len(rows) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, line := range rows {
		b.WriteString(line)
	}
	b.WriteString("artefacts:\n")
	arts := damagedArtefacts(root, events, it)
	if len(arts) == 0 {
		b.WriteString("- (none)\n")
	}
	for _, line := range arts {
		b.WriteString(line)
	}
	return b.String()
}

// failureRows is the `rows:` block of a failure record: the rows that are blocking in the
// attempt's check list and FAIL in the evidence of the attempt's last FAIL round that carries
// rows, with the exit code and the output the run that round names recorded.
//
// The files are read as they are on disk, hashes unverified: the record describes what is there.
// A file that is missing or does not parse, or a row the run file has no record of, yields no
// line for that row — the record does not invent what it could not read.
func failureRows(root string, it *Item) []string {
	if it.Checks == "" {
		return nil
	}
	list, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(it.Checks)))
	if err != nil {
		return nil
	}
	frozen, err := parseChecks(string(list))
	if err != nil {
		return nil
	}
	round, ok := lastFailRoundWithRows(it)
	if !ok {
		return nil
	}
	verdicts := evidenceVerdicts(root, round.Rows)
	if verdicts == nil {
		return nil
	}
	rf, ok := runFileOf(root, it.ID, round.Run)
	if !ok {
		return nil
	}
	observed := map[string]runRow{}
	for _, r := range rf.Rows {
		observed[r.ID] = r
	}
	var out []string
	for _, row := range frozen {
		id := row.f["id"]
		if row.f["severity"] != "blocking" || verdicts[id] != "FAIL" {
			continue
		}
		rec, ok := observed[id]
		if !ok {
			continue
		}
		out = append(out, fmt.Sprintf("- %s exit %d\n", id, rec.Exit))
		for _, line := range outputLines(rec) {
			out = append(out, "  | "+line+"\n")
		}
	}
	return out
}

// lastFailRoundWithRows is the attempt's last FAIL round that wrote an evidence file — the round
// whose rows a failure record reads (R36).
func lastFailRoundWithRows(it *Item) (Event, bool) {
	rounds := it.attemptRounds()
	for i := len(rounds) - 1; i >= 0; i-- {
		if rounds[i].Verdict == "FAIL" && rounds[i].Rows != "" {
			return rounds[i], true
		}
	}
	return Event{}, false
}

// evidenceVerdicts reads the row-verdicts out of an evidence file, by column name, as they are on
// disk. A file that cannot be read, or that holds no header, yields nil.
func evidenceVerdicts(root, rel string) map[string]string {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) == 0 {
		return nil
	}
	header := strings.Split(strings.TrimRight(lines[0], "\r"), "\t")
	out := map[string]string{}
	for _, raw := range lines[1:] {
		cells := strings.Split(strings.TrimRight(raw, "\r"), "\t")
		if len(cells) != len(header) {
			continue
		}
		id, verdict := "", ""
		for i, h := range header {
			switch h {
			case "id":
				id = cells[i]
			case "row_verdict":
				verdict = cells[i]
			}
		}
		if id != "" {
			out[id] = verdict
		}
	}
	return out
}

// runFileOf reads one of the item's run files as it is on disk.
func runFileOf(root, id, run string) (runFile, bool) {
	rel, ok := runFileRel(id, run)
	if !ok {
		return runFile{}, false
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		return runFile{}, false
	}
	var rf runFile
	if err := json.Unmarshal(data, &rf); err != nil {
		return runFile{}, false
	}
	return rf, true
}

// outputLines is a row's output as record lines: stdout first, then stderr, each split on LF with
// one trailing empty segment dropped, so `ok\n` is one line. A stream that was not UTF-8 is the
// one line `(binary output)`. At most 20 lines are kept per row across both streams, and when
// more were cut the 21st says so.
func outputLines(rec runRow) []string {
	lines := append(streamLines(rec, true), streamLines(rec, false)...)
	const limit = 20
	if len(lines) > limit {
		lines = append(append([]string{}, lines[:limit]...), "... (truncated)")
	}
	return lines
}

// streamLines is one stream of a row's output as lines: a binary stream is the single line
// `(binary output)`, and a text stream is split on LF with one trailing empty segment dropped.
func streamLines(rec runRow, stdout bool) []string {
	s, binary := rec.Stdout, rec.StdoutBinary
	if !stdout {
		s, binary = rec.Stderr, rec.StderrBinary
	}
	if binary {
		return []string{"(binary output)"}
	}
	lines := strings.Split(s, "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	return lines
}

// damagedArtefacts is the `artefacts:` block of a failure record, and the scan a restart refuses
// on when it lists nothing. It covers every path an event of the attempt recorded — the brief,
// the check list, the claims, the run file and the round evidence — whose bytes no longer match
// the hash the LATEST event of the attempt naming that path recorded: one line per path, in the
// order the paths were first recorded. A brief or a list amended in place is not listed, because
// the amendment's own event is the latest and the file matches it.
func damagedArtefacts(root string, events []Event, it *Item) []string {
	var order []string
	latest := map[string]string{}
	add := func(rel, sha string) {
		if rel == "" || sha == "" {
			return
		}
		if _, seen := latest[rel]; !seen {
			order = append(order, rel)
		}
		latest[rel] = sha
	}
	for _, ev := range events {
		if ev.Seq <= it.restartSeq || !strings.EqualFold(ev.ID, it.ID) {
			continue
		}
		switch ev.T {
		case "open", "brief":
			add(ev.Brief, ev.BriefSHA)
		case "checks", "rebaseline":
			add(ev.Checks, ev.ChecksSHA)
		case "claims":
			add(ev.Claims, ev.ClaimsSHA)
		case "run":
			if rel, ok := runFileRel(it.ID, ev.Run); ok {
				add(rel, ev.RunSHA)
			}
		case "round":
			add(ev.Rows, ev.RowsSHA)
		}
	}
	var out []string
	for _, rel := range order {
		want := latest[rel]
		now := "missing"
		if data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel))); err == nil {
			now = shaHex(data)
			if now == want {
				continue
			}
		}
		out = append(out, fmt.Sprintf("- %s recorded %s now %s\n", rel, want, now))
	}
	return out
}

// ── the restarts `bl check` excuses (R36) ────────────────────────────────────

// namedArtefact is one recorded artefact of an attempt a restart ended.
type namedArtefact struct{ kind, path, sha string }

// voidArtefacts is the frozen artefacts of the item's earlier attempts: each path with the hash
// the latest event of a VOID attempt that named it recorded, in the order the paths were first
// recorded. The current attempt's artefacts are verified by verifyFrozen; an earlier attempt's are
// reported as a FAIL line, because a restart is what answers them — a refusal here would leave
// the item unable to start the next attempt at all.
func voidArtefacts(events []Event, it *Item) []namedArtefact {
	var order []string
	byPath := map[string]namedArtefact{}
	attempt := 1
	for _, ev := range events {
		if !strings.EqualFold(ev.ID, it.ID) {
			continue
		}
		if ev.T == "restart" {
			attempt++
			continue
		}
		if attempt >= it.Attempt {
			continue
		}
		kind, rel, sha := "", "", ""
		switch ev.T {
		case "open", "brief":
			kind, rel, sha = "brief", ev.Brief, ev.BriefSHA
		case "checks", "rebaseline":
			kind, rel, sha = "checks", ev.Checks, ev.ChecksSHA
		case "claims":
			kind, rel, sha = "claims", ev.Claims, ev.ClaimsSHA
		default:
			continue
		}
		if rel == "" || sha == "" {
			continue
		}
		if _, seen := byPath[rel]; !seen {
			order = append(order, rel)
		}
		byPath[rel] = namedArtefact{kind: kind, path: rel, sha: sha}
	}
	out := make([]namedArtefact, 0, len(order))
	for _, rel := range order {
		out = append(out, byPath[rel])
	}
	return out
}

// excuses is the damage the item's failure records list: each path, and the hash its bytes are
// allowed to have from now on — or `missing`, when the record says the file was already gone. The
// records are read as they are on disk; a rewritten record cannot excuse its own edit, because
// `bl check` verifies a record with no excusal at all.
type excuses map[string]map[string]bool

// readExcuses accumulates the excusals of every failure record the item's restarts name.
func readExcuses(root string, it *Item) excuses {
	out := excuses{}
	for _, r := range it.Restarts {
		if r.Record == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Record)))
		if err != nil {
			continue
		}
		inArtefacts := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "artefacts:" {
				inArtefacts = true
				continue
			}
			if !inArtefacts || !strings.HasPrefix(line, "- ") {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 6 || f[2] != "recorded" || f[4] != "now" {
				continue
			}
			if out[f[1]] == nil {
				out[f[1]] = map[string]bool{}
			}
			out[f[1]][f[5]] = true
		}
	}
	return out
}

// covers reports whether a record excuses the bytes a file holds now.
func (e excuses) covers(path, now string) bool { return e[path][now] }

// checkArtefact re-hashes one recorded artefact and reports the line `bl check` prints when it no
// longer matches, unless a failure record excuses the bytes it holds now. It never refuses: the
// damage a restart records is a FAIL line, and the restart's own record is the answer to it.
func checkArtefact(root string, ex excuses, id, kind, rel, want string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if ex.covers(rel, "missing") {
			return "", false
		}
		return id + " " + kind + " " + rel + " is missing", true
	}
	if got := shaHex(data); got != want {
		if ex.covers(rel, got) {
			return "", false
		}
		return id + " " + kind + " " + rel + " does not match its hash", true
	}
	return "", false
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

// rankable resolves an id a bump, unbump or rank may name: a known item that is neither closed nor
// declined, named once in the call.
func rankable(byID map[string]*Item, id string, seen map[string]bool) (*Item, error) {
	it, err := lookup(byID, id)
	if err != nil {
		return nil, err
	}
	if it.Status == "closed" || it.Status == "declined" {
		return nil, refuse("%s is %s — only an open or parked item can be ordered", it.ID, it.Status)
	}
	key := strings.ToLower(it.ID)
	if seen[key] {
		return nil, refuse("%s is named twice", it.ID)
	}
	seen[key] = true
	return it, nil
}

// cmdBump records the human's priority list (tier 1), and cmdUnbump takes ids out of it. Both
// write one event that names the ids in the order given.
func cmdBump(root string, a args) error { return priorityEvent(root, a, "bump") }

func cmdUnbump(root string, a args) error { return priorityEvent(root, a, "unbump") }

func priorityEvent(root string, a args, kind string) error {
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
	if len(a.positional) == 0 {
		return refuse("bl %s needs at least one id", kind)
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range a.positional {
		it, err := rankable(byID, id, seen)
		if err != nil {
			return err
		}
		ids = append(ids, it.ID)
	}
	if _, err := appendEvent(root, Event{T: kind, IDs: ids, By: "human"}); err != nil {
		return err
	}
	fmt.Printf("bl: %s %s\n", kind, strings.Join(ids, " "))
	return nil
}

// cmdRank records the loop's ranking (tier 2): one `id<TAB>reason` line per item, in rank order.
// It may not name a tier-1 id — the human's order outranks the loop's.
func cmdRank(root string, a args) error {
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
	if a.get("file") == "" {
		return refuse("--file is required")
	}
	text, err := a.input("file")
	if err != nil {
		return err
	}
	bumped := map[string]bool{}
	for _, key := range tierOne(events) {
		bumped[key] = true
	}
	seen := map[string]bool{}
	var ranking []Ranked
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, reason, _ := strings.Cut(line, "\t")
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return refuse("line %d: %s has an empty reason", n+1, strings.TrimSpace(id))
		}
		it, err := rankable(byID, strings.TrimSpace(id), seen)
		if err != nil {
			return err
		}
		if bumped[strings.ToLower(it.ID)] {
			return refuse("%s is in the human's priority list (tier 1) — the loop may not rank it", it.ID)
		}
		ranking = append(ranking, Ranked{ID: it.ID, Reason: reason})
	}
	if len(ranking) == 0 {
		return refuse("the ranking is empty")
	}
	if _, err := appendEvent(root, Event{T: "rank", Ranking: ranking, By: "loop"}); err != nil {
		return err
	}
	fmt.Printf("bl: ranked %d items\n", len(ranking))
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
	order, byID, err := derive(events)
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
	rows := eligible(workOrder(events, order, byID))
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
	// R36: an item that was restarted says which attempt it is in; an item that never was prints
	// no attempt line at all.
	if it.Attempt > 1 {
		fmt.Printf("attempt: %d\n", it.Attempt)
	}
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
		// R36: a round of an attempt a restart ended is still in the history, and says so.
		void := ""
		if it.void(r) {
			void = " void"
		}
		fmt.Printf("  r%d %s%s %s%s\n", i+1, r.Verdict, cause, rows, void)
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
		if len(it.attemptRounds()) == 0 {
			continue
		}
		fmt.Printf("%s %s\n", it.ID, roundCounts(it))
	}
	return nil
}

// roundCounts is the item's round budget as spent in its current attempt: the rounds in total and
// how many of each cause (R19, R36). A PASS carries no cause, so it is counted on its own — the
// total is every round of every cause, and a PASS is a round. A re-baseline resets none of these
// counts, and a restart starts the next attempt's at zero while the void rounds stay in the
// history, where `bl show` marks them.
func roundCounts(it *Item) string {
	return fmt.Sprintf("rounds: %d (code %d, harness %d, dispute %d, pass %d)",
		len(it.attemptRounds()), it.causeRounds("code"), it.causeRounds("harness"),
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
		// the latest event that recorded it. This is the CURRENT attempt's brief, list and
		// claims: a restart clears them, so an earlier attempt's are the FAIL lines below.
		if err := verifyFrozen(root, it); err != nil {
			return err
		}
		// R24: a frozen row whose brief_ref names a clause the current brief no longer holds is
		// a refusal — an amendment removed it, so the row's criterion no longer exists.
		if err := verifyBriefRefs(root, it); err != nil {
			return err
		}
		// R36: the artefacts of an attempt a restart ended are re-hashed against the latest event
		// that named each path, and a mismatch is a FAIL line rather than a refusal — the restart's
		// failure record is what excuses the damage, so refusing here would leave the item unable
		// to start the attempt that answers it.
		excuse := readExcuses(root, it)
		for _, a := range voidArtefacts(events, it) {
			if line, bad := checkArtefact(root, excuse, it.ID, a.kind, a.path, a.sha); bad {
				problems = append(problems, line)
			}
		}
		for _, r := range it.Restarts {
			// A record is checked with no excusal: the B4 scan never lists the record itself, so a
			// record that names its own path is not damage it found — it is an attempt to excuse
			// its own edit, and B7's example is that editing the record makes `bl check` name it.
			if line, bad := checkArtefact(root, nil, it.ID, "failure record", r.Record, r.RecordSHA); bad {
				problems = append(problems, line)
			}
		}
		// R36: a closed item's last round is read from its current attempt, so a void PASS round
		// of an attempt a restart ended cannot make it closed-with-a-PASS.
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
			if line, bad := checkArtefact(root, excuse, it.ID, "round", r.Rows, r.RowsSHA); bad {
				problems = append(problems, line)
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
			if line, bad := checkArtefact(root, excuse, it.ID, "run", rel, r.RunSHA); bad {
				problems = append(problems, line)
			}
		}
		// B8: the frozen test file is reported like a run file — a problem, not a refusal, so
		// `bl check` keeps checking the other items.
		if it.TestsSHA != "" {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(it.Tests)))
			if err != nil {
				problems = append(problems, it.ID+" tests "+it.Tests+" is missing")
			} else if shaHex(data) != it.TestsSHA {
				problems = append(problems, it.ID+" tests "+it.Tests+" does not match its hash")
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
			"file":          {"required": {"id", "text"}, "optional": {"after", "source", "urgent", "by"}},
			"open":          {"required": {"id"}, "optional": {"brief", "brief_sha", "scope", "scope_ignored", "before_tree", "ignored_before"}},
			"brief":         {"required": {"id", "brief", "brief_sha"}, "optional": {}},
			"checks":        {"required": {"id", "checks", "checks_sha"}, "optional": {"prev_checks_sha", "format", "tests", "tests_sha"}},
			"rebaseline":    {"required": {"id", "checks", "checks_sha"}, "optional": {"prev_checks_sha", "reason", "format", "tests", "tests_sha"}},
			"claims":        {"required": {"id", "claims", "claims_sha"}, "optional": {}},
			"run":           {"required": {"id", "run", "run_sha"}, "optional": {}},
			"round":         {"required": {"id", "verdict"}, "optional": {"cause", "rows", "rows_sha", "run", "tree", "finding"}},
			"restart":       {"required": {"id", "reason", "why", "change", "attempt", "record", "record_sha"}, "optional": {}},
			"second-check":  {"required": {"id", "row", "run", "computed"}, "optional": {"cmd", "expect", "ms"}},
			"misclassified": {"required": {"id", "row", "computed"}, "optional": {"run", "cmd", "expect"}},
			"close":         {"required": {"id", "commit"}, "optional": {}},
			"drop":          {"required": {"id", "reason"}, "optional": {"by"}},
			"defer":         {"required": {"id", "reason"}, "optional": {}},
			"reopen":        {"required": {"id"}, "optional": {"reason"}},
			"note":          {"required": {"id", "text"}, "optional": {}},
			"bump":          {"required": {"ids", "by"}, "optional": {}},
			"unbump":        {"required": {"ids", "by"}, "optional": {}},
			"rank":          {"required": {"ranking", "by"}, "optional": {}},
		},
	}
}

func cmdSchema(root string, a args) error {
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(schemaJSON())
	}
	order := []string{"file", "open", "brief", "checks", "rebaseline", "claims", "run", "round", "second-check", "misclassified", "close", "drop", "defer", "reopen", "restart", "note", "bump", "unbump", "rank"}
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

// auditDir is where `bl audit` writes its report, relative to the working tree.
const auditDir = backlogDir + "/audit"

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
	// R25: the report is an artefact of the ledger, so it lives inside the artefact directory and
	// never in the verified tree.
	outDir := filepath.Join(root, defaultStr(a.get("out"), auditDir))
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
	lists      map[string][]string
	positional []string
}

func (a args) get(name string) string { return a.flags[name] }
func (a args) flag(name string) bool  { return a.boolFlags[name] }

// list returns every value a repeatable flag was given, in the order they were given.
func (a args) list(name string) []string { return a.lists[name] }

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

var boolFlagNames = map[string]bool{"json": true, "views": true, "due": true, "amend": true, "urgent": true}

// repeatableFlagNames are the flags that may be given more than once: the item's scope is a
// set of paths, and the scope-ignored list is a set of files.
var repeatableFlagNames = map[string]bool{"scope": true, "scope-ignored": true}

func parseArgs(raw []string) (args, error) {
	a := args{flags: map[string]string{}, boolFlags: map[string]bool{}, lists: map[string][]string{}}
	for i := 0; i < len(raw); i++ {
		tok := raw[i]
		if strings.HasPrefix(tok, "--") {
			name := strings.TrimPrefix(tok, "--")
			if eq := strings.Index(name, "="); eq >= 0 {
				a.flags[name[:eq]] = name[eq+1:]
				a.lists[name[:eq]] = append(a.lists[name[:eq]], name[eq+1:])
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
			a.lists[name] = append(a.lists[name], raw[i+1])
			i++
			continue
		}
		a.positional = append(a.positional, tok)
	}
	return a, nil
}

var handlers = map[string]func(string, args) error{
	"init":         cmdInit,
	"file":         cmdFile,
	"open":         cmdOpen,
	"brief":        cmdBrief,
	"checks":       cmdChecks,
	"rebaseline":   cmdRebaseline,
	"claims":       cmdClaims,
	"run":          cmdRun,
	"round":        cmdRound,
	"second-check": cmdSecondCheck,
	"close":        cmdClose,
	"drop":         cmdDrop,
	"defer":        cmdDefer,
	"reopen":       cmdReopen,
	"restart":      cmdRestart,
	"note":         cmdNote,
	"bump":         cmdBump,
	"unbump":       cmdUnbump,
	"rank":         cmdRank,
	"next":         cmdNext,
	"show":         cmdShow,
	"status":       cmdStatus,
	"render":       cmdRender,
	"check":        cmdCheck,
	"schema":       cmdSchema,
	"audit":        cmdAudit,
	"import":       cmdImport,
	"lint":         cmdLint,
}

const usage = `usage: bl [--root DIR] [--json] VERB [args]

verbs:
  init | file | open | brief | checks | rebaseline | claims | run | round | second-check | close
  drop | defer | reopen | restart | note | bump | unbump | rank | next | show | status | check
  render | schema | audit | import | lint

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
