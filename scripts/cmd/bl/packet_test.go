package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPacketIncludesOnlyCitedRulesAndScope(t *testing.T) {
	dir := t.TempDir()
	for _, argv := range [][]string{{"init"}, {"file", "--id", "A1", "--text", "cite R20 and R10"}, {"open", "A1", "--scope", "scripts/cmd/bl"}} {
		if code := run(append([]string{"--root", dir}, argv...)); code != 0 {
			t.Fatalf("bl %v: exit %d", argv, code)
		}
	}
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := "# Rules\n**R1 — schema.** first rule\ncontinued\n\n**R10 — hunk coverage.** tenth rule\n\n**R20 — final.** last rule\n"
	if err := os.WriteFile(filepath.Join(dir, "references", "design-check-list-schema.md"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := call(t, cmdNote, dir, "A1", "--text", "Keep R20 and R10; R100 is distinct"); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { _ = call(t, cmdPacket, dir, "A1", "--role", "checks") })
	for _, want := range []string{"# A1 packet (role checks)", "Keep R20 and R10; R100 is distinct", "**R10 — hunk coverage.** tenth rule", "**R20 — final.** last rule", "R1 — schema", "- scripts/cmd/bl"} {
		if !strings.Contains(out, want) {
			t.Errorf("packet missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "**R1 — schema.**") {
		t.Fatal("uncited R1 paragraph was included")
	}
}

func TestPacketVerifierRequiresAndPrintsClaims(t *testing.T) {
	dir := openItem(t)
	args := args{flags: map[string]string{"role": "verifier"}, positional: []string{"A1"}}
	if out := capture(t, func() {
		if err := cmdPacket(dir, args); err == nil {
			t.Error("verifier packet without claims succeeded")
		}
	}); out != "" {
		t.Fatalf("refusal printed %q", out)
	}
	if err := call(t, cmdChecks, dir, "A1", "--file", writeFile(t, t.TempDir(), "checks.tsv", checksHeader+"R1\tbl\tblocking\tB1\techo ok\texit 0\t\t\tworks\n")); err != nil {
		t.Fatal(err)
	}
	claims := writeFile(t, t.TempDir(), "claims.md", "CLAIM-MARKER-42\n")
	if err := call(t, cmdClaims, dir, "A1", "--file", claims); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { _ = call(t, cmdPacket, dir, "A1", "--role", "verifier") })
	if !strings.Contains(out, "CLAIM-MARKER-42") {
		t.Fatalf("verifier packet missing claims:\n%s", out)
	}
	for _, role := range []string{"checks", "producer"} {
		out = capture(t, func() { _ = call(t, cmdPacket, dir, "A1", "--role", role) })
		if strings.Contains(out, "CLAIM-MARKER-42") {
			t.Fatalf("%s packet exposed claims", role)
		}
	}
}

func TestPacketMissingRulesDocumentStillPrintsPacket(t *testing.T) {
	dir := openItem(t)
	out := capture(t, func() { _ = call(t, cmdPacket, dir, "A1", "--role", "checks") })
	if !strings.Contains(out, "(rules doc not found: references/design-check-list-schema.md)") {
		t.Fatalf("missing rules notice not printed:\n%s", out)
	}
}

func TestPacketSectionsInOrderAndIndexesTheRulesItLeavesOut(t *testing.T) {
	dir := openItem(t)
	if err := os.MkdirAll(filepath.Join(dir, "references"), 0o755); err != nil {
		t.Fatal(err)
	}
	rules := "# Rules\n**R1 — schema.** first\n\n**R10 — hunk coverage.** tenth\n"
	if err := os.WriteFile(filepath.Join(dir, "references", "design-check-list-schema.md"), []byte(rules), 0o644); err != nil {
		t.Fatal(err)
	}
	out := capture(t, func() { _ = call(t, cmdPacket, dir, "A1", "--role", "producer") })
	headers := []string{"## Intent", "## Brief", "## Notes", "## Rules cited", "## Rules not included", "## Scope"}
	position := -1
	for _, header := range headers {
		at := strings.Index(out, header+"\n")
		if at <= position {
			t.Fatalf("%q is missing or out of order:\n%s", header, out)
		}
		position = at
	}
	if !strings.Contains(out, "\nR1 — schema\n") || !strings.Contains(out, "\nR10 — hunk coverage\n") {
		t.Fatalf("the index does not name both rules:\n%s", out)
	}
	if strings.Contains(out, "## Claims") {
		t.Fatal("the producer packet prints the claims section")
	}
}

func TestPacketRefusesAnUnknownItemOrRole(t *testing.T) {
	dir := openItem(t)
	for _, argv := range [][]string{{"A1"}, {"A1", "--role", "judge"}, {"A1", "--role", ""}, {"ZZ9", "--role", "checks"}} {
		if err := call(t, cmdPacket, dir, argv...); err == nil {
			t.Errorf("bl packet %v succeeded, want a refusal", argv)
		}
	}
}
