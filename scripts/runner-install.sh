#!/usr/bin/env bash
# Install the self-hosted GitHub Actions runner on the production host (run as root there):
#   scripts/runner-install.sh <registration-token>
# Get the token from GitHub: repo Settings > Actions > Runners > New self-hosted runner
# (Linux x64), or `gh api -X POST repos/y9x/estrohelp/actions/runners/registration-token -q .token`
# with an account that administers the repo. Without a token the script prepares
# everything except the registration, which can be re-run later.
set -euo pipefail
TOKEN="${1:-}"
REPO_URL="${REPO_URL:-https://github.com/y9x/estrohelp}"
DIR=/opt/actions-runner
HERE="$(cd "$(dirname "$0")/.." && pwd)"

id runner >/dev/null 2>&1 || useradd -r -m -d "$DIR" -s /bin/bash runner
install -d -o runner -g runner -m 755 "$DIR"

install -m 755 "$HERE/deploy/estro-deploy" /usr/local/sbin/estro-deploy
printf 'runner ALL=(root) NOPASSWD: /usr/local/sbin/estro-deploy\n' > /etc/sudoers.d/estro-runner
chmod 440 /etc/sudoers.d/estro-runner
visudo -cf /etc/sudoers.d/estro-runner >/dev/null

if [ ! -x "$DIR/run.sh" ]; then
  VER=$(curl -fsSL https://api.github.com/repos/actions/runner/releases/latest | python3 -c 'import json,sys; print(json.load(sys.stdin)["tag_name"].lstrip("v"))')
  curl -fsSL -o /tmp/runner.tgz "https://github.com/actions/runner/releases/download/v${VER}/actions-runner-linux-x64-${VER}.tar.gz"
  su -s /bin/bash runner -c "tar xzf /tmp/runner.tgz -C $DIR"
  rm -f /tmp/runner.tgz
  "$DIR/bin/installdependencies.sh" >/dev/null 2>&1 || true
fi

if [ -z "$TOKEN" ]; then
  echo "runner files are in $DIR; re-run with a registration token to register and start it"
  exit 0
fi
cd "$DIR"
su -s /bin/bash runner -c "./config.sh --unattended --replace --url '$REPO_URL' --token '$TOKEN' --name chat --labels chat --work _work"
./svc.sh install runner
./svc.sh start
./svc.sh status | tail -2
