#!/bin/bash
set -euo pipefail
: "${SSH_FIXTURE_HOST:?Disposable Docker fixture hostname required}"
mkdir -p /fixture
if [ ! -f /fixture/identity ]; then
  ssh-keygen -q -t ed25519 -N '' -f /fixture/identity
  ssh-keygen -q -t ed25519 -N '' -f /fixture/wrongkey
  ssh-keygen -q -t ed25519 -N '' -f /fixture/hostkey
fi
printf 'restrict,port-forwarding,permitopen="127.0.0.1:9222",permitopen="127.0.0.1:6080" %s\n' \
  "$(cat /fixture/identity.pub)" > /home/chrome-tunnel/.ssh/authorized_keys
chown -R chrome-tunnel:chrome-tunnel /home/chrome-tunnel/.ssh
chmod 700 /home/chrome-tunnel/.ssh
chmod 600 /home/chrome-tunnel/.ssh/authorized_keys
printf '[%s]:2222 %s\n' "$SSH_FIXTURE_HOST" "$(cat /fixture/hostkey.pub)" > /fixture/known_hosts
printf '[%s]:2222 %s\n' "$SSH_FIXTURE_HOST" "$(cat /fixture/wrongkey.pub)" > /fixture/wrong_hosts
cat > /fixture/sshd_config <<'CONFIG'
Port 2222
HostKey /fixture/hostkey
AuthorizedKeysFile .ssh/authorized_keys
PasswordAuthentication no
KbdInteractiveAuthentication no
UsePAM no
AllowUsers chrome-tunnel
AllowTcpForwarding local
AllowStreamLocalForwarding no
PermitOpen 127.0.0.1:9222 127.0.0.1:6080
PermitTTY no
MaxSessions 0
X11Forwarding no
AllowAgentForwarding no
CONFIG
exec /usr/sbin/sshd -D -e -f /fixture/sshd_config
