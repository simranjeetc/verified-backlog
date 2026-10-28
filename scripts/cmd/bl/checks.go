package main

import (
	"fmt"
	"os"
	"path/filepath"
)

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
