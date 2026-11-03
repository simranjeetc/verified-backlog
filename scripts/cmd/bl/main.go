// Command bl is the verified-backlog ledger.
//
// State lives in .backlog/ledger.jsonl, one JSON object per line, append-only, in call order.
// TODO.md and PROGRESS.md are rendered from it after every append and are never read by a
// program. The model judges; this binary records, refuses, and renders.
//
// One command, one append. Nothing is rewritten and nothing is inferred: if a line is not in
// the ledger, it did not happen.
//
// Usage:
//
//	bl init
//	bl file --id A1 --text "..." [--after B2] [--source human] [--proof "command and result"] [--bucket fix-now|batch|later] [--urgent]
//	bl open A1                                            (writes .backlog/items/A1/brief.md)
//	bl brief A1 --file brief.md                           (amends the brief; a new event)
//	bl checks A1 [--file checks.tsv] [--tests tests.py] [--amend]  (list from stdin without --file)
//	bl rebaseline A1 --file checks.tsv [--tests tests.py] --reason "..."  (replaces the frozen list)
//	bl baseline A1 --reason "external committed changes"   (preserves in-scope baseline; fresh run required)
//	bl claims A1 --file claims.md
//	bl run A1                                            (runs the frozen rows; writes the run file)
//	bl round A1 --verdict FAIL --cause code --rows -      (rows from stdin)
//	bl round A1 --verdict PASS --rows checks.txt
//	bl close A1 --commit 6742cf6
//	bl drop D9 --reason "cosmetic; not worth it"
//	bl defer O4 --reason "awaiting triage"
//	bl reopen O4
//	bl restart A1 --reason bad-check --why "..." --change "..."  (ends the attempt, starts the next)
//	bl note A1 --text "deferred because ..."
//	bl bump A3 A1 | bl unbump A3                          (the human's priority list: tier 1)
//	bl rank --file ranking.tsv                            (the loop's ranking: tier 2)
//	bl next | bl show A1 | bl packet A1 --role checks | bl status | bl check [--views] | bl render
//	bl audit [--since 2d] [--out .backlog/audit]
//	bl import TODO.md PROGRESS.md | bl lint TODO.md PROGRESS.md
//	bl schema [--json]
//
// Exit status: 0 ok, 2 refused (nothing written), 3 the ledger itself is corrupt.
// Every invocation appends one line to .backlog/run.log: argv, exit code, duration.
package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ── cli ──────────────────────────────────────────────────────────────────────

type args struct {
	flags      map[string]string
	boolFlags  map[string]bool
	lists      map[string][]string
	positional []string
}

func (a args) get(name string) string { return a.flags[name] }
func (a args) flag(name string) bool  { return a.boolFlags[name] }

// list returns every value a repeatable flag was given, in the order they were given.
func (a args) list(name string) []string { return a.lists[name] }

func (a args) pos(i int) string {
	if i < len(a.positional) {
		return a.positional[i]
	}
	return ""
}

func (a args) rows() (string, error) {
	path := a.get("rows")
	if path == "" {
		return "", refuse("--rows is required")
	}
	if path == "-" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", refuse("--rows %s: %v", path, err)
	}
	return string(data), nil
}

// input reads the file named by a flag, or stdin when the flag is absent.
func (a args) input(name string) (string, error) {
	path := a.get(name)
	if path == "" {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", refuse("--%s %s: %v", name, path, err)
	}
	return string(data), nil
}

var boolFlagNames = map[string]bool{"json": true, "views": true, "due": true, "amend": true, "urgent": true, "check": true, "render": true, "init": true, "cancel": true}

// repeatableFlagNames are the flags that may be given more than once: the item's scope is a
// set of paths, and the scope-ignored list is a set of files.
var repeatableFlagNames = map[string]bool{"scope": true, "scope-ignored": true}

func parseArgs(raw []string) (args, error) {
	a := args{flags: map[string]string{}, boolFlags: map[string]bool{}, lists: map[string][]string{}}
	for i := 0; i < len(raw); i++ {
		tok := raw[i]
		if strings.HasPrefix(tok, "--") {
			name := strings.TrimPrefix(tok, "--")
			if eq := strings.Index(name, "="); eq >= 0 {
				a.flags[name[:eq]] = name[eq+1:]
				a.lists[name[:eq]] = append(a.lists[name[:eq]], name[eq+1:])
				continue
			}
			if boolFlagNames[name] {
				a.boolFlags[name] = true
				continue
			}
			if i+1 >= len(raw) {
				return a, refuse("--%s needs a value", name)
			}
			a.flags[name] = raw[i+1]
			a.lists[name] = append(a.lists[name], raw[i+1])
			i++
			continue
		}
		a.positional = append(a.positional, tok)
	}
	return a, nil
}

