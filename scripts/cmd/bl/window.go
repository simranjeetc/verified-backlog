package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type backlogEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA    string `json:"sha,omitempty"`
	Target string `json:"target,omitempty"`
}

func packetSnapshot(root, id string) (Event, error) {
	p := newPaths(root)
	ledger, err := os.ReadFile(p.ledger)
	if err != nil {
		return Event{}, err
	}
	runlog, err := os.ReadFile(p.runlog)
	if err != nil && !os.IsNotExist(err) {
		return Event{}, err
	}
	if os.IsNotExist(err) {
		runlog = nil
	}
	backlogSHA, err := backlogDigest(root)
	if err != nil {
		return Event{}, err
	}
	return Event{
		T: "packet", ID: id,
		LedgerLen: intRef(len(ledger)), LedgerSHA: shaHex(ledger),
		RunlogLen: intRef(len(runlog)), RunlogSHA: shaHex(runlog), BacklogSHA: backlogSHA,
	}, nil
}

func backlogDigest(root string) (string, error) {
	base := filepath.Join(root, backlogDir)
	var entries []backlogEntry
	err := filepath.WalkDir(base, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == base {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == ledgerName || rel == runlogName {
			return nil
		}
		if rel == "loop.json" || rel == "loop.lock" {
			entries = append(entries, backlogEntry{Path: rel, Kind: "present"})
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			entries = append(entries, backlogEntry{Path: rel, Kind: "symlink", Target: target})
			return nil
		}
		if d.IsDir() {
			entries = append(entries, backlogEntry{Path: rel, Kind: "dir"})
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		entries = append(entries, backlogEntry{Path: rel, Kind: "file", SHA: shaHex(data)})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	data, err := json.Marshal(entries)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func verifyPacketWindow(root string, events []Event, it *Item) error {
	var baseline Event
	for _, ev := range events {
		if ev.T == "packet" && strings.EqualFold(ev.ID, it.ID) && ev.Seq > it.restartSeq {
			baseline = ev
		}
	}
	if baseline.Seq == 0 {
		return nil
	}
	p := newPaths(root)
	ledger, err := os.ReadFile(p.ledger)
	if err != nil {
		return err
	}
	if len(ledger) < derefInt(baseline.LedgerLen) || shaHex(ledger[:minInt(len(ledger), derefInt(baseline.LedgerLen))]) != baseline.LedgerSHA {
		return refuseRule("R39", "ledger prefix changed or is shorter than verifier packet seq %d", baseline.Seq)
	}
	for _, ev := range events {
		if ev.Seq > baseline.Seq && ev.T != "packet" && ev.T != "note" && ev.T != "second-check" {
			return refuseRule("R39", "ledger event seq %d type %s is outside the verifier window", ev.Seq, ev.T)
		}
	}
	runlog, err := os.ReadFile(p.runlog)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if os.IsNotExist(err) {
		runlog = nil
	}
	if len(runlog) < derefInt(baseline.RunlogLen) || shaHex(runlog[:minInt(len(runlog), derefInt(baseline.RunlogLen))]) != baseline.RunlogSHA {
		return refuseRule("R39", "run.log prefix changed or is shorter than verifier packet seq %d", baseline.Seq)
	}
	digest, err := backlogDigest(root)
	if err != nil {
		return err
	}
	if digest != baseline.BacklogSHA {
		return refuseRule("R39", ".backlog/ changed since verifier packet seq %d", baseline.Seq)
	}
	return nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func intRef(n int) *int { return &n }

func derefInt(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}
