#!/bin/sh
# usage: scripts/dispatch.sh <role> <prompt file> <expected output file> [timeout secs]
# Flash models only, via opencode-go. Tries each model in the role's chain; moves on when
# opencode fails, times out, or leaves no output file. CHAIN="m1 m2" overrides the chain.
role=$1; prompt=$2; out=$3; secs=${4:-1800}
case $role in
  design)   chain="glm-5.3-flash qwen3.8-flash deepseek-v4.1-flash" ;;
  checks)   chain="glm-5.3-flash qwen3.8-flash mimo-v2.6-flash" ;;
  produce)  chain="deepseek-v4.1-flash minimax-m3.1-flash mimo-v2.6-flash" ;;
  verify)   chain="qwen3.8-flash glm-5.3-flash mimo-v2.6-flash" ;;
  light)    chain="deepseek-v4.1-flash glm-5.3-flash qwen3.8-flash minimax-m3.1-flash mimo-v2.6-flash" ;;
  *) echo "dispatch.sh: role must be design, checks, produce, verify or light" >&2; exit 64 ;;
esac
chain=${CHAIN:-$chain}; log=$out.log; : > "$log"
for m in $chain; do
  rm -f "$out"
  echo "== opencode-go/$m $(date +%H:%M:%S)" >> "$log"
  perl -e 'alarm shift; exec @ARGV' "$secs" \
    opencode run --title "$(basename "$out") $m" --thinking --model "opencode-go/$m" "$(cat "$prompt")" >> "$log" 2>&1 < /dev/null
  rc=$?
  echo "== exit $rc $(date +%H:%M:%S)" >> "$log"
  if [ $rc -eq 0 ] && [ -s "$out" ]; then echo "MODEL opencode-go/$m"; exit 0; fi
done
echo "dispatch.sh: every model in the $role chain failed; see $log" >&2; exit 1
