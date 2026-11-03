package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"
)

// ── the run ──────────────────────────────────────────────────────────────────

// runTimeout is the timeout of §2: after it the process GROUP is killed, and the row is
// recorded with timed_out = true and exit = -1, which is distinct from any command's own exit
// code — a timeout can never be written up as a pass. It is a variable so a test can shorten it.
var runTimeout = 120 * time.Second

// runRow is one row's record in the run file (§2).
type runRow struct {
	ID           string `json:"id"`
	Cmd          string `json:"cmd"`
	Exit         int    `json:"exit"`
	MS           int64  `json:"ms"`
	TimedOut     bool   `json:"timed_out"`
	Signalled    bool   `json:"signalled"`
	Stdout       string `json:"stdout"`
	StdoutSHA    string `json:"stdout_sha256"`
	StdoutBinary bool   `json:"stdout_binary"`
	Stderr       string `json:"stderr"`
	StderrSHA    string `json:"stderr_sha256"`
	StderrBinary bool   `json:"stderr_binary"`
	// Verdict is the row-verdict the binary computed for a `decider: bl` row, from the row's
	// expectation and the values observed above (R7). A `decider: judge` row carries none: its
	// row-verdict is the judge's, and the judge reads this file to write it.
	Verdict string `json:"verdict,omitempty"`
}

// runFile is the run file (§2): the item, the run id, the verified tree at the start and at the
// end of the run, the environment the rows ran in, and every row's record.
type runFile struct {
	ID        string            `json:"id"`
	Run       string            `json:"run"`
	TreeStart string            `json:"tree_start"`
	TreeEnd   string            `json:"tree_end"`
	Env       map[string]string `json:"env"`
	Rows      []runRow          `json:"rows"`
	Derived   *derivedRun       `json:"derived,omitempty"`
}

