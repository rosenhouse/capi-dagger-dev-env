#!/usr/bin/env bash
# Save twice, follow the README, save while an environment is up, stop and start a restored VM, change a pinned input.
source hack/explore-warm/lib.sh
cpu
watch_data_dirs

note "platform save twice"
timed save1 "$D" platform save
timed save2 "$D" platform save
leftovers

note "README as written"
timed readme-up go run ./cmd/devenv up
timed readme-status go run ./cmd/devenv status
go run ./cmd/devenv kubeconfig --cluster workload >"$OUT/workload.kubeconfig"
timed readme-kubectl kubectl --kubeconfig "$OUT/workload.kubeconfig" get nodes
timed readme-redeploy go run ./cmd/devenv redeploy
vm=$(smolvm machine ls -q | head -1)
dir=$(smolvm machine data-dir --name "$vm")
say "- restored VM's data dir: $(du -sh "$dir" | cut -f1) on disk, $(du -sh --apparent-size "$dir" | cut -f1) apparent"
du -sh "$dir"/* 2>&1 | quote
leftovers
name=$(ls .devenv | head -1)

note "platform save --force while $name is up"
free -m | quote
(sleep 150; { echo "status during the save:"; "$D" status; free -m; } >"$OUT/during-save.txt" 2>&1) &
timed save-while-up "$D" platform save --force
wait
quote <"$OUT/during-save.txt"
timed after-save-pods kubectl --kubeconfig ".devenv/$name/mgmt.kubeconfig" get pods -A
timed after-save-redeploy go run ./cmd/devenv redeploy
leftovers

note "Stop and start the restored VM"
timed stop smolvm machine stop --name "$vm"
timed start smolvm machine start --name "$vm"
timed stopped-status go run ./cmd/devenv status
timed stopped-kubectl timeout 60 kubectl --request-timeout=10s --kubeconfig ".devenv/$name/mgmt.kubeconfig" get nodes
timed stopped-redeploy timeout 300 go run ./cmd/devenv redeploy
timed stopped-up timeout 900 go run ./cmd/devenv up

note "down --purge"
timed down go run ./cmd/devenv down --purge
timed down-status go run ./cmd/devenv status
leftovers

note "A changed platform input"
sed -i 's/fs.inotify.max_user_instances=8192/fs.inotify.max_user_instances=8191/' internal/devenv/infra/infra.go
go build -o "$RUNNER_TEMP/devenv-changed" ./cmd/devenv
git checkout internal/devenv/infra/infra.go
say "- key $("$D" platform key) became $("$RUNNER_TEMP/devenv-changed" platform key)"
interrupt changed-up 'platform: (cold start|restoring)' 2 "$RUNNER_TEMP/devenv-changed" up --name changed
timed changed-down "$D" down --name changed --purge
leftovers
