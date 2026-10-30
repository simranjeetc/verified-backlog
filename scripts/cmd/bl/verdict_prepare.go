package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A draft keeps the machine-owned binding separate from the verifier's decisions.
// Never silently rebind an old draft to the latest run or packet.
type verdictBinding struct {
	Version   int    `json:"version"`
	Item      string `json:"item"`
	Attempt   int    `json:"attempt"`
	Packet    int    `json:"packet_seq"`
	Run       string `json:"run"`
	Tree      string `json:"tree"`
	ChecksSHA string `json:"checks_sha"`
	RunSHA    string `json:"run_sha"`
}
type preparedJudgment struct {
	ID      string `json:"id"`
	Verdict string `json:"verdict"`
	Anchor  string `json:"anchor"`
}
type preparedNote struct {
	Text        string `json:"text"`
	Disposition string `json:"disposition"`
	Item        string `json:"item,omitempty"`
	Reason      string `json:"reason,omitempty"`
}
type verdictDraft struct {
	Binding   verdictBinding     `json:"binding"`
	Summary   string             `json:"summary"`
	Judgments []preparedJudgment `json:"judgments"`
	Finding   string             `json:"finding"`
	Notes     []preparedNote     `json:"notes"`
}

func cmdVerdicts(root string, a args) error {
	if len(a.positional) != 1 {
		return refuse("usage: bl verdicts ID --init --output draft.json | --file draft.json --output rows.tsv --report report.md")
	}
	for k := range a.flags {
		if k != "file" && k != "output" && k != "report" {
			return refuse("unknown verdicts option --%s", k)
		}
		if len(a.list(k)) != 1 {
			return refuse("verdicts option --%s must occur once", k)
		}
	}
	for k := range a.boolFlags {
		if k != "init" {
			return refuse("unknown verdicts option --%s", k)
		}
	}
	if a.flag("init") && (a.get("file") != "" || a.get("report") != "") {
		return refuse("--init accepts --output only")
	}
	if !a.flag("init") && (a.get("file") == "" || a.get("report") == "") {
		return refuse("conversion requires --file, --output and --report")
	}
	output, err := verdictOutputPath(root, a.get("output"))
	if err != nil {
		return err
	}
	reportPath := ""
	if !a.flag("init") {
		reportPath, err = verdictOutputPath(root, a.get("report"))
		if err != nil {
			return err
		}
		input, e := filepath.Abs(a.get("file"))
		if e != nil {
			return e
		}
		input, e = filepath.EvalSymlinks(input)
		if e != nil {
			return e
		}
		if output == reportPath || output == input || reportPath == input {
			return refuse("draft, rows and report must be distinct files")
		}
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	order, items, err := derive(events)
	if err != nil {
		return err
	}
	it, err := liveItem(items, a.pos(0))
	if err != nil {
		return err
	}
	if err := requireInFlight(order, it, ""); err != nil {
		return err
	}
	if it.Claims == "" {
		return refuseRule("R6", "verdict preparation requires recorded claims")
	}
	if openVerifierPacket(events, it) == 0 {
		return refuseRule("R39", "verdict preparation requires a current verifier packet")
	}
	if err := verifyPacketWindow(root, events, it); err != nil {
		return err
	}
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	if err := verifyTests(root, it); err != nil {
		return err
	}
	frozen, err := frozenRows(root, it)
	if err != nil {
		return err
	}
	run, ok := theRun(it)
	if !ok {
		return refuseRule("R6", "verdict preparation requires a recorded run")
	}
	for _, round := range it.attemptRounds() {
		if round.Run == run.Run {
			return refuseRule("R6", "run %s was already consumed", run.Run)
		}
	}
	rel, ok := runFileRel(it.ID, run.Run)
	if !ok {
		return refuseRule("R6", "invalid run file")
	}
	raw, err := verifyArtefact(root, rel, run.RunSHA, "run")
	if err != nil {
		return err
	}
	var rf runFile
	if err := json.Unmarshal(raw, &rf); err != nil {
		return err
	}
	tree, err := verifiedTree(root)
	if err != nil {
		return err
	}
	if !treeHashRe.MatchString(rf.TreeStart) || !treesMatch(root, tree, rf.TreeStart) {
		return refuseRule("R6", "source tree changed since run %s; obtain a fresh run and verifier", run.Run)
	}
	if err := checkR28(root, it); err != nil {
		return err
	}
	binding := verdictBinding{1, it.ID, it.Attempt, openVerifierPacket(events, it), run.Run, rf.TreeStart, it.ChecksSHA, run.RunSHA}
	if a.flag("init") {
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			return refuse("--init requires a new draft path; do not overwrite or rebind earlier judgments")
		}
		draft := verdictDraft{Binding: binding, Judgments: []preparedJudgment{}, Notes: []preparedNote{}}
		for _, r := range frozen {
			if r.f["decider"] == "judge" {
				draft.Judgments = append(draft.Judgments, preparedJudgment{ID: r.f["id"]})
			}
		}
		data, err := json.MarshalIndent(draft, "", "  ")
		if err != nil {
			return err
		}
		return writeRolePrompt(output, string(data)+"\n")
	}
	data, err := os.ReadFile(a.get("file"))
	if err != nil {
		return err
	}
	var draft verdictDraft
	if err := decodeVerdictDraft(data, &draft); err != nil {
		return refuse("invalid verdict draft: %v", err)
	}
	if draft.Binding != binding {
		return refuseRule("R6", "draft binding is stale or changed; initialize a new draft from a fresh verifier packet and reverify")
	}
	computed, err := computedVerdicts(frozen, &rf)
	if err != nil {
		return err
	}
	rows, report, err := prepareVerdictEvidence(draft, frozen, computed, items)
	if err != nil {
		return err
	}
	// Preflight anchors against the actual run/source as well as their syntax.
	vf, err := parseVerdicts(rows, frozen)
	if err != nil {
		return err
	}
	hunks, _ := roundDiff(root, it.BeforeTree, tree)
	if err := checkAnchors(root, it, vf, frozen, &rf, hunks); err != nil {
		return err
	}
	// Validate all content before any output. Round still independently applies R6-R39.
	if err := writeRolePrompt(reportPath, report); err != nil {
		return err
	}
	if err := writeRolePrompt(output, rows); err != nil {
		return err
	}
	fmt.Printf("bl: prepared %s and %s; no round recorded\n", output, reportPath)
	return nil
}

