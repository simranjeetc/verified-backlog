#!/bin/sh
# usage: scripts/dispatch.sh <role> <prompt file> <expected output file> [timeout secs]
# Starts and waits for one dispatch job, printing the model on success.
role=$1
prompt=$2
out=$3
secs=${4:-1800}
dispatch_bin=${DISPATCH_BIN:-$HOME/.agents/bin/dispatch}

case $out in
  /*) abs_out=$out ;;
  *) abs_out=$(pwd -P)/$out ;;
esac
outdir=$(dirname "$abs_out")
title=$(basename "$out")
if [ -n "${BL_LOOP_TOKEN:-}" ]; then
  title=bl-worker-$BL_LOOP_TOKEN-$title
fi
prompt_copy=$(mktemp /tmp/dispatch-prompt.XXXXXX) || exit 1
trap 'rm -f "$prompt_copy"' 0
{
  printf 'Write your final report to %s; a report printed only to stdout is lost.\n' "$abs_out"
  cat "$prompt"
} > "$prompt_copy" || exit 1

chain_arg=
if [ "${CHAIN+x}" = x ]; then
  set -- $CHAIN
  [ "$#" -le 2 ] || { echo "dispatch.sh: at most two models per chain (primary, fallback)" >&2; exit 64; }
  mapped=
  for model do
    case $model in
      gpt-6-luna) entry=codex:$model ;;
      claude:haiku) entry=$model ;;
      deepseek-v4.1-flash) entry=opencode:$model ;;
      *) echo "dispatch.sh: $model refused; only gpt-6-luna, claude:haiku and deepseek-v4.1-flash" >&2; exit 64 ;;
    esac
    if [ -n "$mapped" ]; then mapped=$mapped,$entry; else mapped=$entry; fi
  done
  [ -n "$mapped" ] || { echo "dispatch.sh: empty model chain" >&2; exit 64; }
  chain_arg=$mapped
fi

if [ -n "${BL_LOOP_TOKEN:-}" ]; then
  # The helper holds a session launch lock through start and receipt persistence.
  job_id=$(python3 "$BL_LOOP_ROOT/scripts/register-loop-worker.py" "$dispatch_bin" "$role" "$prompt_copy" "$out" "$secs" "$title" "$chain_arg")
else
  if [ -n "$chain_arg" ]; then
    job_id=$("$dispatch_bin" start "$role" "$prompt_copy" "$out" --timeout "$secs" --title "$title" --chain "$chain_arg")
  else
    job_id=$("$dispatch_bin" start "$role" "$prompt_copy" "$out" --timeout "$secs" --title "$title")
  fi
fi
rc=$?
[ "$rc" -eq 0 ] || exit "$rc"

status=$("$dispatch_bin" wait "$job_id" --timeout 0)
rc=$?
set -- $status
state=${2:-lost}
model=${4:-}
if [ "$state" = done ] && [ "$rc" -eq 0 ]; then
  printf 'MODEL %s\n' "$model"
else
  printf 'dispatch.sh: every model in the %s chain failed; see %s.log\n' "$role" "$out" >&2
  [ "$rc" -ne 0 ] || rc=1
fi

if [ -n "${DISPATCH_HOME:-}" ]; then
  job_log=$DISPATCH_HOME/jobs/$job_id/log
else
  job_log=$HOME/.local/state/dispatch/jobs/$job_id/log
fi
if [ -f "$job_log" ]; then cat "$job_log" > "$out.log"; else : > "$out.log"; fi
printf '== exit %s %s\n' "$rc" "$(date +%H:%M:%S)" >> "$out.log"
exit "$rc"
