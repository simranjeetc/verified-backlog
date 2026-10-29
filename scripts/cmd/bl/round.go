package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

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
	if err := verifyPacketWindow(root, events, it); err != nil {
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
		// R38: a harness round reads no report, so a report given to one would go unread.
		if a.get("report") != "" {
			return refuseRule("R38", "a harness round reads no report — drop --report or record a code round")
		}
		if verdict != "FAIL" {
			return refuseRule("R23", "a harness round is a failure — it needs --verdict FAIL")
		}
		return recordHarnessRound(root, it, a)
	}
	var reportSHA string
	var dispositions []string
	if it.Brief != "" {
		brief, e := verifyArtefact(root, it.Brief, it.BriefSHA, "brief")
		if e != nil {
			return e
		}
		if briefClauses(string(brief))["B0"] && a.get("report") == "" {
			return refuseRule("R38", "a brief with B0 requires --report <path>")
		}
	}
	if a.get("report") != "" {
		raw, e := os.ReadFile(a.get("report"))
		if e != nil {
			return refuseRule("R38", "cannot read --report: %v", e)
		}
		reportSHA = shaHex(raw)
		notes, e := reportOnlyTags(string(raw))
		if e != nil {
			return e
		}
		dispositions, e = parseReportDispositions(a.get("rows"), notes, byID)
		if e != nil {
			return e
		}
		if hasFixDisposition(dispositions) && verdict == "PASS" {
			return refuseRule("R38", "a fix disposition requires FAIL; explicit --verdict PASS is refused")
		}
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
	if hasFixDisposition(dispositions) {
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
		// R38: a fix disposition is the orchestrator agreeing the code is wrong.
		if cause == "" && hasFixDisposition(dispositions) {
			cause = "code"
		}
		if cause == "" {
			return refuseRule("R23", "a FAIL needs --cause %s", strings.Join(causes, "|"))
		}
		if cause == "dispute" {
			return refuseRule("R23", "--cause dispute is accepted only when the round carries a finding")
		}
		if cause == "harness" {
			return refuseRule("R23", "--cause harness is accepted only when the verdicts file is absent or carries no rows")
		}
		if !blockingFailed && !hasFixDisposition(dispositions) {
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
		Rows:      backlogDir + "/" + evidenceDir + "/" + name,
		RowsSHA:   shaHex([]byte(body)),
		ReportSHA: reportSHA, Dispositions: dispositions,
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

func hasFixDisposition(ds []string) bool {
	for _, d := range ds {
		// a disposition reads "<n> <kind> [...]"; only the kind decides
		if f := strings.Fields(d); len(f) >= 2 && f[1] == "fix" {
			return true
		}
	}
	return false
}

// reportCountRe is the sole report declaration accepted by R38.
var (
	reportCountRe        = regexp.MustCompile(`^Report-only: (0|[1-9][0-9]*)$`)
	reportTagRe          = regexp.MustCompile(`\[RO([1-9][0-9]*)\]`)
	malformedReportTagRe = regexp.MustCompile(`(?i)\[\s*ro[\s-]*[0-9]`)
	reportLineRe         = regexp.MustCompile(`(?i)^#\s*report\b`)
	dispositionRe        = regexp.MustCompile(`^# report RO([1-9][0-9]*): (fix|filed (\S+)|waived (\S.*))$`)
)

// reportOnlyTags reads the fixed plain-line marker and note tags; it deliberately knows no Markdown.
func reportOnlyTags(report string) (int, error) {
	count := -1
	tagged := map[int]bool{}
	for _, line := range strings.Split(report, "\n") {
		line = strings.TrimRight(line, "\r 	")
		if reportCountRe.MatchString(line) {
			if count >= 0 {
				return 0, refuseRule("R38", "report has more than one Report-only: N line")
			}
			m := reportCountRe.FindStringSubmatch(line)
			count, _ = strconv.Atoi(m[1])
		}
		stripped := reportTagRe.ReplaceAllString(line, "")
		if malformedReportTagRe.MatchString(stripped) {
			return 0, refuseRule("R38", "malformed report-only tag in line %q", line)
		}
		for _, m := range reportTagRe.FindAllStringSubmatch(line, -1) {
			n, _ := strconv.Atoi(m[1])
			tagged[n] = true
		}
	}
	if count < 0 {
		return 0, refuseRule("R38", "report needs exactly one `Report-only: N` line")
	}
	for n := 1; n <= count; n++ {
		if !tagged[n] {
			return 0, refuseRule("R38", "report is missing tag RO%d", n)
		}
	}
	for n := range tagged {
		if n > count {
			return 0, refuseRule("R38", "report has extra tag RO%d above Report-only: %d", n, count)
		}
	}
	return count, nil
}

func parseReportDispositions(path string, notes int, byID map[string]*Item) ([]string, error) {
	if path == "" {
		if notes > 0 {
			return nil, refuseRule("R38", "report note RO1 has no disposition: --rows is required")
		}
		return nil, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, refuseRule("R38", "cannot read verdicts file for report dispositions: %v", err)
	}
	seen := map[int]string{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r 	")
		if !reportLineRe.MatchString(line) {
			continue
		}
		m := dispositionRe.FindStringSubmatch(line)
		if m == nil {
			return nil, refuseRule("R38", "disposition line %q is malformed", line)
		}
		n, _ := strconv.Atoi(m[1])
		if _, ok := seen[n]; ok {
			return nil, refuseRule("R38", "report note RO%d has duplicate dispositions", n)
		}
		if m[3] != "" {
			target, ok := byID[strings.ToLower(m[3])]
			if !ok {
				return nil, refuseRule("R38", "report note RO%d: filed item %s does not exist", n, m[3])
			}
			if target.Status == "closed" || target.Status == "declined" {
				return nil, refuseRule("R38", "report note RO%d: filed item %s is closed or dropped", n, m[3])
			}
		}
		seen[n] = m[2]
	}
	for n := range seen {
		if n > notes {
			return nil, refuseRule("R38", "disposition for extra report note RO%d", n)
		}
	}
	out := make([]string, notes)
	for n := 1; n <= notes; n++ {
		v, ok := seen[n]
		if !ok {
			return nil, refuseRule("R38", "report note RO%d has no disposition", n)
		}
		out[n-1] = "RO" + strconv.Itoa(n) + " " + v
	}
	return out, nil
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
