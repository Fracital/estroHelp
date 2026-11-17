#!/usr/bin/env bash
# Install the CI deploy gate on the production host `chat` (run as root there, from a checkout):
#   scripts/gate-install.sh '<ssh-ed25519 public key of the CHAT_DEPLOY_KEY secret>'
# The deploy job runs on the self-hosted runner divide-deploy (CT 204 on divide, 172.16.5.204):
# it builds, then runs `ssh root@172.16.5.64 deploy <sha> < artifacts.tar.gz`. The key below may
# only run the gate, only from the runner's address.
set -euo pipefail
PUB="${1:?public key}"
HERE="$(cd "$(dirname "$0")/.." && pwd)"
[[ $PUB =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+ ]] || { echo "expected an ssh-ed25519 public key" >&2; exit 64; }

install -m 755 "$HERE/deploy/estro-deploy" /usr/local/sbin/estro-deploy
install -m 755 "$HERE/deploy/estro-deploy-gate" /usr/local/sbin/estro-deploy-gate
install -d -m 700 /var/lib/estro-deploy

line="restrict,command=\"/usr/local/sbin/estro-deploy-gate\",from=\"172.16.5.204\" $PUB estrohelp-ci-deploy"
install -d -m 700 /root/.ssh
touch /root/.ssh/authorized_keys
grep -v 'estrohelp-ci-deploy$' /root/.ssh/authorized_keys > /root/.ssh/authorized_keys.new || true
echo "$line" >> /root/.ssh/authorized_keys.new
chmod 600 /root/.ssh/authorized_keys.new
mv /root/.ssh/authorized_keys.new /root/.ssh/authorized_keys

# The old setup (a runner on this host with sudo to estro-deploy) is retired.
rm -f /etc/sudoers.d/estro-runner
echo "gate installed; deploy key pinned to /usr/local/sbin/estro-deploy-gate from 172.16.5.204"
