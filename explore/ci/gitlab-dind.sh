#!/usr/bin/env bash
# GitLab CI's docker:dind pattern: the job container reaches a privileged dind service at tcp://docker:2375.
# The job image has only the docker CLI and a static devenv binary, as a GitLab job image might.
source "$(dirname "$0")/lib.sh"
G=$REPO/examples/greeting
(cd "$G" && CGO_ENABLED=0 go build -o "$T/devenv-static" ./cmd/devenv) || exit 1
docker network create gl >/dev/null
docker run -d --privileged --name docker --network gl --network-alias docker -e DOCKER_TLS_CERTDIR= docker:29-dind >/dev/null
for _ in $(seq 30); do docker run --rm --network gl -e DOCKER_HOST=tcp://docker:2375 docker:29-cli docker info >/dev/null 2>&1 && break; sleep 2; done
obs "dind: $(docker run --rm --network gl -e DOCKER_HOST=tcp://docker:2375 docker:29-cli docker info --format 'cgroup {{.CgroupVersion}} driver {{.Driver}}' 2>&1)"

job() { # label CMD
  local label=$1; shift
  run "$label" docker run --rm --network gl -e DOCKER_HOST=tcp://docker:2375 \
    -v "$REPO:/builds/repo" -v "$T/devenv-static:/usr/local/bin/devenv:ro" \
    -w /builds/repo/examples/greeting docker:29-cli sh -c "$*"
}
job gitlab-test 'timeout 1800 devenv test --name gl'
obs "gitlab-test: stages: $(grep -o '] [a-z].*' "$T/gitlab-test.err" | cut -c3-36 | tr '\n' '|')"
L=$G/.devenv/gl
echo "--- dagger.log tail"; sudo tail -20 "$L/dagger.log"
sudo grep -rhoE '.{0,120}(cgroup|failed|Error).{0,120}' "$L/logs" 2>/dev/null | sort | uniq -c | sort -rn | head -20
docker run --rm --network gl -e DOCKER_HOST=tcp://docker:2375 docker:29-cli docker ps -a --format '{{.Names}} {{.Image}} {{.Status}}'
finish
