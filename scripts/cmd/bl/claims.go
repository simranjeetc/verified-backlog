package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func cmdClaims(root string, a args) error {
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
	if it.Checks == "" {
		return refuseRule("R5", "%s has no check list — the list is frozen before the claims; run `bl checks %s` first", it.ID, it.ID)
	}
	if it.Claims != "" {
		return refuseRule("R5", "%s already has a claims event — the claims are frozen once", it.ID)
	}
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	if err := verifyTests(root, it); err != nil {
		return err
	}
	path := a.get("file")
	if path == "" {
		return refuse("--file is required")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return refuse("--file %s: %v", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return refuse("--file %s is empty", path)
	}
	rel := attemptFile(it.ID, it.Attempt, claimsName)
	if err := atomicWrite(filepath.Join(root, rel), string(data)); err != nil {
		return err
	}
	if _, err := appendEvent(root, Event{T: "claims", ID: it.ID, Claims: rel, ClaimsSHA: shaHex(data)}); err != nil {
		return err
	}
	fmt.Printf("bl: %s claims recorded\n", it.ID)
	return nil
}
