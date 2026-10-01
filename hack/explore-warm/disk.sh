#!/usr/bin/env bash
# Where does a restore's disk go, and what does it leave after down or Ctrl-C?
source hack/explore-warm/lib.sh
cpu
watch_data_dirs

used() { df --output=used -BM / | tail -1 | tr -d ' '; }
mark() { touch "$OUT/mark-$1"; say "- $1: disk used $(used)"; }
# new LABEL lists files over 50 MiB written since mark LABEL, with their allocated size.
new() {
	say "- $1 -> now: disk used $(used); new files over 50 MiB:"
	sudo find / -xdev -type f -cnewer "$OUT/mark-$1" -size +50M -printf '%k KiB allocated, %s bytes: %p\n' 2>/dev/null | sort -n | quote
}

mark save
timed save "$D" platform save
new save

mark restore
timed up "$D" up --name d1
vm=$(smolvm machine ls -q | head -1)
dir=$(smolvm machine data-dir --name "$vm")
say "- running restored VM's data dir: $(du -sh "$dir" | cut -f1) allocated, $(du -sh --apparent-size "$dir" | cut -f1) apparent"
new restore
mark down
timed down "$D" down --name d1 --purge
new down
new restore

mark again
timed up-again "$D" up --name d2
timed down-again "$D" down --name d2 --purge
new again

mark interrupted
interrupt create-from "\] restore VM$" 20 "$D" up --name d3
new interrupted
leftovers

mark cold
timed cold-up "$D" up --cold --name d4
vm=$(smolvm machine ls -q | head -1)
dir=$(smolvm machine data-dir --name "$vm")
say "- running cold VM's data dir: $(du -sh "$dir" | cut -f1) allocated, $(du -sh --apparent-size "$dir" | cut -f1) apparent"
timed cold-down "$D" down --name d4 --purge
new cold
sudo du -xsm /home/runner/.cache/* /home/runner/.local/share/* /tmp 2>/dev/null | sort -n | tail -15 | quote