// cmdRun executes every row of the frozen check list under the fixed environment (§2, R21),
// runs derivation, verifies the tree and backlog stayed unchanged, writes the run file, and
// records the run event (R7).
//
// The order is P3's: R35 (the item is the one in flight) → R20 (the frozen artefacts are what
// they were) → R1 (the list still parses) → R7 (every row ran and its row-verdict was computed;
// R8, the expectation's form, is refused inside it) → derivation → R26 (the tree did not move)
// → R27 (the backlog state did not move). A run is refused on the first of these that fails,
// and no run file is written.
//
// R26 and R27 are compared after derivation too, since a derivation command may write through an
// absolute path into the real tree or backlog. The list must be parsed before execution because
// the rows come out of it.
func cmdRun(root string, a args) error {
	jobs := runtime.NumCPU()
	if values := a.list("jobs"); len(values) > 0 {
		value := a.get("jobs")
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			return refuse("--jobs must be an integer of at least 1")
		}
		jobs = parsed
	}
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
	// R35 (P3): the item must be the one in flight. The check comes FIRST on this path — before
	// the item's own state (a declined item, a missing check list) and before R20 — so that a run
	// on an item that is not in flight is refused under R35 and under no other rule.
	it, err := lookup(byID, a.pos(0))
	if err != nil {
		return err
	}
	if err := requireInFlight(order, it, "R35"); err != nil {
		return err
	}
	// R20: the frozen artefacts are what they were. The brief, the check list and the claims are
	// re-hashed and compared with the hash of the latest event that recorded each of them, so a
	// frozen artefact that was edited after the freeze is refused at the point of use.
	if err := verifyFrozen(root, it); err != nil {
		return err
	}
	// R20: the frozen test file is a frozen artefact too (I89, B8).
	if err := verifyTests(root, it); err != nil {
		return err
	}
	// R1: the frozen list still parses.
	if it.Checks == "" {
		return refuseRule("R1", "%s has no check list to run", it.ID)
	}
	list, err := verifyArtefact(root, it.Checks, it.ChecksSHA, "check list")
	if err != nil {
		return err
	}
	rows, err := parseChecks(string(list))
	if err != nil {
		return err
	}
	if err := validateR1(rows); err != nil {
		return err
	}
	// The state before the run: the verified tree, computed from the FILESYSTEM, and the hash of
	// the whole of .backlog/. The tree is written to the object store, so that the round can
	// diff it against the tree the round verifies.
	treeStart, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	ledgerBefore, err := backlogHash(root)
	if err != nil {
		return err
	}
	// The scratch directory: outside the repository, and removed after the run.
	env, cleanup, err := execEnv(root)
	if err != nil {
		return err
	}
	defer cleanup()
	blUnderTest := ""
	if info, err := os.Stat(filepath.Join(root, "scripts", "cmd", "bl")); err == nil && info.IsDir() {
		blUnderTest = filepath.Join(filepath.Dir(envHome(env)), "bin", "bl")
		if err := os.MkdirAll(filepath.Dir(blUnderTest), 0o755); err != nil {
			return err
		}
		cmd := exec.Command("go", "-C", "scripts", "build", "-o", blUnderTest, "./cmd/bl")
		cmd.Dir = root
		cmd.Env = os.Environ()
		output, err := cmd.CombinedOutput()
		if err != nil {
			return refuse("cannot build bl under test: %s", output)
		}
		env = withEnvValue(env, "BL_UNDER_TEST", blUnderTest)
	}
	path := os.Getenv("PATH")
	home := envHome(env)
	tmpdir := envTmp(env)
	// R7: every row runs, with records in frozen-list order, whatever the decider. A
	// `decider: bl` row's row-verdict is computed HERE, from the row's expectation and the
	// values the run observed: the exit code is not the verdict, and a row whose command
	// refused is a PASS row when its expectation matches what the refusal printed. A
	// `decider: judge` row's row-verdict is the judge's, and the judge reads this file.
	records := make([]runRow, len(rows))
	rowEnvs := make([][]string, len(rows))
	for i := range rows {
		rowTmp := filepath.Join(tmpdir, fmt.Sprintf("row-%d", i+1))
		if err := os.Mkdir(rowTmp, 0o755); err != nil {
			return err
		}
		rowEnvs[i] = withEnvValue(env, "TMPDIR", rowTmp)
	}
	if jobs > len(rows) {
		jobs = len(rows)
	}
	indices := make(chan int)
	var workers sync.WaitGroup
	for w := 0; w < jobs; w++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range indices {
				records[i] = execRow(root, rows[i], rowEnvs[i], runTimeout)
			}
		}()
	}
	for i := range rows {
		indices <- i
	}
	close(indices)
	workers.Wait()
	for i, row := range rows {
		rec := records[i]
		if row.f["decider"] == "bl" {
			v, err := expectVerdict(root, row.f["id"], row.f["expect"], rec, treeStart)
			if err != nil {
				return err
			}
			if rec.TimedOut {
				// R8: a timeout is recorded with exit = -1, and -1 is not a valid
				// expectation, so a timeout can never be written up as a pass.
				v = "FAIL"
			}
			rec.Verdict = v
		}
		records[i] = rec
	}
	// The state after the run.
	treeEnd, err := verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	ledgerAfter, err := backlogHash(root)
	if err != nil {
		return err
	}
	if treeEnd != treeStart {
		return refuseRule("R26", "the tree moved under the run: %s before, %s after — a row's command changed the tree the rows ran against", treeStart, treeEnd)
	}
	if ledgerAfter != ledgerBefore {
		return refuseRule("R27", "the %s directory changed under the run: %s before, %s after — a row's command wrote to the ledger, the evidence or the item files", backlogDir, ledgerBefore, ledgerAfter)
	}
	// The run file: .backlog/evidence/<id>-run<N>.json, N being the run's number for the item.
	n := len(it.Runs) + 1
	runID := fmt.Sprintf("run%d", n)
	name := fmt.Sprintf("%s-run%d.json", it.ID, n)
	rf := runFile{
		ID: it.ID, Run: runID,
		TreeStart: treeStart, TreeEnd: treeEnd,
		Env: map[string]string{
			"PATH": path, "HOME": home, "TMPDIR": tmpdir, "LANG": "C", "TZ": "UTC",
		},
		Rows: records,
	}
	derived, deriveErr := deriveCoverage(root, it, rows, records, env, it.BeforeTree, treeEnd)
	if deriveErr != nil {
		return deriveErr
	}
	rf.Derived = derived
	// Derivation executes rows in scratch trees, but a row can still write through an absolute
	// path. Recheck both protected states after the entire derivation pass before recording it.
	treeEnd, err = verifiedTreeWriting(root)
	if err != nil {
		return err
	}
	ledgerAfter, err = backlogHash(root)
	if err != nil {
		return err
	}
	if treeEnd != treeStart {
		return refuseRule("R26", "the tree moved under derivation: %s before, %s after — a derivation row changed the real tree through an absolute path", treeStart, treeEnd)
	}
	if ledgerAfter != ledgerBefore {
		return refuseRule("R27", "the %s directory changed under derivation: %s before, %s after — a derivation row wrote to the ledger, the evidence or the item files", backlogDir, ledgerBefore, ledgerAfter)
	}
	if derived != nil {
		for _, p := range derived.Paths {
			if p.Verdict != "exempt" {
				fmt.Printf("bl: %s %s %s %s", it.ID, p.ID, p.Path, p.Verdict)
				if p.KilledBy != "" {
					fmt.Printf(" killed_by %s", p.KilledBy)
				}
				fmt.Println()
			}
		}
	}
	if blUnderTest != "" {
		rf.Env["BL_UNDER_TEST"] = blUnderTest
	}
	data, err := encodeRun(rf)
	if err != nil {
		return err
	}
	p := newPaths(root)
	if err := os.MkdirAll(p.evidence, 0o755); err != nil {
		return err
	}
	if err := atomicWrite(filepath.Join(p.evidence, name), string(data)); err != nil {
		return err
	}
	// The ledger records the run and the hash of the file it names (R20).
	ev := Event{T: "run", ID: it.ID, Run: runID, RunSHA: shaHex(data)}
	if _, err := appendEvent(root, ev); err != nil {
		return err
	}
	// The run's exit code follows the rows' verdicts: a run whose rows all PASS exits 0, even
	// when a row's command exited non-zero because the binary refused. A judge row has no
	// verdict yet — the judge writes it — so it cannot fail the run.
	failed := 0
	for _, r := range records {
		if r.Verdict == "FAIL" {
			failed++
		}
	}
	if failed > 0 {
		fmt.Printf("bl: %s %s written (%d rows, %d failed)\n", it.ID, runID, len(records), failed)
		return errFailed
	}
	fmt.Printf("bl: %s %s written (%d rows)\n", it.ID, runID, len(records))
	return nil
}

