#!/usr/bin/env bash
# timings.sh NAME=LOG... prints a Markdown table of the stage durations in devenv's progress logs, a column per log.
# Rows after the first-party phase's last stage belong to a redeploy. If LOG.seconds exists, it holds the command's total.
# Notes below the table say which way each log brought up the platform, and count container restarts.
set -euo pipefail
re='^(\[[a-z]+\] )?\[ *([0-9.]+)s\] (.+): ([0-9.]+)s$'
declare -A cell
rows=()
names=()
add() { # row column value
	[[ -v "seen[$1]" ]] || { seen[$1]=1; rows+=("$1"); }
	cell[$1/$2]=$3
}
declare -A seen
for arg in "$@"; do
	name=${arg%%=*} log=${arg#*=}
	names+=("$name")
	[ -f "$log" ] || continue
	prefix=
	while IFS= read -r line; do
		[[ $line =~ $re ]] || continue
		add "$prefix${BASH_REMATCH[3]}" "$name" "${BASH_REMATCH[4]}"
		if [ "${BASH_REMATCH[3]}" = "workload packages" ]; then
			add "**up**" "$name" "**${BASH_REMATCH[2]}**"
			prefix="redeploy: "
		fi
	done <"$log"
	[ ! -f "$log.seconds" ] || cell[total/$name]="**$(cat "$log.seconds")**"
done
rows+=(total)
header="| Stage |" rule="| --- |"
for name in "${names[@]}"; do header+=" $name |" rule+=" ---: |"; done
echo "$header"
echo "$rule"
for row in "${rows[@]}"; do
	line="| $([ "$row" = total ] && echo '**total**' || echo "$row") |"
	for name in "${names[@]}"; do line+=" ${cell[$row/$name]:-} |"; done
	echo "$line"
done
echo
for arg in "$@"; do
	name=${arg%%=*} log=${arg#*=}
	[ -f "$log" ] || continue
	sed -nE 's/^(\[[a-z]+\] )?\[ *[0-9.]+s\] (platform: .*|container restarts: .*|zero-filled .*|checkpoint: .*|the restored VM .*)$/\2/p' "$log" | sed "s/^/- $name: /"
done
