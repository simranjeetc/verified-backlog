package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

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
			"round":         {"required": {"id", "verdict"}, "optional": {"cause", "rows", "rows_sha", "run", "tree", "finding", "report_sha", "dispositions"}},
			"restart":       {"required": {"id", "reason", "why", "change", "attempt", "record", "record_sha"}, "optional": {}},
			"second-check":  {"required": {"id", "row", "run", "computed"}, "optional": {"cmd", "expect", "ms"}},
			"misclassified": {"required": {"id", "row", "computed"}, "optional": {"run", "cmd", "expect"}},
			"close":         {"required": {"id", "commit"}, "optional": {}},
			"drop":          {"required": {"id", "reason"}, "optional": {"by"}},
			"defer":         {"required": {"id", "reason"}, "optional": {}},
			"reopen":        {"required": {"id"}, "optional": {"reason"}},
			"note":          {"required": {"id", "text"}, "optional": {}},
			"signoff":       {"required": {"id", "by", "note"}, "optional": {}},
			"bump":          {"required": {"ids", "by"}, "optional": {}},
			"unbump":        {"required": {"ids", "by"}, "optional": {}},
			"rank":          {"required": {"ranking", "by"}, "optional": {}},
			"packet":        {"required": {"id", "ledger_len", "ledger_sha", "runlog_len", "runlog_sha", "backlog_sha"}, "optional": {}},
		},
	}
}

func cmdSchema(root string, a args) error {
	if a.pos(0) == "intake" {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(intakeSchema())
	}
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(schemaJSON())
	}
	order := []string{"file", "open", "brief", "checks", "rebaseline", "claims", "run", "round", "second-check", "misclassified", "close", "drop", "defer", "reopen", "restart", "note", "signoff", "bump", "unbump", "rank", "packet"}
	spec := schemaJSON()["events"].(map[string]map[string][]string)
	// The count is part of the output: a reader (and a grep) can tell 21 kinds from 20 without counting the lines.
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
