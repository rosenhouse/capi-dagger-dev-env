#!/bin/bash
# Surprising sequences a controller developer might try.
source hack/explore/lib.sh
D=(go run ./cmd/devenv)
cache=$HOME/.cache/devenv

say "S1: Ctrl-C during the first VM start on a fresh runner"
bg s1 "${D[@]}" up --name x
waitlog s1 '\] VM$' 600
sleep 6
procs
kill -INT -- "-$PGID"
finish s1
machines; procs
ls -la .devenv/x; ls -la "$cache/downloads/sha256" "$cache"

say "S2: Ctrl-C while the management cluster comes up, then up again"
bg s2 "${D[@]}" up --name x
waitlog s2 '\] management cluster$' 600
sleep 20
kill -INT -- "-$PGID"
finish s2
machines; procs
ls -la .devenv/x .devenv/x/logs 2>&1; tail -5 .devenv/x/guest.log

say "S3: terminal closed (SIGHUP) mid bring-up"
bg s3 "${D[@]}" up --name h
waitlog s3 '\] registry$' 600
sleep 3
kill -HUP -- "-$PGID"
finish s3
sleep 5
machines; procs
run "${D[@]}" status
run "${D[@]}" up --name h
run "${D[@]}" kubeconfig --name h
run "${D[@]}" down --name h --purge
machines

say "S4: full up, with concurrent commands on the same name"
bg s4 "${D[@]}" up --name x
waitlog s4 '\] VM$' 600
run "${D[@]}" up --name x
run "${D[@]}" test --name x
run "${D[@]}" down --name x
run "${D[@]}" status
waitlog s4 '\] cluster api$' 900
run "${D[@]}" kubeconfig --name x
waitlog s4 'is up\.|Error' 900
finish s4
machines

say "S5: up again while up"
run "${D[@]}" up --name x
ls -la .devenv/x

say "S6: down of a missing environment, then status with several"
run "${D[@]}" down --name nope
ls -la .devenv/nope
run "${D[@]}" status

say "S7: kubeconfig without --name, and kubectl through it"
"${D[@]}" kubeconfig --cluster workload >"$L/w.kubeconfig"; echo "[kubeconfig exit $?]"
run kubectl --kubeconfig "$L/w.kubeconfig" get nodes -o wide
run kubectl --kubeconfig .devenv/x/mgmt.kubeconfig get clusters -A

say "S8: redeploy after a code change"
M=.devenv/x/mgmt.kubeconfig W=.devenv/x/workload.kubeconfig
kubectl --kubeconfig "$M" apply -f - <<'EOF'
apiVersion: demo.example.com/v1alpha1
kind: Greeting
metadata: {name: dev, namespace: default}
spec: {clusterName: work, message: hi}
EOF
hello() { kubectl --kubeconfig "$W" get --raw /api/v1/namespaces/default/services/http:dev-proxy:80/proxy/ 2>&1; }
s=$SECONDS; until out=$(hello) && [[ $out == hi* ]]; do [ $((SECONDS - s)) -gt 300 ] && break; sleep 3; done
echo "before ($((SECONDS - s))s): $out"
sed -i 's|"%s (hello %s)\\n"|"%s [EDITED hello %s]\\n"|' cmd/hello/main.go
git diff --stat
run "${D[@]}" redeploy --name x
s=$SECONDS; until out=$(hello) && [[ $out == *EDITED* ]]; do [ $((SECONDS - s)) -gt 300 ] && break; sleep 3; done
echo "after ($((SECONDS - s))s after redeploy returned): $out"
run kubectl --kubeconfig "$W" get pods -A -o wide

say "S9: redeploy without a change, and with a compile error"
run "${D[@]}" redeploy --name x
echo 'func broken() {' >>cmd/hello/main.go
run "${D[@]}" redeploy --name x
git checkout cmd/hello/main.go
echo "hello still serves: $(hello)"

say "S10: up from a subdirectory"
(cd internal && bg s10 go run ../cmd/devenv up --name sub; echo "$BGPID $PGID" >"$L/s10.ids")
read -r BGPID PGID <"$L/s10.ids"
waitlog s10 '\] registry$' 600
run "${D[@]}" status
run "${D[@]}" down --name sub
machines
(cd internal && run go run ../cmd/devenv status)
kill -INT -- "-$PGID"
sleep 30
cat "$L/s10.log"
machines
git status --porcelain --ignored

say "S11: down, then kubeconfig and redeploy while down"
run "${D[@]}" down --name x
run "${D[@]}" kubeconfig --name x
run "${D[@]}" redeploy --name x
run "${D[@]}" status
run "${D[@]}" down --purge
run "${D[@]}" down --purge --name x
run "${D[@]}" down --purge --name nope

say "Leftovers"
machines; procs
ls -la .devenv "$cache"
du -sh "$cache" ~/.local/share/smolvm 2>/dev/null
