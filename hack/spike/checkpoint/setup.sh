#!/usr/bin/env bash
# Grants the runner user /dev/kvm and installs smolvm, as ci.yml does.
set -euo pipefail
echo 'KERNEL=="kvm", GROUP="kvm", MODE="0666", OPTIONS+="static_node=kvm"' | sudo tee /etc/udev/rules.d/99-kvm4all.rules
sudo udevadm control --reload-rules
sudo udevadm trigger --name-match=kvm
sudo udevadm settle
test -w /dev/kvm
curl -fsSL "https://raw.githubusercontent.com/smol-machines/smolvm/v${SMOLVM_VERSION}/scripts/install.sh" | bash -s -- --version "$SMOLVM_VERSION"
echo "$HOME/.smolvm" >>"$GITHUB_PATH"
"$HOME/.smolvm/smolvm" --version
grep -m1 'model name' /proc/cpuinfo
