package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func cmdShow(root string, a args) error {
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
	it, err := lookup(byID, a.pos(0))
	if err != nil {
		return err
	}
	if a.flag("json") {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(it)
	}
	fmt.Printf("%s — %s\n", it.ID, it.Text)
	status := it.Status
	if it.BlockedBy != "" {
		status += " (blocked by " + it.BlockedBy + ")"
	}
	fmt.Printf("status: %s\n", status)
	// R36: an item that was restarted says which attempt it is in; an item that never was prints
	// no attempt line at all.
	if it.Attempt > 1 {
		fmt.Printf("attempt: %d\n", it.Attempt)
	}
	if it.After != "" {
		fmt.Printf("after: %s\n", it.After)
	}
	if it.Source != "" {
		fmt.Printf("source: %s\n", it.Source)
	}
	if it.Proof != "" {
		fmt.Printf("proof: %s\n", it.Proof)
	}
	if it.Bucket != "" {
		fmt.Printf("bucket: %s\n", it.Bucket)
	}
	if it.Commit != "" {
		fmt.Printf("commit: %s\n", it.Commit)
	}
	if it.Reason != "" {
		fmt.Printf("reason: %s\n", it.Reason)
	}
	fmt.Printf("%s\n", roundCounts(it))
	for i, r := range it.Rounds {
		cause := ""
		if r.Cause != "" {
			cause = " cause=" + r.Cause
		}
		rows := r.Rows
		if rows == "" {
			rows = "no rows"
		}
		// R36: a round of an attempt a restart ended is still in the history, and says so.
		void := ""
		if it.void(r) {
			void = " void"
		}
		fmt.Printf("  r%d %s%s %s%s\n", i+1, r.Verdict, cause, rows, void)
	}
	for _, n := range it.Notes {
		fmt.Printf("  note: %s\n", n)
	}
	return nil
}

func cmdStatus(root string, a args) error {
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
	counts := map[string]int{}
	for _, it := range order {
		key := it.Status
		if it.BlockedBy != "" {
			key = "blocked"
		}
		counts[key]++
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s %d", k, counts[k])
	}
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	inFlight := "none"
	if cur != nil {
		inFlight = cur.ID
	}
	if len(parts) == 0 {
		fmt.Printf("items %d\n", len(order))
	} else {
		fmt.Printf("items %d · %s\n", len(order), strings.Join(parts, " · "))
	}
	fmt.Printf("in flight: %s\n", inFlight)
	// R19: the round counts are part of the status — the total and how many of each cause,
	// so the harness and dispute rounds are visible.
	if id := a.pos(0); id != "" {
		it, err := lookup(byID, id)
		if err != nil {
			return err
		}
		fmt.Printf("%s %s\n", it.ID, roundCounts(it))
		return nil
	}
	for _, it := range order {
		if len(it.attemptRounds()) == 0 {
			continue
		}
		fmt.Printf("%s %s\n", it.ID, roundCounts(it))
	}
	return nil
}

// roundCounts is the item's round budget as spent in its current attempt: the rounds in total and
// how many of each cause (R19, R36). A PASS carries no cause, so it is counted on its own — the
// total is every round of every cause, and a PASS is a round. A re-baseline resets none of these
// counts, and a restart starts the next attempt's at zero while the void rounds stay in the
// history, where `bl show` marks them.
func roundCounts(it *Item) string {
	return fmt.Sprintf("rounds: %d (code %d, harness %d, dispute %d, pass %d)",
		len(it.attemptRounds()), it.causeRounds("code"), it.causeRounds("harness"),
		it.causeRounds("dispute"), it.passRounds())
}

func cmdRender(root string, a args) error {
	if err := requireLedger(root); err != nil {
		return err
	}
	events, err := readLedger(root)
	if err != nil {
		return err
	}
	if err := render(root, events); err != nil {
		return err
	}
	fmt.Printf("bl: rendered %s and %s\n", queueView, recordView)
	return nil
}
