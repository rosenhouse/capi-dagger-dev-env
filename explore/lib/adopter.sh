#!/usr/bin/env bash
# Usage: adopter.sh SRC_DIR DEST_DIR
# Copies a synthetic consumer repo to DEST_DIR, makes it a Git repo, and requires the tool
# from this checkout, as an adopter's `go get github.com/rosenhouse/capi-dagger-dev-env@main` would.
set -euo pipefail
src=$1; dest=$2
tool=$(cd "$(dirname "$0")/../.." && pwd)
mkdir -p "$dest"
cp -a "$src"/. "$dest"/
cd "$dest"
git init -q && git add -A && git -c user.email=x@example.com -c user.name=x commit -qm init
export GOWORK=off
go mod edit -require=github.com/rosenhouse/capi-dagger-dev-env@v0.0.0-00010101000000-000000000000 \
  -replace=github.com/rosenhouse/capi-dagger-dev-env="$tool"
go mod tidy
