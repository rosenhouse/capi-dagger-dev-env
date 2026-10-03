#!/usr/bin/env bash
# Corporate proxy: containers on the Docker bridge may reach the internet only through squid.
# Usage: proxy.sh both|https  (both sets HTTP_PROXY and HTTPS_PROXY; https sets only HTTPS_PROXY)
source "$(dirname "$0")/lib.sh"
MODE=${1:-both}
G=$REPO/examples/greeting
D=$T/devenv
cd "$G" && go build -o "$D" ./cmd/devenv || exit 1
PX=http://172.17.0.1:3128

mkdir -p "$T/squid/log" && chmod 777 "$T/squid/log"
cat >"$T/squid/squid.conf" <<'EOF'
http_port 3128
acl localnet src 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16 127.0.0.0/8
acl SSL_ports port 443
acl CONNECT method CONNECT
http_access deny CONNECT !SSL_ports
http_access allow localnet
http_access deny all
cache deny all
access_log stdio:/var/log/squid/access.log
EOF
docker run -d --name squid --network host -v "$T/squid/squid.conf:/etc/squid/squid.conf:ro" -v "$T/squid/log:/var/log/squid" ubuntu/squid:latest >/dev/null
for _ in $(seq 30); do curl -s -o /dev/null -x http://127.0.0.1:3128 https://registry-1.docker.io/v2/ && break; sleep 2; done
sudo iptables -I DOCKER-USER -i docker0 ! -o docker0 -p tcp -m multiport --dports 80,443 -j REJECT --reject-with tcp-reset
docker pull -q curlimages/curl:8.11.1 >/dev/null
obs "direct from bridge: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -o /dev/null -w '%{http_code}' https://registry-1.docker.io/v2/ 2>&1 | tail -1)"
obs "via squid from bridge: $(docker run --rm curlimages/curl:8.11.1 -sS -m 10 -x $PX -o /dev/null -w '%{http_code}' https://registry-1.docker.io/v2/ 2>&1 | tail -1)"

squid_summary() {
  echo "--- squid: requests by result, method, host"
  awk '{u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); print $4, $6, u}' "$T/squid/log/access.log" | sort | uniq -c | sort -rn | head -40
  obs "squid: $(wc -l <"$T/squid/log/access.log") requests, hosts: $(awk '{u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); print u}' "$T/squid/log/access.log" | sort -u | tr '\n' ' ')"
  obs "squid: denied or failed: $(awk '$4 !~ /TUNNEL\/200|\/200|\/30[0-9]/ {u=$7; sub(/^https?:\/\//,"",u); sub(/[:\/].*/,"",u); print $4" "u}' "$T/squid/log/access.log" | sort | uniq -c | sort -rn | head -10 | tr '\n' ';')"
}

diagnose() { # name
  local L=$G/.devenv/$1
  echo "--- dagger.log tail"; tail -15 "$L/dagger.log"
  [ -d "$L/logs" ] || return
  echo "--- image pull and proxy errors in exported logs"
  grep -rhoE '.{0,160}(proxy|dagger\.local|ErrImagePull|ImagePullBackOff|failed to pull|failed to resolve|x509|connection refused|no such host).{0,160}' "$L/logs" 2>/dev/null | sort | uniq -c | sort -rn | head -25
  grep -A3 usefulErrorMessage "$L/logs/resources.yaml" 2>/dev/null | head -30
}

cd "$G"
if [ "$MODE" = both ]; then
  # A developer who only exports proxy variables in the shell, with the engine Dagger provisions.
  HTTPS_PROXY=$PX HTTP_PROXY=$PX NO_PROXY=localhost,127.0.0.1 run naive timeout 600 "$D" test --name naive
  obs "naive: engine container env proxy: $(docker inspect "$(engine)" --format '{{json .Config.Env}}' 2>/dev/null | grep -o '[A-Za-z_]*PROXY=[^"]*' | tr '\n' ' ')"
  diagnose naive
  docker rm -f "$(engine)" >/dev/null 2>&1
  squid_summary; : >"$T/squid/log/access.log"
fi

# A developer who provisions the engine with the proxy, as Dagger documents.
envs=(-e HTTPS_PROXY=$PX -e https_proxy=$PX -e NO_PROXY=localhost,127.0.0.1 -e no_proxy=localhost,127.0.0.1)
[ "$MODE" = both ] && envs+=(-e HTTP_PROXY=$PX -e http_proxy=$PX)
docker run -d --name dagger-engine-proxy --privileged "${envs[@]}" -v dagger-proxy:/var/lib/dagger registry.dagger.io/engine:v0.21.10 >/dev/null
export _EXPERIMENTAL_DAGGER_RUNNER_HOST=docker-container://dagger-engine-proxy
run informed timeout 1500 "$D" test --name px
obs "informed ($MODE): stages reached: $(grep -o '] [a-z].*' "$T/informed.err" | cut -c3-40 | tr '\n' '|')"
diagnose px
squid_summary
finish
