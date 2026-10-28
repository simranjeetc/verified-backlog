package main

import (
	"os"
	"path/filepath"
	"strings"
)

// verifyArtefact re-reads a frozen artefact and refuses if it no longer matches the hash of the
// latest event that recorded it (R20). The message names the artefact and both hashes.
func verifyArtefact(root, rel, want, what string) ([]byte, error) {
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return nil, refuseRule("R20", "the %s %s is missing", what, rel)
	}
	if got := shaHex(data); got != want {
		return nil, refuseRule("R20", "the %s %s no longer matches the hash recorded in the ledger: recorded %s, on disk %s", what, rel, want, got)
	}
	return data, nil
}

// verifyFrozen re-hashes the item's frozen artefacts — the brief, the check list and the claims —
// and refuses when one of them no longer matches the hash of the latest event that recorded it
// (R20). It is the check every verb that reads a frozen artefact makes before it uses it.
func verifyFrozen(root string, it *Item) error {
	for _, a := range []struct{ what, rel, want string }{
		{"brief", it.Brief, it.BriefSHA},
		{"check list", it.Checks, it.ChecksSHA},
		{"claims", it.Claims, it.ClaimsSHA},
	} {
		if a.rel == "" {
			continue
		}
		if _, err := verifyArtefact(root, a.rel, a.want, a.what); err != nil {
			return err
		}
	}
	return nil
}

// verifyTests re-hashes the item's frozen test file, when the latest `checks` or `rebaseline`
// event carries `tests_sha` (I89, B8). It is the same comparison R31 requires, through the same
// helper, so the test file joins the brief, the check list and the claims as a frozen artefact.
// An item without a frozen test file has nothing to re-hash.
func verifyTests(root string, it *Item) error {
	if it.TestsSHA == "" {
		return nil
	}
	_, err := verifyArtefact(root, it.Tests, it.TestsSHA, "test file")
	return err
}

// requireInFlight refuses when the item is not the item in flight. The rule argument names the
// design rule the refusal is reported under: "R35" for `bl run` and `bl round`, the two verbs
// the design's rule R35 covers. The other paths that hold the lock — brief, checks, claims,
// rebaseline and second-check — pass no rule, so their refusals keep their unnamed wording.
func requireInFlight(order []*Item, it *Item, rule string) error {
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur == nil || !strings.EqualFold(cur.ID, it.ID) {
		if rule == "" {
			return refuse("%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
		}
		return refuseRule(rule, "%s is not the item in flight — run `bl open %s`", it.ID, it.ID)
	}
	return nil
}

// verifyBriefRefs checks that every frozen row still resolves against the brief. This is the USE
// path — `bl round`, `bl check` and `bl close` — so the refusal is R24's: an amendment removed
// the clause the frozen row cites, and the row's criterion no longer exists in the brief it was
// written against.
//
// The WRITE path is R2's: `bl checks` refuses a list written with a clause that does not exist
// (validateR2), and it keeps R2. The two are different faults — a list that was never valid, and
// a list that a later amendment invalidated — and they are named differently.
//
// A row with an empty brief_ref is fine — a judge row and an advisory row may both carry one.
// Only a brief_ref that is present and names a clause the brief no longer holds is a refusal.
// The message names the row and the clause.
func verifyBriefRefs(root string, it *Item) error {
	if it.Brief == "" || it.Checks == "" {
		return nil
	}
	brief, err := verifyArtefact(root, it.Brief, it.BriefSHA, "brief")
	if err != nil {
		return err
	}
	list, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	clauses := briefClauses(string(brief))
	rows, err := parseChecks(string(list))
	if err != nil {
		return refuseRule("R1", "the stored check list %s does not parse: %v", it.Checks, err)
	}
	for _, r := range rows {
		ref := r.f["brief_ref"]
		if ref == "" || clauses[ref] {
			continue
		}
		return refuseRule("R24", "row %s: brief_ref %q names no clause in the brief %s — a brief amendment removed it, so the row's criterion no longer exists in the brief it was written against",
			r.f["id"], ref, it.Brief)
	}
	return nil
}