var handlers = map[string]func(string, args) error{
	"init":         cmdInit,
	"file":         cmdFile,
	"open":         cmdOpen,
	"brief":        cmdBrief,
	"checks":       cmdChecks,
	"rebaseline":   cmdRebaseline,
	"baseline":     cmdBaseline,
	"claims":       cmdClaims,
	"run":          cmdRun,
	"round":        cmdRound,
	"verdicts":     cmdVerdicts,
	"second-check": cmdSecondCheck,
	"close":        cmdClose,
	"drop":         cmdDrop,
	"defer":        cmdDefer,
	"reopen":       cmdReopen,
	"restart":      cmdRestart,
	"loop":         cmdLoop,
	"note":         cmdNote,
	"signoff":      cmdSignoff,
	"bump":         cmdBump,
	"unbump":       cmdUnbump,
	"rank":         cmdRank,
	"next":         cmdNext,
	"show":         cmdShow,
	"packet":       cmdPacket,
	"status":       cmdStatus,
	"render":       cmdRender,
	"check":        cmdCheck,
	"schema":       cmdSchema,
	"intake":       cmdIntake,
	"audit":        cmdAudit,
	"import":       cmdImport,
	"lint":         cmdLint,
}

const usage = `usage: bl [--root DIR] [--json] VERB [args]

verbs:
  init | file | open | brief | checks | rebaseline | claims | run | round | second-check | close
  drop | defer | reopen | restart | note | signoff | bump | unbump | rank | next | show | packet | status | check
  render | schema | intake | audit | import | lint | loop | verdicts

state: .backlog/ledger.jsonl  ·  views: TODO.md, PROGRESS.md  ·  log: .backlog/run.log
exit: 0 ok · 2 refused (nothing written) · 3 corrupt ledger`

func run(argv []string) int {
	defer releaseLoopLock()
	started := time.Now()
	root, _ := os.Getwd()
	rest := argv
	for len(rest) > 0 && strings.HasPrefix(rest[0], "--") {
		switch {
		case rest[0] == "--root" && len(rest) > 1:
			root = rest[1]
			rest = rest[2:]
		case strings.HasPrefix(rest[0], "--root="):
			root = strings.TrimPrefix(rest[0], "--root=")
			rest = rest[1:]
		case rest[0] == "--help" || rest[0] == "-h":
			fmt.Println(usage)
			return 0
		default:
			goto verb
		}
	}
verb:
	if len(rest) == 0 {
		fmt.Println(usage)
		return 2
	}
	verb := rest[0]
	handler, ok := handlers[verb]
	if !ok {
		fmt.Fprintf(os.Stderr, "bl: unknown verb %q\n%s\n", verb, usage)
		return 2
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		abs = root
	}
	a, err := parseArgs(rest[1:])
	code := 0
	if err != nil {
		code = report(err)
	} else {
		err = handler(abs, a)
		code = report(err)
	}
	if verb != "intake" && !(verb == "schema" && len(rest) > 1 && rest[1] == "intake") {
		rule := ""
		var refusal Refusal
		if errors.As(err, &refusal) {
			rule = refusal.rule
		}
		logRun(abs, verb, rest[1:], code, rule, started)
	}
	return code
}

func report(err error) int {
	if err == nil {
		return 0
	}
	var refusal Refusal
	var corruption Corruption
	var loopExit loopExitError
	switch {
	case errors.As(err, &loopExit):
		fmt.Fprintf(os.Stderr, "bl loop: %s\n", loopExit.msg)
		return loopExit.code
	case errors.As(err, &refusal):
		if refusal.rule == "intake" {
			fmt.Fprintln(os.Stderr, refusal.msg)
			return 2
		}
		if refusal.rule != "" {
			fmt.Fprintf(os.Stderr, "bl: refused (%s): %s\n", refusal.rule, refusal.msg)
		} else {
			fmt.Fprintf(os.Stderr, "bl: refused: %s\n", refusal.msg)
		}
		return 2
	case errors.As(err, &corruption):
		fmt.Fprintf(os.Stderr, "bl: corrupt ledger: %s\n", corruption.msg)
		return 3
	case errors.Is(err, errFailed):
		return 1
	default:
		fmt.Fprintf(os.Stderr, "bl: %v\n", err)
		return 1
	}
}

func main() { os.Exit(run(os.Args[1:])) }
