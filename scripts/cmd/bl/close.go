package main

import (
	"fmt"
	"strings"
)

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
	if last := it.last(); last != nil && last.Rows != "" {
		if _, err := verifyArtefact(root, last.Rows, last.RowsSHA, "evidence file"); err != nil {
			return err
		}
	}
	// R24: the rows the last round was written against must still resolve against the brief, or
	// the round closed on a criterion the brief no longer holds.
	if err := verifyBriefRefs(root, it); err != nil {
		return err
	}
	if last := it.last(); last == nil || last.Verdict != "PASS" {
		return refuse("%s has no PASS round — nothing to close", it.ID)
	}
	if !hasSignoff(events, it.ID) {
		rowsText, verifyErr := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
		if verifyErr != nil {
			return verifyErr
		}
		rows, parseErr := parseChecks(string(rowsText))
		if parseErr != nil {
			return parseErr
		}
		qualifies := false
		for _, row := range rows {
			if row.f["severity"] == "blocking" && row.f["decider"] == "bl" {
				qualifies = true
				break
			}
		}
		if !qualifies {
			return refuseRule("R15", "%s last round has no blocking decider: bl row and no sign-off", it.ID)
		}
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
	last := it.last()
	currentTree, err := verifiedTree(root)
	if err != nil {
		return err
	}
	if len(last.Tree) != 40 || len(currentTree) != 40 || currentTree != last.Tree {
		return refuseRule("R18", "%s verified tree %s does not match the PASS tree %s", commit, currentTree, last.Tree)
	}
	changed, code := git(root, "diff", "--name-only", "--no-renames", commit, "--", ".", ":!.backlog", ":!TODO.md", ":!PROGRESS.md")
	if code != 0 {
		return refuseRule("R18", "could not compare %s with the working directory", commit)
	}
	if strings.TrimSpace(changed) != "" {
		return refuseRule("R18", "%s differs from the working directory outside artefacts: %s", commit, strings.TrimSpace(changed))
	}
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
	if _, err := appendEvent(root, Event{T: "close", ID: it.ID, Commit: commit, Tree: last.Tree}); err != nil {
		return err
	}
	short := commit
	if len(short) > 7 {
		short = short[:7]
	}
	fmt.Printf("bl: closed %s at %s\n", it.ID, short)
	return nil
}

func hasSignoff(events []Event, id string) bool {
	for _, ev := range events {
		if ev.T == "signoff" && strings.EqualFold(ev.ID, id) {
			return true
		}
	}
	return false
}
