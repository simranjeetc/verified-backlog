package main

import (
	"fmt"
	"strings"
)

func cmdNote(root string, a args) error {
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
	text, err := cleanText(a.get("text"), "text")
	if err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "note", ID: it.ID, Text: text}); err != nil {
		return err
	}
	fmt.Printf("bl: noted on %s\n", it.ID)
	return nil
}

func cmdSignoff(root string, a args) error {
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
		return refuseRule("R22", "unknown item %s", a.pos(0))
	}
	if it.Status != "open" || !it.Opened || len(it.attemptRounds()) != 0 {
		return refuseRule("R22", "%s must be in flight before its first round", it.ID)
	}
	by := strings.TrimSpace(a.get("by"))
	note := strings.TrimSpace(a.get("note"))
	if by == "" || note == "" || strings.ContainsAny(by+note, "\r\n") {
		return refuseRule("R22", "--by and --note must be non-empty single lines")
	}
	if _, err := appendEvent(root, Event{T: "signoff", ID: it.ID, By: by, Note: note}); err != nil {
		return err
	}
	fmt.Printf("bl: signed off %s by %s\n", it.ID, by)
	return nil
}
