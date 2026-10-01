# shellcheck shell=bash
# Helpers shared by the checkpoint spike scripts. Source it.

summary() { echo "$*"; echo "$*" >>"${GITHUB_STEP_SUMMARY:-/dev/null}"; }
measure() { summary "| $1 | $2 |"; }

# since T0: seconds elapsed since $EPOCHREALTIME was T0.
since() { awk -v a="$1" -v b="$EPOCHREALTIME" 'BEGIN { printf "%.2f", b - a }'; }

# timed NAME CMD...: runs CMD and records its wall time.
timed() {
  local name=$1 t=$EPOCHREALTIME rc=0
  shift
  "$@" || rc=$?
  measure "$name" "$(since "$t") s (exit $rc)"
  return $rc
}

guest() { smolvm machine exec --name "$1" --timeout 30s -- sh -c "$2"; }

# clock NAME: prints "host_epoch guest_epoch guest_uptime".
clock() { echo "$(date +%s.%N) $(guest "$1" 'date +%s; cut -d" " -f1 /proc/uptime' | tr '\n' ' ')"; }
diff_s() { awk -v a="$1" -v b="$2" 'BEGIN { printf "%.1f s", b - a }'; }

# probe PORT: prints what the guest server sends; empty if nothing answers.
# A bare connect succeeds even with no guest listener, so read the payload.
probe() { timeout 5 bash -c "exec 3<>/dev/tcp/127.0.0.1/$1 && cat <&3" 2>/dev/null || true; }

cpu_model() { awk -F': *' '/^model name/ { print $2; exit }' /proc/cpuinfo; }

# host_info: runner image, kernel, microcode and Azure VM size.
host_info() {
  local size
  size=$(curl -fsS -m 2 -H Metadata:true 'http://169.254.169.254/metadata/instance/compute/vmSize?api-version=2021-02-01&format=text' 2>/dev/null) || size=unknown
  echo "image ${ImageVersion:-?}, kernel $(uname -r), $(grep -m1 microcode /proc/cpuinfo | tr -d '\t'), vm size $size"
}

# host_contract [CPUINFO]: smolvm's checkpoint CPU contract for this host.
# Mirrors checkpoint_cpu_contract() and cpu_fingerprint() in smolvm v1.22.0
# src/portable_checkpoint.rs.
host_contract() {
  local identity
  identity=$(printf 'platform=linux/amd64\n'
    awk '
      BEGIN { n = split("vendor_id|cpu family|model|stepping|flags|Features|CPU implementer|CPU architecture|CPU variant|CPU part|CPU revision", a, "|"); for (i = 1; i <= n; i++) ok[a[i]] = 1 }
      /^[ \t]*$/ { exit }
      { i = index($0, ":"); if (!i) next
        k = substr($0, 1, i - 1); v = substr($0, i + 1)
        gsub(/^[ \t]+|[ \t]+$/, "", k); if (!(k in ok)) next
        gsub(/[ \t]+/, " ", v); sub(/^ /, "", v); sub(/ $/, "", v)
        print k "=" v }' "${1:-/proc/cpuinfo}")
  if grep -qx vendor_id=GenuineIntel <<<"$identity"; then
    echo linux-kvm-intel-portable-v1
  else
    echo "exact-v1-$(sha256sum <<<"$identity" | cut -d' ' -f1)"
  fi
}

# manifest FILE: prints a checkpoint file's manifest JSON.
# The last 64 bytes are a footer whose manifest_size is a little-endian u64 at
# offset 44; the manifest sits just before the footer (smolvm FORMAT.md §3.1).
manifest() {
  local size
  size=$(tail -c 64 "$1" | od -An -tu8 -j44 -N8 --endian=little | tr -d ' ')
  tail -c $((size + 64)) "$1" | head -c "$size"
}

# file_contract FILE: the checkpoint's recorded contract, in host_contract's form.
file_contract() {
  manifest "$1" | jq -r '.checkpoint.cpu_contract | if .kind == "exact-v1" then "exact-v1-" + .fingerprint else .kind end'
}

# boot_tiny NAME PORT: a 1-vCPU bare VM that counts seconds in RAM and serves
# "<counter pid> <count>" on guest port 8080, published on host PORT.
boot_tiny() {
  smolvm machine create --name "$1" --net --net-backend virtio-net --cpus 1 --mem 512 \
    --storage 2 --overlay 1 -p "$2:8080"
  timed "$1: first start (bare, --branchable)" smolvm machine start --name "$1" --branchable
  smolvm machine exec --name "$1" -d -- sh -c 'i=0; while :; do i=$((i+1)); echo "$$ $i" >/dev/shm/n; sleep 1; done'
  smolvm machine exec --name "$1" -d -- nc -lk -p 8080 -e cat /dev/shm/n
}

# wait_serving PORT: waits up to 30 s for the counter on PORT.
wait_serving() {
  for _ in $(seq 30); do
    [[ $(probe "$1") =~ ^[0-9]+\ [0-9]+$ ]] && return 0
    sleep 1
  done
  return 1
}

# checkpoint NAME FILE: captures NAME to FILE and records size and times.
checkpoint() {
  local out
  out=$(timed "$1: checkpoint" env RUST_LOG=smolvm=info smolvm machine checkpoint --name "$1" -o "$2" 2>"$2.log") || {
    echo "$out"; cat "$2.log"; return 1
  }
  echo "$out"
  measure "$1: checkpoint size" "$(stat -c %s "$2") bytes"
  measure "$1: checkpoint source pause" "$(grep -o '[0-9.]*s source pause' <<<"$out")"
  sed -E 's/\x1b\[[0-9;]*m//g' "$2.log" | grep 'phase completed' |
    sed -E 's/.*phase="?([a-z_]+)"? elapsed_ms=([0-9]+).*/\1 \2/' |
    while read -r phase ms; do measure "$1: $phase" "$ms ms"; done
}
