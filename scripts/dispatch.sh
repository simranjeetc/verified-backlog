#!/bin/sh
# usage: scripts/dispatch.sh <role> <prompt file> <expected output file> [timeout secs]
# Three cheap models only: gpt-6-luna via codex exec (Codex's workspace-write sandbox, no
# approval prompts), claude:haiku via claude -p (Claude Code's bash sandbox, edits auto-accepted)
# and deepseek-v4.1-flash via opencode run (opencode-go). Tries each model in the role's chain;
# moves on when the run fails, times out, or leaves no output file. CHAIN="m1 m2" overrides it.
role=$1; prompt=$2; out=$3; secs=${4:-1800}
case $role in
  design)   chain="gpt-6-luna deepseek-v4.1-flash claude:haiku" ;;
  checks)   chain="claude:haiku deepseek-v4.1-flash gpt-6-luna" ;;
  produce)  chain="gpt-6-luna deepseek-v4.1-flash claude:haiku" ;;
  verify)   chain="deepseek-v4.1-flash claude:haiku gpt-6-luna" ;;
  light)    chain="gpt-6-luna deepseek-v4.1-flash claude:haiku" ;;
  *) echo "dispatch.sh: role must be design, checks, produce, verify or light" >&2; exit 64 ;;
esac
chain=${CHAIN:-$chain}; log=$out.log; : > "$log"
outdir=$(cd "$(dirname "$out")" && pwd -P)   # the report may live outside the sandbox's roots
# go build's default cache is outside the sandbox's writable roots
export GOCACHE=${GOCACHE_DISPATCH:-${TMPDIR:-/tmp}/dispatch-gocache}
for m in $chain; do
  case $m in
    gpt-6-luna|claude:haiku|deepseek-v4.1-flash) ;;
    *) echo "dispatch.sh: $m refused; only gpt-6-luna, claude:haiku and deepseek-v4.1-flash" >&2; exit 64 ;;
  esac
  rm -f "$out"
  echo "== $m $(date +%H:%M:%S)" >> "$log"
  if [ "$m" = claude:haiku ]; then
    # Claude Code's sandbox cannot write $TMPDIR, so mktemp and go build get a dir it can write
    work=$outdir/.dispatch-tmp; mkdir -p "$work/gocache"
    TMPDIR=$work GOCACHE=$work/gocache perl -e 'alarm shift; exec @ARGV' "$secs" \
      claude -p --model haiku --permission-mode acceptEdits --add-dir "$outdir" \
        --allowedTools Bash Read Write Edit Grep Glob \
        --settings '{"sandbox":{"enabled":true,"autoAllowBashIfSandboxed":true}}' \
        >> "$log" 2>&1 < "$prompt"
  elif [ "$m" = deepseek-v4.1-flash ]; then
    perl -e 'alarm shift; exec @ARGV' "$secs" \
      opencode run --title "$(basename "$out") $m" --thinking --model "opencode-go/$m" "$(cat "$prompt")" \
        >> "$log" 2>&1 < /dev/null
  else
    perl -e 'alarm shift; exec @ARGV' "$secs" \
      codex exec -s workspace-write -c approval_policy='"never"' --skip-git-repo-check --ephemeral \
        -c "sandbox_workspace_write.writable_roots=[\"$outdir\"]" \
        -m "$m" - >> "$log" 2>&1 < "$prompt"
  fi
  rc=$?
  echo "== exit $rc $(date +%H:%M:%S)" >> "$log"
  if [ $rc -eq 0 ] && [ -s "$out" ]; then echo "MODEL $m"; exit 0; fi
done
echo "dispatch.sh: every model in the $role chain failed; see $log" >&2; exit 1