// encodeRun writes the run file as JSON: indented, and without HTML escaping, so the bytes the
// judge reads are the bytes the row produced.
func encodeRun(rf runFile) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(rf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// execEnv prepares the environment of R21 and the scratch directory it points at: PATH is
// inherited from the caller, HOME and TMPDIR point into a fresh scratch directory outside the
// repository, LANG=C, TZ=UTC, and nothing else — in particular no GIT_* and no other variable
// of the caller's. The cleanup removes the scratch directory.
func execEnv(root string) ([]string, func(), error) {
	scratch, err := scratchDir(root)
	if err != nil {
		return nil, nil, err
	}
	home := filepath.Join(scratch, "home")
	tmpdir := filepath.Join(scratch, "tmp")
	for _, d := range []string{home, tmpdir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			os.RemoveAll(scratch)
			return nil, nil, err
		}
	}
	env := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"TMPDIR=" + tmpdir,
		"LANG=C",
		"TZ=UTC",
	}
	return env, func() { os.RemoveAll(scratch) }, nil
}

// envHome and envTmp read the HOME and the TMPDIR out of the fixed environment.
func envHome(env []string) string { return envValue(env, "HOME") }
func envTmp(env []string) string  { return envValue(env, "TMPDIR") }

func envValue(env []string, name string) string {
	for _, e := range env {
		if strings.HasPrefix(e, name+"=") {
			return strings.TrimPrefix(e, name+"=")
		}
	}
	return ""
}

