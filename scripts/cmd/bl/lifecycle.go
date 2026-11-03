package main

import (
	"fmt"
)

func cmdDrop(root string, a args) error {
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
	if it.Status != "open" && it.Status != "parked" {
		return refuse("%s is %s, not open", it.ID, it.Status)
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	by := a.get("by")
	if by == "" {
		by = "human"
	}
	if _, err := appendEvent(root, Event{T: "drop", ID: it.ID, Reason: reason, By: by}); err != nil {
		return err
	}
	fmt.Printf("bl: declined %s\n", it.ID)
	return nil
}

func cmdDefer(root string, a args) error {
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
	it, err := liveItem(byID, a.pos(0))
	if err != nil {
		return err
	}
	reason, err := cleanText(a.get("reason"), "reason")
	if err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "defer", ID: it.ID, Reason: reason}); err != nil {
		return err
	}
	fmt.Printf("bl: parked %s\n", it.ID)
	return nil
}

func cmdReopen(root string, a args) error {
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
	if it.Status != "declined" && it.Status != "parked" {
		return refuse("%s is %s — only a declined or parked item can be reopened", it.ID, it.Status)
	}
	ev := Event{T: "reopen", ID: it.ID}
	if r := a.get("reason"); r != "" {
		reason, err := cleanText(r, "reason")
		if err != nil {
			return err
		}
		ev.Reason = reason
	}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	fmt.Printf("bl: %s back in the queue\n", it.ID)
	return nil
}
