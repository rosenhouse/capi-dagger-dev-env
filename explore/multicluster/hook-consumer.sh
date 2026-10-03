#!/usr/bin/env bash
# A consumer whose Test hook adds a second cluster and recreates work, then fails so devenv test exports logs.
source "$(dirname "$0")/lib.sh"
install_tools
src=$RUNNER_TEMP/consumer
cp -a "$GREETING" "$src"
rm -rf "$src/.devenv"
cp "$REPO/explore/multicluster/consumer/multi.go.txt" "$src/cmd/devenv/multi.go"
sed -i 's/Test:  greeting,/Test:  multi,/' "$src/cmd/devenv/main.go"
cd "$src" || exit 1
export GOWORK=off
go mod edit -droprequire=github.com/rosenhouse/capi-dagger-dev-env
go get github.com/rosenhouse/capi-dagger-dev-env@2eacde6 >/dev/null 2>&1
go mod tidy
grep capi-dagger-dev-env go.mod
go build -o "$BIN/mdevenv" ./cmd/devenv || { obs "consumer build failed"; summary; exit 1; }

start=$(now)
timeout 2400 "$BIN/mdevenv" test --name mt >"$RUNNER_TEMP/test.log" 2>&1
obs "devenv test with multi-cluster Test hook: exit $? after $(since "$start")s"
grep -E '^\[|OBS|Error|logs:|clean up' "$RUNNER_TEMP/test.log" | tail -40
logs=$src/.devenv/mt/logs
echo "--- exported logs"
find "$logs" -maxdepth 2 | sort | head -60
obs "exported per-cluster log dirs: $(ls "$logs" 2>/dev/null | tr '\n' ' ')"
obs "resources.yaml mentions work2: $(grep -c 'name: work2' "$logs/resources.yaml" 2>/dev/null)"
ls "$src/.devenv/mt"
summary