func withEnvValue(env []string, name, value string) []string {
	updated := make([]string, len(env))
	copy(updated, env)
	for i, entry := range updated {
		if strings.HasPrefix(entry, name+"=") {
			updated[i] = name + "=" + value
			return updated
		}
	}
	return append(updated, name+"="+value)
}

// execRow runs one row's command and records what it saw: the exit code, whether it timed out,
// the full output of each stream, and the sha256 of each.
//
// A command that exits non-zero, a command that cannot be run at all and a death by a signal are
// each recorded as what they are — never swallowed, never turned into a pass.
func execRow(root string, row checkRow, env []string, timeout time.Duration) runRow {
	rec := execCmd(root, row.f["cmd"], env, timeout)
	rec.ID = row.f["id"]
	return rec
}

// execCmd runs one command under the fixed environment and records what it saw.
func execCmd(root string, command string, env []string, timeout time.Duration) runRow {
	rec := runRow{Cmd: command}
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Dir = root
	cmd.Env = env
	cmd.Stdin = nil // /dev/null
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// Its own process group, so a timeout kills the children too.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	started := time.Now()
	if err := cmd.Start(); err != nil {
		// the command could not be run at all
		rec.Exit = 127
		rec.Stderr = err.Error() + "\n"
		rec.StderrSHA = shaHex([]byte(rec.Stderr))
		rec.MS = max(0, time.Since(started).Milliseconds())
		return rec
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		rec.Exit, rec.Signalled = exitCodeOf(err)
	case <-time.After(timeout):
		// the process GROUP is killed, and the row is recorded as a timeout: exit = -1, which
		// is distinct from any command's own exit code.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		rec.Exit = -1
		rec.TimedOut = true
	}
	rec.MS = max(0, time.Since(started).Milliseconds())
	rec.Stdout, rec.StdoutBinary = encodeStream(out.Bytes())
	rec.Stderr, rec.StderrBinary = encodeStream(errOut.Bytes())
	rec.StdoutSHA = shaHex(out.Bytes())
	rec.StderrSHA = shaHex(errOut.Bytes())
	return rec
}

// exitCodeOf reads the exit code of a finished command. A process killed by a signal is
// recorded as 128+signal, and a command that exits 137 on its own is recorded as 137 — the
// signalled flag is what tells the two apart.
func exitCodeOf(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	var ee *exec.ExitError
	if !errors.As(err, &ee) {
		return 127, false
	}
	if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
		return 128 + int(ws.Signal()), true
	}
	code := ee.ExitCode()
	if code < 0 {
		return 127, false
	}
	return code, false
}

// encodeStream stores a captured stream as text. Output that is not valid UTF-8 is stored
// base64, with the binary flag set (§2).
func encodeStream(data []byte) (string, bool) {
	if utf8.Valid(data) {
		return string(data), false
	}
	return base64.StdEncoding.EncodeToString(data), true
}

// scratchDir creates the run's scratch directory: outside the repository, and removed after the
// run (§2). HOME and TMPDIR point into it, so a tool that caches under HOME cannot write into
// the tree.
func scratchDir(root string) (string, error) {
	for _, base := range []string{os.TempDir(), "/tmp", "/private/tmp"} {
		if base == "" {
			continue
		}
		dir, err := os.MkdirTemp(base, "bl-run-")
		if err != nil {
			continue
		}
		if inside(root, dir) {
			os.RemoveAll(dir)
			continue
		}
		return dir, nil
	}
	return "", refuse("cannot create a scratch directory outside the repository")
}

// inside reports whether path lies under root.
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// backlogHash hashes .backlog/ except run.log — every other file's path and content — so that a
// row that writes to the ledger, the evidence or the item files is caught (R27). P10 defines
// run.log as the log of every call, not state.
func backlogHash(root string) (string, error) {
	base, err := resolvedBacklogBase(root)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	var files []string
	err = filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if filepath.Clean(p) == filepath.Join(base, runlogName) {
			return nil
		}
		files = append(files, p)
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	sort.Strings(files)
	h := sha256.New()
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			return "", err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%s\n", filepath.ToSlash(rel), shaHex(data))
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
