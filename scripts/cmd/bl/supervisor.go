package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loopSupervisorPacket only derives ledger state. Role packets retain their own
// blindness and verifier-window behavior; generating this packet records no events.
func loopSupervisorPacket(root, id, config, out string, continuation bool) (string, error) {
	events, err := readLedger(root)
	if err != nil {
		return "", err
	}
	_, byID, err := derive(events)
	if err != nil {
		return "", err
	}
	it, err := lookup(byID, id)
	if err != nil {
		return "", err
	}
	root, err = filepath.Abs(root)
	if err != nil {
		return "", err
	}
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	stage, next := supervisorNext(it)
	var b strings.Builder
	fmt.Fprintf(&b, "# Supervisor startup: %s\nWrite your final report to %s; a report printed only to stdout is lost.\n", it.ID, out)
	fmt.Fprintf(&b, "Repository and supervisor cwd: %s\nProducer workspace: unknown (not recorded in ledger); inspect git worktree list and live jobs before assigning or recovering a workspace.\n", root)
	fmt.Fprintf(&b, "Item: %s\nIntent: %s\nStatus: %s; attempt: %d; stage: %s\n%s\n", it.ID, it.Text, it.Status, it.Attempt, stage, roundCounts(it))
	if continuation {
		fmt.Fprintln(&b, "Continuation after context limit: reconcile ledger, git status and existing jobs; do not repeat recorded steps or launch duplicate workers.")
	}
	fmt.Fprintln(&b, "\nLocal CLI (works from any directory):")
	fmt.Fprintln(&b, "Prefix EVERY Bash/tool shell call that uses bl with the following function definition; shell functions do not persist across calls. Keep the definition and commands in the same call.")
	fmt.Fprintf(&b, "bl() { %s --root %s \"$@\"; }\n", supervisorQuote(binary), supervisorQuote(root))
	fmt.Fprintf(&b, "bl show %s --json\nbl status %s\ngit -C %s status --short\ngit -C %s worktree list\n", it.ID, it.ID, supervisorQuote(root), supervisorQuote(root))
	fmt.Fprintf(&b, "Next action: %s\n", next)
	fmt.Fprintln(&b, "\nArtifacts (repository-relative; pending paths are destinations, not evidence):")
	for _, a := range []struct{ name, path, dest string }{
		{"brief", it.Brief, attemptFile(it.ID, it.Attempt, "brief.md")},
		{"checks", it.Checks, attemptFile(it.ID, it.Attempt, "checks.tsv")},
		{"claims", it.Claims, attemptFile(it.ID, it.Attempt, "claims.md")},
		{"tests", it.Tests, attemptFile(it.ID, it.Attempt, "tests.py")},
	} {
		if a.path != "" {
			fmt.Fprintf(&b, "%s: %s (recorded)\n", a.name, a.path)
		} else {
			fmt.Fprintf(&b, "%s: %s (pending)\n", a.name, a.dest)
		}
	}
	fmt.Fprintln(&b, "evidence: .backlog/evidence/; ledger: .backlog/ledger.jsonl")
	runs := it.attemptRuns()
	if len(runs) > 0 {
		r := runs[len(runs)-1]
		fmt.Fprintf(&b, "latest current-attempt run: .backlog/evidence/%s-%s.json\n", it.ID, r.Run)
	}
	for _, r := range it.Restarts {
		fmt.Fprintf(&b, "prior failure record: %s\n", r.Record)
	}
	fmt.Fprintf(&b, "\nRouting authority: brain launch config %s. Worker roles resolve through dispatch configuration and scripts/dispatch.sh (CHAIN when set); inspect those actual settings, never infer model assignments from handoff prose.\n", config)
	fmt.Fprintln(&b, "Supervisor coordinates, resolves routine architecture choices and records evidence; do not write producer code. Producer implements only scoped work. Independent checks writer gets intent, never producer claims or implementation; producer never receives frozen checks. Start producer and checks writer together. Fresh verifier gets the verifier packet only after checks, claims and run are recorded; never reuse the producer as verifier.")
	fmt.Fprintf(&b, "Role packet commands: bl packet %s --role producer; bl packet %s --role checks; bl packet %s --role verifier (only when ready; records verifier window).\n", it.ID, it.ID, it.ID)
	fmt.Fprintln(&b, "Use this packet for startup. Consult only the relevant section of playbooks/item-loop/index.md and the producer.md, verifier-checks.md or verifier-claims.md template when preparing that role. Do not read whole directories or prior sessions without a specific missing fact. Inspect existing worker jobs before resuming. Keep prompts, outputs and workspace ownership explicit. Use bl run for frozen checks and suites; do not bypass gates or run suites separately as supervisor. Record verdict rows unchanged; supervisor sets observed cause. Commit explicit paths locally; never push. Exit after this item is closed or parked; park questions only the human can answer.")
	fmt.Fprintln(&b, "Recovery: checks --amend never removes/reorders rows; after claims only added rows are allowed, and no amendment follows PASS. A command/setup defect in a frozen check calls for bad-check assessment, not a producer code fix. Restart only for bad-check, extra-file, damaged-evidence or wrong-premise, with --why and --change; preserve failure records, then reopen the item for its new attempt. Two restarts maximum; code FAIL cap is three per attempt. Stop after repeated failures on the same premise and assess it directly. Refresh only the current item with bl show "+it.ID+" --json on resume, external changes or a CLI refusal; never dump the ledger or full history. These instructions do not waive CLI refusals.")
	return b.String(), nil
}

func supervisorQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'" }

func supervisorNext(it *Item) (string, string) {
	id := it.ID
	switch {
	case it.Status != "open":
		return "terminal", "Report the recorded disposition; do not reopen automatically."
	case it.BlockedBy != "":
		return "blocked", "Reconcile the dependency; do not open or dispatch blocked work."
	case !it.Opened:
		if it.Attempt > 1 {
			return "unopened", "Review inherited scope and before-state, then bl open " + id + " without scope flags to preserve the inherited lists. Explicit scope flags replace both lists; supply the complete intended scope and ignored-scope lists only when deliberately changing them. Review and record the new attempt brief before dispatch."
		}
		return "unopened", "Pin scope and before-state, then bl open " + id + " --scope <path> (repeat --scope for each allowed path); review and record an observable brief before dispatch."
	case it.Brief == "":
		return "brief", "Record the attempt brief with bl brief " + id + " --file <brief.md> before dispatch."
	case it.Checks == "":
		return "checks", "Reconcile worker jobs; dispatch producer and independent checks writer together if absent, then bl checks " + id + " --file <checks.tsv> --tests <tests.py> for a format-2 list (omit --tests only for a format-1 list). Record claims only after checks."
	case it.Claims == "":
		return "claims", "Collect producer output, then bl claims " + id + " --file <claims.md>; keep the frozen checks away from producer."
	case it.passRounds() > 0:
		return "passed", "Re-run the surprising verifier row per playbook, commit explicit paths, then bl close " + id + " --commit HEAD; closure gates still apply."
	default:
		rounds := it.attemptRounds()
		runs := it.attemptRuns()
		if len(rounds) > 0 && (len(runs) == 0 || rounds[len(rounds)-1].Seq > runs[len(runs)-1].Seq) {
			return "failed", "Read current-attempt defect rows; dispatch only the scoped fixes, then bl run " + id + " and a fresh verifier. Assess restart classes before attempting to repair an invalid check."
		}
		if len(runs) == 0 {
			return "run", "Execute bl run " + id + "; only after it succeeds issue the verifier packet and dispatch a fresh verifier."
		}
		return "verification", "Inspect the latest recorded run and existing verifier jobs; obtain a fresh verifier packet when ready, then record its unchanged rows with bl round " + id + " --verdict <PASS|FAIL> --rows <rows.tsv> --report <report.md> (the report is required for a B0 brief; FAIL also needs observed --cause). A stale run may require bl run again."
	}
}
