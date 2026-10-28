package main

import (
	"fmt"
	"strings"
)

// rankable resolves an id a bump, unbump or rank may name: a known item that is neither closed nor
// declined, named once in the call.
func rankable(byID map[string]*Item, id string, seen map[string]bool) (*Item, error) {
	it, err := lookup(byID, id)
	if err != nil {
		return nil, err
	}
	if it.Status == "closed" || it.Status == "declined" {
		return nil, refuse("%s is %s — only an open or parked item can be ordered", it.ID, it.Status)
	}
	key := strings.ToLower(it.ID)
	if seen[key] {
		return nil, refuse("%s is named twice", it.ID)
	}
	seen[key] = true
	return it, nil
}

// cmdBump records the human's priority list (tier 1), and cmdUnbump takes ids out of it. Both
// write one event that names the ids in the order given.
func cmdBump(root string, a args) error { return priorityEvent(root, a, "bump") }

func cmdUnbump(root string, a args) error { return priorityEvent(root, a, "unbump") }

func priorityEvent(root string, a args, kind string) error {
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
	if len(a.positional) == 0 {
		return refuse("bl %s needs at least one id", kind)
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range a.positional {
		it, err := rankable(byID, id, seen)
		if err != nil {
			return err
		}
		ids = append(ids, it.ID)
	}
	if _, err := appendEvent(root, Event{T: kind, IDs: ids, By: "human"}); err != nil {
		return err
	}
	fmt.Printf("bl: %s %s\n", kind, strings.Join(ids, " "))
	return nil
}

// cmdRank records the loop's ranking (tier 2): one `id<TAB>reason` line per item, in rank order.
// It may not name a tier-1 id — the human's order outranks the loop's.
func cmdRank(root string, a args) error {
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
	if a.get("file") == "" {
		return refuse("--file is required")
	}
	text, err := a.input("file")
	if err != nil {
		return err
	}
	bumped := map[string]bool{}
	for _, key := range tierOne(events) {
		bumped[key] = true
	}
	seen := map[string]bool{}
	var ranking []Ranked
	for n, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			continue
		}
		id, reason, _ := strings.Cut(line, "\t")
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return refuse("line %d: %s has an empty reason", n+1, strings.TrimSpace(id))
		}
		it, err := rankable(byID, strings.TrimSpace(id), seen)
		if err != nil {
			return err
		}
		if bumped[strings.ToLower(it.ID)] {
			return refuse("%s is in the human's priority list (tier 1) — the loop may not rank it", it.ID)
		}
		ranking = append(ranking, Ranked{ID: it.ID, Reason: reason})
	}
	if len(ranking) == 0 {
		return refuse("the ranking is empty")
	}
	if _, err := appendEvent(root, Event{T: "rank", Ranking: ranking, By: "loop"}); err != nil {
		return err
	}
	fmt.Printf("bl: ranked %d items\n", len(ranking))
	return nil
}

func cmdNext(root string, a args) error {
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
	cur, err := pinned(order)
	if err != nil {
		return err
	}
	if cur != nil {
		fmt.Printf("%s\t%s\n", cur.ID, cur.Text)
		return nil
	}
	rows := eligible(workOrder(events, order, byID))
	if len(rows) == 0 {
		fmt.Println("none")
		return nil
	}
	fmt.Printf("%s\t%s\n", rows[0].ID, rows[0].Text)
	return nil
}
