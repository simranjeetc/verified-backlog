package main

import (
	"os"
	"strings"
)

// ── validators ───────────────────────────────────────────────────────────────

func requireLedger(root string) error {
	if _, err := os.Stat(newPaths(root).ledger); err != nil {
		return refuse("no ledger at %s — run `bl init` first", newPaths(root).ledger)
	}
	return nil
}

func cleanText(value, field string) (string, error) {
	v := strings.TrimSpace(value)
	if v == "" {
		return "", refuse("%s is empty", field)
	}
	if strings.ContainsAny(v, "\r\n") {
		return "", refuse("%s must be one line — put detail in --rows or bl note", field)
	}
	return v, nil
}

func checkID(value string) (string, error) {
	v, err := cleanText(value, "id")
	if err != nil {
		return "", err
	}
	if !idPattern.MatchString(v) {
		return "", refuse("id %q does not match %s", v, idPattern.String())
	}
	return v, nil
}

func lookup(byID map[string]*Item, id string) (*Item, error) {
	it, ok := byID[strings.ToLower(id)]
	if !ok {
		return nil, refuse("unknown item %s", id)
	}
	return it, nil
}

func liveItem(byID map[string]*Item, id string) (*Item, error) {
	it, err := lookup(byID, id)
	if err != nil {
		return nil, err
	}
	if it.Status != "open" {
		return nil, refuse("%s is %s, not open", it.ID, it.Status)
	}
	return it, nil
}
