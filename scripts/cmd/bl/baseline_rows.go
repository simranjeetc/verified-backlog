package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func hasBaseline(rows []checkRow) bool {
	for _, row := range rows {
		if _, ok := row.f["baseline"]; ok {
			return true
		}
	}
	return false
}

// validateBaselineRows checks the proposed declarations against the recorded before-tree.
// It runs sequentially because a baseline row may consume most of the row timeout.
func validateBaselineRows(root string, it *Item, rows []checkRow, amend bool, suppliedTests []byte, hasTests bool) (ran bool, resultErr error) {
	if !hasBaseline(rows) {
		fmt.Fprintln(os.Stderr, "bl: warning: no baseline check (list has no baseline column)")
		return false, nil
	}
	if !isGit(root) || it.BeforeTree == "" {
		fmt.Fprintln(os.Stderr, "bl: warning: no baseline check (root is not a git repository or has no before-tree)")
		return false, nil
	}
	start := 0
	if amend {
		oldData, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
		if err != nil {
			return false, err
		}
		old, err := parseChecks(string(oldData))
		if err != nil {
			return false, err
		}
		start = len(old)
	}
	var baseline []checkRow
	newRed, keepGreen := 0, 0
	for _, row := range rows[start:] {
		if row.f["decider"] == "bl" && row.f["baseline"] != "" {
			baseline = append(baseline, row)
		}
	}
	if len(baseline) == 0 {
		fmt.Printf("bl: %s baseline: 0 new red, 0 keep green\n", it.ID)
		return true, nil
	}
	treeBefore, err := verifiedTreeWriting(root)
	if err != nil {
		return true, refuseRule("R40", "cannot capture repository tree before baseline: %v", err)
	}
	backlogBefore, err := backlogHash(root)
	if err != nil {
		return true, refuseRule("R40", "cannot hash .backlog before baseline: %v", err)
	}
	stateMoved := func(id string) error {
		treeAfter, treeErr := verifiedTreeWriting(root)
		if treeErr != nil || treeAfter != treeBefore {
			return refuseRule("R40", "row %s: baseline changed the repository tree", id)
		}
		backlogAfter, hashErr := backlogHash(root)
		if hashErr != nil || backlogAfter != backlogBefore {
			return refuseRule("R40", "row %s: baseline changed .backlog", id)
		}
		return nil
	}
	scratch, cleanup, err := scratchTree(root, it.BeforeTree, it.ID)
	if err != nil {
		return true, refuseRule("R40", "before copy setup error: %v", err)
	}
	defer cleanup()
	if hasTests {
		path := filepath.Join(scratch, filepath.FromSlash(testPath(it.ID, it.Attempt)))
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return true, refuseRule("R40", "before copy setup error: %v", err)
		}
		if err := os.WriteFile(path, suppliedTests, 0644); err != nil {
			return true, refuseRule("R40", "before copy setup error: %v", err)
		}
	}
	env, done, err := execEnv(root)
	if err != nil {
		return true, refuseRule("R40", "before copy setup error: %v", err)
	}
	defer done()
	if _, err := os.Stat(filepath.Join(scratch, "scripts", "cmd", "bl")); err == nil {
		bin := filepath.Join(envHome(env), "bl-under-test")
		build := exec.Command("go", "-C", "scripts", "build", "-o", bin, "./cmd/bl")
		build.Dir, build.Env = scratch, setEnv(append([]string{}, env...), "HOME", envHome(env))
		out, buildErr := build.CombinedOutput()
		if buildErr != nil {
			return true, refuseRule("R40", "row %s setup error: cannot build bl: %s", baseline[0].f["id"], strings.TrimSpace(string(out)))
		}
		env = setEnv(env, "BL_UNDER_TEST", bin)
	}
	for index, row := range baseline {
		rowTmp := filepath.Join(envTmp(env), fmt.Sprintf("baseline-row-%d", index+1))
		if err := os.Mkdir(rowTmp, 0755); err != nil {
			return true, refuseRule("R40", "row %s setup error: %v", row.f["id"], err)
		}
		rec := execRow(scratch, row, setEnv(env, "TMPDIR", rowTmp), runTimeout)
		if err := stateMoved(row.f["id"]); err != nil {
			return true, err
		}
		if rec.TimedOut || rec.Exit == 126 || rec.Exit == 127 {
			return true, refuseRule("R40", "row %s declared %s: setup error (exit %d)", row.f["id"], row.f["baseline"], rec.Exit)
		}
		if name, test := testRowName(2, it, row); test {
			if rec.Exit != 0 && rec.Exit != 1 {
				return true, refuseRule("R40", "row %s declared %s: setup error (exit %d)", row.f["id"], row.f["baseline"], rec.Exit)
			}
			if !strings.Contains("\n"+rec.Stdout, "\nbl-test: "+name+"\n") {
				return true, refuseRule("R40", "row %s declared %s: setup error (no bl-test: %s line)", row.f["id"], row.f["baseline"], name)
			}
		}
		verdict, err := expectVerdict(scratch, row.f["id"], row.f["expect"], rec, it.BeforeTree)
		if err != nil {
			return true, err
		}
		if (row.f["baseline"] == "new" && verdict == "PASS") || (row.f["baseline"] == "keep" && verdict != "PASS") {
			observed := "fails"
			if verdict == "PASS" {
				observed = "passes"
			}
			return true, refuseRule("R40", "row %s: declared %s but it %s on the before-tree", row.f["id"], row.f["baseline"], observed)
		}
		if row.f["baseline"] == "new" {
			newRed++
		} else {
			keepGreen++
		}
	}
	fmt.Printf("bl: %s baseline: %d new red, %d keep green\n", it.ID, newRed, keepGreen)
	return true, nil
}
