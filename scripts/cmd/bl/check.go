package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func cmdCheck(root string, a args) error {
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
	var problems []string
	for _, it := range order {
		// R20: re-hash the frozen artefacts and refuse when one no longer matches the hash of
		// the latest event that recorded it. This is the CURRENT attempt's brief, list and
		// claims: a restart clears them, so an earlier attempt's are the FAIL lines below.
		if err := verifyFrozen(root, it); err != nil {
			return err
		}
		// R24: a frozen row whose brief_ref names a clause the current brief no longer holds is
		// a refusal — an amendment removed it, so the row's criterion no longer exists.
		if err := verifyBriefRefs(root, it); err != nil {
			return err
		}
		// R36: the artefacts of an attempt a restart ended are re-hashed against the latest event
		// that named each path, and a mismatch is a FAIL line rather than a refusal — the restart's
		// failure record is what excuses the damage, so refusing here would leave the item unable
		// to start the attempt that answers it.
		excuse := readExcuses(root, it)
		for _, a := range voidArtefacts(events, it) {
			if line, bad := checkArtefact(root, excuse, it.ID, a.kind, a.path, a.sha); bad {
				problems = append(problems, line)
			}
		}
		for _, r := range it.Restarts {
			// A record is checked with no excusal: the B4 scan never lists the record itself, so a
			// record that names its own path is not damage it found — it is an attempt to excuse
			// its own edit, and B7's example is that editing the record makes `bl check` name it.
			if line, bad := checkArtefact(root, nil, it.ID, "failure record", r.Record, r.RecordSHA); bad {
				problems = append(problems, line)
			}
		}
		// R36: a closed item's last round is read from its current attempt, so a void PASS round
		// of an attempt a restart ended cannot make it closed-with-a-PASS.
		if it.Status == "closed" {
			if last := it.last(); last == nil || last.Verdict != "PASS" {
				problems = append(problems, it.ID+" is closed without a PASS round")
			}
		}
		for _, r := range it.Rounds {
			if r.Rows == "" {
				// a harness round carries no rows, so there is no evidence file to verify
				continue
			}
			if line, bad := checkArtefact(root, excuse, it.ID, "round", r.Rows, r.RowsSHA); bad {
				problems = append(problems, line)
			}
		}
		// R20: the run file is a frozen artefact too — it is hashed into the ledger event that
		// names it, and `bl check` re-hashes it.
		for _, r := range it.Runs {
			rel, ok := runFileRel(it.ID, r.Run)
			if !ok {
				problems = append(problems, it.ID+" run "+r.Run+" names no run file")
				continue
			}
			if line, bad := checkArtefact(root, excuse, it.ID, "run", rel, r.RunSHA); bad {
				problems = append(problems, line)
			}
		}
		// B8: the frozen test file is reported like a run file — a problem, not a refusal, so
		// `bl check` keeps checking the other items.
		if it.TestsSHA != "" {
			data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(it.Tests)))
			if err != nil {
				problems = append(problems, it.ID+" tests "+it.Tests+" is missing")
			} else if shaHex(data) != it.TestsSHA {
				problems = append(problems, it.ID+" tests "+it.Tests+" does not match its hash")
			}
		}
		if it.BlockedBy != "" {
			if _, ok := byID[strings.ToLower(it.BlockedBy)]; !ok {
				problems = append(problems, it.ID+" waits on unknown item "+it.BlockedBy)
			}
		}
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if a.flag("views") {
		p := newPaths(root)
		want := map[string]string{
			p.queue:  renderQueue(root, order),
			p.record: renderRecords(root, order),
		}
		names := make([]string, 0, len(want))
		for path := range want {
			names = append(names, path)
		}
		sort.Strings(names)
		for _, path := range names {
			data, err := os.ReadFile(path)
			if err != nil {
				problems = append(problems, path+" is missing")
				continue
			}
			if string(data) != want[path] {
				problems = append(problems, path+" is stale or hand-edited")
			}
		}
	}
	if len(problems) > 0 {
		for _, p := range problems {
			fmt.Printf("FAIL %s\n", p)
		}
		return errFailed
	}
	inFlight := "none"
	if cur != nil {
		inFlight = cur.ID
	}
	checked := "not checked"
	if a.flag("views") {
		checked = "checked"
	}
	fmt.Printf("PASS %d events · %d items · in flight %s · views %s\n", len(events), len(order), inFlight, checked)
	return nil
}

func mustRead(path string) []byte {
	data, _ := os.ReadFile(path)
	return data
}

// errFailed signals a check that found problems without refusing the call.
var errFailed = errors.New("check found problems")

// runFileRel is the path of a run file: .backlog/evidence/<id>-run<N>.json, derived from the run
// id `run<N>`. The run event does not carry the path — it is derivable, and one derivation is
// one thing to get right.
func runFileRel(id, run string) (string, bool) {
	if !strings.HasPrefix(run, "run") || strings.TrimPrefix(run, "run") == "" {
		return "", false
	}
	return backlogDir + "/" + evidenceDir + "/" + id + "-" + run + ".json", true
}
