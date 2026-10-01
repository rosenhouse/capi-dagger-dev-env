#!/usr/bin/env bash
# E2 summary: the hit/miss matrix from the rows every restore job wrote.
set -euo pipefail
. "$(dirname "$0")/lib.sh"
rows=$(cat "$@")
short() { sed -E 's/linux-kvm-intel-portable-v1/intel/g; s/exact-v1-([0-9a-f]{8})[0-9a-f]*/exact:\1/g'; }

summary "### Restores: ok / attempted, by capture host → restore host"
summary ""
summary "| captured on | contract | restored on | contract | predicted | ok / attempted |"
summary "|---|---|---|---|---|---|"
awk -F'\t' '{ k = $1 "\t" $2 "\t" $3 "\t" $4 "\t" $5; n[k]++; if ($6 == "ok") ok[k]++ }
  END { for (k in n) print k "\t" ok[k] + 0 "/" n[k] }' <<<"$rows" | sort | short |
  while IFS=$'\t' read -r cm cc rm rc p r; do summary "| $cm | $cc | $rm | $rc | $p | $r |"; done

summary ""
summary "### Rows where host_contract predicted wrongly"
awk -F'\t' '($5 == "hit") != ($6 == "ok")' <<<"$rows" | short | sed 's/\t/ | /g; s/^/| /; s/$/ |/' |
  while read -r line; do summary "$line"; done

summary ""
summary "### Distinct CPUs"
awk -F'\t' '{ print $1 "\t" $2; print $3 "\t" $4 }' <<<"$rows" | sort -u | short |
  while IFS=$'\t' read -r m c; do summary "- $m ($c)"; done

summary ""
summary "### Timings of successful restores"
awk -F'\t' '$6 == "ok" { n++; c += $7; s += $8; if ($7 > cm) cm = $7; if ($8 > sm) sm = $8 }
  END { if (n) printf "create --from mean %.2f s, max %.2f s; start mean %.2f s, max %.2f s; n=%d\n", c/n, cm, s/n, sm, n }' <<<"$rows" |
  while read -r line; do summary "$line"; done
