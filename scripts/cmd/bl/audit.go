package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

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
	Verb string   `json:"verb"`
	Rule *string  `json:"rule"`
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

func refusalRuleCounts(records []runRecord) map[string]int {
	counts := map[string]int{}
	for _, record := range records {
		if record.Exit != 2 {
			continue
		}
		rule := "unrecorded"
		if record.Rule != nil {
			rule = *record.Rule
			if rule == "" {
				rule = "unnamed"
			}
		}
		counts[rule]++
	}
	return counts
}

func sortedRefusalRules(counts map[string]int) []string {
	rules := make([]string, 0, len(counts))
	for rule := range counts {
		rules = append(rules, rule)
	}
	sort.Slice(rules, func(i, j int) bool {
		if counts[rules[i]] == counts[rules[j]] {
			return rules[i] < rules[j]
		}
		return counts[rules[i]] > counts[rules[j]]
	})
	return rules
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
	var windowRecords []runRecord
	var slowest []int64
	for _, r := range readRunlog(root) {
		if inWindow(r.At) {
			runs = append(runs, r)
			windowRecords = append(windowRecords, r)
			if r.Exit == 2 {
				refusals++
			}
			slowest = append(slowest, r.MS)
		}
	}
	refusalsByRule := refusalRuleCounts(windowRecords)
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
		"refusals_by_rule":         refusalsByRule,
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
	md.WriteString("\n## refusals by rule\n\n")
	if len(refusalsByRule) == 0 {
		md.WriteString("- none\n")
	} else {
		for _, rule := range sortedRefusalRules(refusalsByRule) {
			fmt.Fprintf(&md, "- %s: %d\n", rule, refusalsByRule[rule])
		}
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
