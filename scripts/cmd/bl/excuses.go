package main

import (
	"os"
	"path/filepath"
	"strings"
)

// ── the restarts `bl check` excuses (R36) ────────────────────────────────────

// namedArtefact is one recorded artefact of an attempt a restart ended.
type namedArtefact struct{ kind, path, sha string }

// voidArtefacts is the frozen artefacts of the item's earlier attempts: each path with the hash
// the latest event of a VOID attempt that named it recorded, in the order the paths were first
// recorded. The current attempt's artefacts are verified by verifyFrozen; an earlier attempt's are
// reported as a FAIL line, because a restart is what answers them — a refusal here would leave
// the item unable to start the next attempt at all.
func voidArtefacts(events []Event, it *Item) []namedArtefact {
	var order []string
	byPath := map[string]namedArtefact{}
	attempt := 1
	for _, ev := range events {
		if !strings.EqualFold(ev.ID, it.ID) {
			continue
		}
		if ev.T == "restart" {
			attempt++
			continue
		}
		if attempt >= it.Attempt {
			continue
		}
		kind, rel, sha := "", "", ""
		switch ev.T {
		case "open", "brief":
			kind, rel, sha = "brief", ev.Brief, ev.BriefSHA
		case "checks", "rebaseline":
			kind, rel, sha = "checks", ev.Checks, ev.ChecksSHA
		case "claims":
			kind, rel, sha = "claims", ev.Claims, ev.ClaimsSHA
		default:
			continue
		}
		if rel == "" || sha == "" {
			continue
		}
		if _, seen := byPath[rel]; !seen {
			order = append(order, rel)
		}
		byPath[rel] = namedArtefact{kind: kind, path: rel, sha: sha}
	}
	out := make([]namedArtefact, 0, len(order))
	for _, rel := range order {
		out = append(out, byPath[rel])
	}
	return out
}

// excuses is the damage the item's failure records list: each path, and the hash its bytes are
// allowed to have from now on — or `missing`, when the record says the file was already gone. The
// records are read as they are on disk; a rewritten record cannot excuse its own edit, because
// `bl check` verifies a record with no excusal at all.
type excuses map[string]map[string]bool

// readExcuses accumulates the excusals of every failure record the item's restarts name.
func readExcuses(root string, it *Item) excuses {
	out := excuses{}
	for _, r := range it.Restarts {
		if r.Record == "" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(r.Record)))
		if err != nil {
			continue
		}
		inArtefacts := false
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "artefacts:" {
				inArtefacts = true
				continue
			}
			if !inArtefacts || !strings.HasPrefix(line, "- ") {
				continue
			}
			f := strings.Fields(line)
			if len(f) < 6 || f[2] != "recorded" || f[4] != "now" {
				continue
			}
			if out[f[1]] == nil {
				out[f[1]] = map[string]bool{}
			}
			out[f[1]][f[5]] = true
		}
	}
	return out
}

// covers reports whether a record excuses the bytes a file holds now.
func (e excuses) covers(path, now string) bool { return e[path][now] }

// checkArtefact re-hashes one recorded artefact and reports the line `bl check` prints when it no
// longer matches, unless a failure record excuses the bytes it holds now. It never refuses: the
// damage a restart records is a FAIL line, and the restart's own record is the answer to it.
func checkArtefact(root string, ex excuses, id, kind, rel, want string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		if ex.covers(rel, "missing") {
			return "", false
		}
		return id + " " + kind + " " + rel + " is missing", true
	}
	if got := shaHex(data); got != want {
		if ex.covers(rel, got) {
			return "", false
		}
		return id + " " + kind + " " + rel + " does not match its hash", true
	}
	return "", false
}
