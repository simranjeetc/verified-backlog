package main

import (
	"errors"
	"os/exec"
	"strings"
)

// ── git ──────────────────────────────────────────────────────────────────────

func git(root string, args ...string) (string, int) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	out, err := cmd.Output()
	code := 0
	if err != nil {
		code = 1
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			code = ee.ExitCode()
		}
	}
	return string(out), code
}

func isGit(root string) bool {
	_, code := git(root, "rev-parse", "--git-dir")
	return code == 0
}

// isArtifact reports whether a repository-relative path is one of the artefacts of R25: a path
// under the ledger's own directory, or one of the two rendered views, TODO.md and PROGRESS.md.
// The artefacts are excluded from the verified tree and from the round's diff.
func isArtifact(rel string) bool {
	return strings.HasPrefix(rel, backlogDir+"/") || rel == queueView || rel == recordView
}
