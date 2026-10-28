package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

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
