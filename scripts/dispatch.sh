#!/bin/sh
# usage: scripts/dispatch.sh <role> <prompt file> <expected output file> [timeout secs]
# Cheap Codex (ChatGPT plan) models only (luna tier) via codex exec, inside Codex's workspace-write sandbox (cwd, /tmp
# and $TMPDIR writable; no approval prompts). Tries each model in the role's chain; moves on when
# the run fails, times out, or leaves no output file. CHAIN="m1 m2" overrides the chain.
role=$1; prompt=$2; out=$3; secs=${4:-1800}
case $role in
  design)   chain="gpt-6-luna gpt-5.6-luna" ;;
  checks)   chain="gpt-6-luna gpt-5.6-luna" ;;
  produce)  chain="gpt-5.6-luna gpt-6-luna" ;;
  verify)   chain="gpt-6-luna gpt-5.6-luna" ;;
  light)    chain="gpt-5.6-luna gpt-6-luna" ;;
  *) echo "dispatch.sh: role must be design, checks, produce, verify or light" >&2; exit 64 ;;
esac
chain=${CHAIN:-$chain}; log=$out.log; : > "$log"
outdir=$(cd "$(dirname "$out")" && pwd -P)   # the report may live outside the sandbox's roots
# go build's default cache is outside the sandbox's writable roots
export GOCACHE=${GOCACHE_DISPATCH:-${TMPDIR:-/tmp}/dispatch-gocache}
for m in $chain; do
  case $m in
    gpt-*-luna) ;;
    *) echo "dispatch.sh: $m refused; only cheap luna models (gpt-*-luna)" >&2; exit 64 ;;
  esac
  rm -f "$out"
  echo "== $m $(date +%H:%M:%S)" >> "$log"
  perl -e 'alarm shift; exec @ARGV' "$secs" \
    codex exec -s workspace-write -c approval_policy='"never"' --skip-git-repo-check --ephemeral \
      -c "sandbox_workspace_write.writable_roots=[\"$outdir\"]" \
      -m "$m" - >> "$log" 2>&1 < "$prompt"
  rc=$?
  echo "== exit $rc $(date +%H:%M:%S)" >> "$log"
  if [ $rc -eq 0 ] && [ -s "$out" ]; then echo "MODEL $m"; exit 0; fi
done
echo "dispatch.sh: every model in the $role chain failed; see $log" >&2; exit 1
