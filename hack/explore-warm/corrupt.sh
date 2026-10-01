#!/usr/bin/env bash
# Up with a truncated checkpoint, a corrupt one, and one whose CPU contract is another host's.
source hack/explore-warm/lib.sh
cpu
watch_data_dirs

timed save "$D" platform save
ckpt=$(ls "$CACHE"/*.checkpoint)
good=$RUNNER_TEMP/good.checkpoint
cp --sparse=always "$ckpt" "$good"
leftovers

note "Truncated checkpoint"
truncate -s 1G "$ckpt"
timed truncated-up timeout 1200 "$D" up --name t1
ls -la .devenv/t1/logs/restore 2>&1 | quote
leftovers
timed truncated-down "$D" down --name t1 --purge
interrupt truncated-again 'platform: (cold start|restoring)' 1 "$D" up --name t1b
timed truncated-again-down "$D" down --name t1b --purge

note "Checkpoint with 64 MiB of random bytes 1.5 GiB in"
cp --sparse=always "$good" "$ckpt"
dd if=/dev/urandom of="$ckpt" bs=1M count=64 seek=1536 conv=notrunc status=none
timed corrupt-up timeout 1500 "$D" up --name t2
if [ -f .devenv/t2/mgmt.kubeconfig ]; then
	timed corrupt-pods kubectl --request-timeout=10s --kubeconfig .devenv/t2/mgmt.kubeconfig get pods -A
	timed corrupt-redeploy timeout 300 "$D" redeploy --name t2
fi
timed corrupt-down "$D" down --name t2 --purge
leftovers

note "Checkpoint whose manifest names another CPU fingerprint"
cp --sparse=always "$good" "$ckpt"
python3 - "$ckpt" <<'EOF'
import os, sys
path = sys.argv[1]
with open(path, "r+b") as f:
    size = os.path.getsize(path)
    start = max(0, size - (16 << 20))
    f.seek(start)
    tail = f.read()
    i = tail.find(b'"fingerprint"')
    j = tail.index(b'"', tail.index(b':', i) + 1) + 1
    old = tail[j:j + 1]
    new = b"0" if old != b"0" else b"1"
    f.seek(start + j)
    f.write(new)
    print("fingerprint starts with", tail[j:j + 12], "now", new)
EOF
timed contract-up timeout 1200 "$D" up --name t3
timed contract-down "$D" down --name t3 --purge
leftovers
