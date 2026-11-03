package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// cmdBaseline reconciles committed outside-scope entries, never the producer's own diff.
func cmdBaseline(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
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
	if err = requireInFlight(order, it, ""); err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	if len(it.Scope) == 0 || it.BeforeTree == "" {
		return refuse("baseline recovery requires an explicit scope and recorded before-tree")
	}
	if it.passRounds() != 0 {
		return refuse("baseline recovery is unavailable after PASS; preserve its evidence")
	}
	if openVerifierPacket(events, it) != 0 && verifyPacketWindow(root, events, it) == nil {
		return refuseRule("R39", "record the round or void the verifier window before baseline recovery")
	}
	if err = verifyFrozen(root, it); err != nil {
		return err
	}
	if err = verifyTests(root, it); err != nil {
		return err
	}
	head, code := git(root, "rev-parse", "--verify", "HEAD^{commit}")
	if code != 0 {
		return refuse("baseline recovery requires a committed HEAD")
	}
	head = strings.TrimSpace(head)
	current, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	before, err := baselineEntries(root, it.BeforeTree)
	if err != nil {
		return err
	}
	now, err := baselineEntries(root, current)
	if err != nil {
		return err
	}
	committed, err := baselineEntries(root, head)
	if err != nil {
		return err
	}
	paths := map[string]bool{}
	for p := range before {
		paths[p] = true
	}
	for p := range now {
		paths[p] = true
	}
	var changed []string
	for p := range paths {
		if before[p] == now[p] {
			continue
		}
		if filepath.Base(p) == ".gitignore" || filepath.Base(p) == ".gitattributes" {
			return refuse("baseline recovery cannot reconcile ignore or attribute policy: %s", p)
		}
		if inScope(it.Scope, p) {
			continue
		}
		if now[p] != committed[p] {
			return refuse("baseline recovery refuses uncommitted outside-scope change: %s", p)
		}
		changed = append(changed, p)
	}
	if len(changed) == 0 {
		return refuse("no committed outside-scope changes to reconcile")
	}
	sort.Strings(changed)
	// A private index preserves modes, symlinks, additions and deletions without touching
	// the real index, working tree, or the original before-tree object.
	tmp, err := os.CreateTemp("", "bl-baseline-index-")
	if err != nil {
		return err
	}
	index := tmp.Name()
	tmp.Close()
	os.Remove(index)
	defer os.Remove(index)
	runIndex := func(input string, argv ...string) (string, error) {
		cmd := exec.Command("git", argv...)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GIT_INDEX_FILE="+index)
		cmd.Stdin = strings.NewReader(input)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return "", refuse("baseline git %s: %s", argv[0], out)
		}
		return strings.TrimSpace(string(out)), nil
	}
	if _, err = runIndex("", "read-tree", it.BeforeTree); err != nil {
		return err
	}
	var updates strings.Builder
	for _, p := range changed {
		entry := now[p]
		if entry == "" {
			entry = "0 " + strings.Repeat("0", len(strings.Fields(before[p])[1]))
		}
		fmt.Fprintf(&updates, "%s\t%s%c", entry, p, 0)
	}
	if _, err = runIndex(updates.String(), "update-index", "-z", "--index-info"); err != nil {
		return err
	}
	next, err := runIndex("", "write-tree")
	if err != nil {
		return err
	}
	merged, err := baselineEntries(root, next)
	if err != nil {
		return err
	}
	// Git can remove an ancestor/descendant implicitly during a file/directory
	// replacement. Check the complete result rather than trusting update paths.
	for p, entry := range before {
		if inScope(it.Scope, p) && merged[p] != entry {
			return refuse("baseline recovery would erase in-scope baseline: %s", p)
		}
	}
	for p, entry := range merged {
		if inScope(it.Scope, p) && before[p] != entry {
			return refuse("baseline recovery would change in-scope baseline: %s", p)
		}
	}
	// Catch concurrent writes before recording the transition.
	final, err := verifiedTree(root)
	if err != nil {
		return err
	}
	finalHead, code := git(root, "rev-parse", "--verify", "HEAD^{commit}")
	if final != current || code != 0 || strings.TrimSpace(finalHead) != head {
		return refuse("tree or HEAD moved during baseline recovery; retry")
	}
	latest, err := readLedger(root)
	if err != nil {
		return err
	}
	if len(latest) != len(events) {
		return refuse("ledger moved during baseline recovery; retry")
	}
	_, err = appendEvent(root, Event{T: "baseline", ID: it.ID, BeforeTree: next, PrevBeforeTree: it.BeforeTree, BaselinePaths: changed, Commit: head, Reason: reason})
	if err == nil {
		fmt.Printf("bl: %s reconciled %d committed outside-scope paths; attempt unchanged; obtain a fresh run and verifier packet\n", it.ID, len(changed))
	}
	return err
}

// Values are "mode object-id", using NUL-delimited paths throughout.
func baselineEntries(root, tree string) (map[string]string, error) {
	out, code := git(root, "ls-tree", "-r", "-z", tree)
	if code != 0 {
		return nil, refuse("cannot read baseline tree %s", tree)
	}
	result := map[string]string{}
	for _, rec := range strings.Split(out, "\x00") {
		if rec == "" {
			continue
		}
		meta, path, ok := strings.Cut(rec, "\t")
		fields := strings.Fields(meta)
		if !ok || len(fields) != 3 {
			return nil, refuse("invalid tree entry")
		}
		result[path] = fields[0] + " " + fields[2]
	}
	return result, nil
}