// Resolve parents, not just lexical prefixes, so a symlink cannot write into evidence.
func verdictOutputPath(root, path string) (string, error) {
	if path == "" {
		return "", refuse("an output path outside the repository is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(abs))
	if err != nil {
		return "", err
	}
	dest := filepath.Join(parent, filepath.Base(abs))
	repo, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	repo, err = filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(repo, dest)
	if err != nil {
		return "", err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", refuse("verdict outputs must be outside the repository to preserve the verified tree and R39")
	}
	if info, err := os.Lstat(dest); err == nil {
		if !info.Mode().IsRegular() {
			return "", refuse("verdict output must be a regular file")
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	return dest, nil
}

func prepareVerdictEvidence(d verdictDraft, frozen []checkRow, computed map[string]string, items map[string]*Item) (string, string, error) {
	if strings.TrimSpace(d.Summary) == "" {
		return "", "", refuse("verifier summary is required")
	}
	judgments := map[string]preparedJudgment{}
	for _, j := range d.Judgments {
		if _, exists := judgments[j.ID]; exists {
			return "", "", refuse("duplicate judgment %s", j.ID)
		}
		judgments[j.ID] = j
	}
	var rows, report strings.Builder
	fmt.Fprintf(&rows, "%s\nrun %s\ntree %s\n", strings.Join(verdictColumns, "\t"), d.Binding.Run, d.Binding.Tree)
	for _, r := range frozen {
		id := r.f["id"]
		v, anchor := computed[id], ""
		if r.f["decider"] == "judge" {
			j, exists := judgments[id]
			if !exists {
				return "", "", refuse("missing judgment %s", id)
			}
			delete(judgments, id)
			v, anchor = j.Verdict, j.Anchor
			if v != "PASS" && v != "FAIL" {
				return "", "", refuse("judgment %s requires PASS or FAIL", id)
			}
			if strings.ContainsAny(anchor, "\t\r\n") {
				return "", "", refuse("judgment %s anchor must fit one TSV field", id)
			}
			if _, err := parseAnchor(id, anchor); err != nil {
				return "", "", err
			}
		}
		fmt.Fprintf(&rows, "%s\t%s\t%s\n", id, v, anchor)
	}
	if len(judgments) != 0 {
		return "", "", refuse("judgments must name only frozen judge rows")
	}
	if strings.TrimSpace(d.Finding) != "" {
		if strings.ContainsAny(d.Finding, "\t\r\n") {
			return "", "", refuse("finding must be one line; put detailed probes in summary")
		}
		fmt.Fprintf(&rows, "# finding: %s\n", d.Finding)
	}
	report.WriteString(d.Summary + "\n")
	if d.Finding != "" {
		fmt.Fprintf(&report, "\nFinding: %s\n", d.Finding)
	}
	for i, note := range d.Notes {
		if strings.TrimSpace(note.Text) == "" {
			return "", "", refuse("note %d requires text", i+1)
		}
		disposition := note.Disposition
		switch disposition {
		case "fix":
			if note.Item != "" || note.Reason != "" {
				return "", "", refuse("fix note accepts no item or reason")
			}
		case "filed":
			target, ok := items[strings.ToLower(note.Item)]
			if !ok || target.Status == "closed" || target.Status == "declined" || note.Reason != "" {
				return "", "", refuse("filed note needs an existing open item and no reason")
			}
			disposition += " " + target.ID
		case "waived":
			if strings.TrimSpace(note.Reason) == "" || strings.ContainsAny(note.Reason, "\t\r\n") || note.Item != "" {
				return "", "", refuse("waived note needs a single-line reason and no item")
			}
			disposition += " " + note.Reason
		default:
			return "", "", refuse("note %d disposition must be fix, filed or waived", i+1)
		}
		fmt.Fprintf(&rows, "# report RO%d: %s\n", i+1, disposition)
		fmt.Fprintf(&report, "\n[RO%d] %s\n", i+1, note.Text)
	}
	fmt.Fprintf(&report, "\nReport-only: %d\n", len(d.Notes))
	if _, err := parseVerdicts(rows.String(), frozen); err != nil {
		return "", "", err
	}
	n, err := reportOnlyTags(report.String())
	if err != nil {
		return "", "", err
	}
	if n != len(d.Notes) {
		return "", "", refuse("report control syntax in free text")
	}
	// Reject even valid-looking reserved tags in free text: only this serializer owns them.
	texts := []string{d.Summary, d.Finding}
	for _, note := range d.Notes {
		texts = append(texts, note.Text)
	}
	for _, s := range texts {
		if malformedReportTagRe.MatchString(s) || strings.Contains(s, "Report-only:") {
			return "", "", refuse("reserved report control syntax in verifier text")
		}
	}
	return rows.String(), report.String(), nil
}

func decodeVerdictDraft(data []byte, draft *verdictDraft) error {
	// encoding/json otherwise silently accepts duplicate keys, including verdict/binding.
	tokens := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSONValue(tokens); err != nil {
		return err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return fmt.Errorf("expected one JSON value")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	return decoder.Decode(draft)
}
func uniqueJSONValue(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]bool{}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return err
			}
			key, ok := k.(string)
			key = strings.ToLower(key)
			if !ok || seen[key] {
				return fmt.Errorf("duplicate or invalid JSON key %v", k)
			}
			seen[key] = true
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSONValue(d); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}
