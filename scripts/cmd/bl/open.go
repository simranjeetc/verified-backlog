package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

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
	source := ""
	if a.get("source") != "" {
		clean, err := cleanText(a.get("source"), "source")
		if err != nil {
			return err
		}
		source = clean
	}
	agentSource := isAgentSource(source)
	bucket := strings.TrimSpace(a.get("bucket"))
	if bucket != "" && bucket != "fix-now" && bucket != "batch" && bucket != "later" {
		return refuseRule("R41", "--bucket must be fix-now, batch or later")
	}
	proof := ""
	if strings.TrimSpace(a.get("proof")) != "" {
		clean, err := cleanText(a.get("proof"), "proof")
		if err != nil {
			return err
		}
		proof = clean
	}
	if agentSource && proof == "" {
		return refuseRule("R41", "agent findings require --proof with the command run and what it showed")
	}
	if agentSource && bucket == "" {
		return refuseRule("R41", "agent findings require --bucket: fix-now, batch or later")
	}
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
	var openPackets []string
	for _, item := range byID {
		if seq := openVerifierPacket(events, item); seq != 0 {
			openPackets = append(openPackets, fmt.Sprintf("%s packet seq %d", item.ID, seq))
		}
	}
	sort.Strings(openPackets)
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
	ev := Event{T: "file", ID: id, Text: text, Bucket: bucket, Proof: proof, Source: source}
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
	// --urgent is a bump of the new item, recorded on its own file event: one call, one append.
	if a.flag("urgent") {
		ev.Urgent, ev.By = true, "human"
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	if len(openPackets) > 0 {
		fmt.Fprintf(os.Stderr, "bl: warning: file event does not void the verifier window for %s\n", strings.Join(openPackets, ", "))
	}
	if ev.After != "" {
		fmt.Printf("bl: filed %s (after %s)\n", id, ev.After)
	} else {
		fmt.Printf("bl: filed %s\n", id)
	}
	return nil
}

func isAgentSource(source string) bool {
	s := strings.ToLower(strings.TrimSpace(source))
	for _, prefix := range []string{"verifier:", "producer:", "supervisor:", "loop"} {
		if strings.HasPrefix(s, prefix) {
			return true
		}
	}
	return false
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
	resume := it.Claims != ""
	if resume {
		// R5: claims freeze the attempt's brief and scope. Re-hash every frozen artefact (R20),
		// refuse a dirty working tree, and snapshot the current tree for the resumed attempt.
		if err := verifyFrozen(root, it); err != nil {
			return err
		}
		if err := verifyTests(root, it); err != nil {
			return err
		}
		_, scopeGiven := a.lists["scope"]
		_, ignoredGiven := a.lists["scope-ignored"]
		if scopeGiven && !sameStrings(a.list("scope"), it.Scope) || ignoredGiven && !sameStrings(a.list("scope-ignored"), it.ScopeIgnored) {
			return refuseRule("R5", "%s can resume only with its recorded scope", it.ID)
		}
		if err := refuseDirtyTree(root, it.ID); err != nil {
			return err
		}
		before, err := verifiedTreeWriting(root)
		if err != nil {
			return err
		}
		ignored, err := ignoredUnderScope(root, it.Scope)
		if err != nil {
			return err
		}
		ev := Event{T: "open", ID: it.ID, Brief: it.Brief, BriefSHA: it.BriefSHA,
			Scope: it.Scope, ScopeIgnored: it.ScopeIgnored, BeforeTree: before, IgnoredBefore: ignored}
		if _, err := appendEvent(root, ev); err != nil {
			return err
		}
		fmt.Printf("bl: %s resumed with its frozen brief, checks and claims\n", it.ID)
		fmt.Printf("bl: %s in flight\n", it.ID)
		return nil
	}
	scope := a.list("scope")
	if err := validateOpenScope(root, scope); err != nil {
		return err
	}
	// R5: every open records a before-tree. Refuse external changes before writing the fresh
	// attempt brief or its open event, so the snapshot cannot absorb uncommitted producer work.
	if err := refuseDirtyTree(root, it.ID); err != nil {
		return err
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

func validateOpenScope(root string, scope []string) error {
	for _, entry := range scope {
		if strings.TrimSpace(entry) == "" {
			return refuse("--scope entry %q is empty; --scope is repeated once per path", entry)
		}
		if strings.Contains(entry, ",") {
			return refuse("--scope entry %q contains a comma; --scope is repeated once per path", entry)
		}
		path := filepath.Join(root, entry)
		if _, err := os.Stat(path); err == nil {
			continue
		} else if !os.IsNotExist(err) {
			return refuse("--scope entry %q cannot be checked; --scope is repeated once per path", entry)
		}
		parent := filepath.Dir(path)
		info, err := os.Stat(parent)
		if err != nil || !info.IsDir() {
			return refuse("--scope entry %q has no existing parent directory; --scope is repeated once per path", entry)
		}
	}
	return nil
}

func refuseDirtyTree(root string, id string) error {
	if !isGit(root) {
		return nil
	}
	status, code := git(root, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if code != 0 {
		return refuseRule("R5", "could not check whether %s can open on a clean tree", id)
	}
	var dirty []string
	entries := strings.Split(status, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		if len(entry) < 4 {
			continue
		}
		path := entry[3:]
		if !isArtifact(path) {
			dirty = append(dirty, path)
		}
		// With -z, rename/copy records put the destination first and the source
		// in the next NUL field. A rename also takes the file away from its source,
		// so that path is judged too; a copy leaves the source untouched.
		if (entry[0] == 'R' || entry[0] == 'C' || entry[1] == 'R' || entry[1] == 'C') && i+1 < len(entries) {
			i++
			if (entry[0] == 'R' || entry[1] == 'R') && !isArtifact(entries[i]) {
				dirty = append(dirty, entries[i])
			}
		}
	}
	if len(dirty) == 0 {
		return nil
	}
	return refuseRule("R5", "%s can open only on a clean tree — commit or stash first: %s", id, strings.Join(dirty, "; "))
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
