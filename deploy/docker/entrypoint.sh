#!/bin/bash
set -euo pipefail
if [ "$(id -u)" = 0 ]; then
  mkdir -p /var/data/files
  chown hoanxu:hoanxu /var/data /var/data/files
  chmod 700 /var/data /var/data/files
  exec gosu hoanxu "$0" "$@"
fi

if [ "${BROWSER_MODE:-local}" != remote ] || [ "${CHROME_SSH_TUNNEL_ENABLED:-true}" != true ]; then
  exec /app/api
fi
# False is only for tests or a separately supervised loopback tunnel.
: "${CHROME_SSH_HOST:?Set the EC2 hostname or Elastic IP}"
: "${CHROME_SSH_PRIVATE_KEY:?Set a dedicated unencrypted SSH private key}"
: "${CHROME_SSH_KNOWN_HOSTS:?Set verified known_hosts contents}"
[[ "$CHROME_SSH_HOST" =~ ^[a-zA-Z0-9][a-zA-Z0-9.-]*$ ]] || { echo 'Invalid SSH host' >&2; exit 1; }
[[ "${CHROME_SSH_USER:-chrome-tunnel}" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo 'Invalid SSH user' >&2; exit 1; }
[[ "${CHROME_SSH_PORT:-22}" =~ ^[0-9]+$ ]] || { echo 'Invalid SSH port' >&2; exit 1; }
[ "${CHROME_REMOTE_URL:-http://127.0.0.1:9222}" = http://127.0.0.1:9222 ] || { echo 'Managed tunnel uses CDP port 9222' >&2; exit 1; }
[ "${REMOTE_BROWSER_UPSTREAM:-http://127.0.0.1:6080}" = http://127.0.0.1:6080 ] || { echo 'Managed tunnel uses display port 6080' >&2; exit 1; }

umask 077
task_ssh_dir=$(mktemp -d /tmp/hoanxu-ssh.XXXXXX)
printf '%s\n' "$CHROME_SSH_PRIVATE_KEY" | tr -d '\r' > "$task_ssh_dir/key"
printf '%s\n' "$CHROME_SSH_KNOWN_HOSTS" | tr -d '\r' > "$task_ssh_dir/known_hosts"
unset CHROME_SSH_PRIVATE_KEY CHROME_SSH_KNOWN_HOSTS
# Reject malformed/encrypted keys. Never print private key contents.
ssh-keygen -y -P '' -f "$task_ssh_dir/key" > /dev/null
ssh-keygen -F "$CHROME_SSH_HOST" -f "$task_ssh_dir/known_hosts" > /dev/null || \
  ssh-keygen -F "[$CHROME_SSH_HOST]:${CHROME_SSH_PORT:-22}" -f "$task_ssh_dir/known_hosts" > /dev/null

tunnel_loop() {
  task_tunnel_pid=''
  task_sleep_pid=''
  tunnel_stop() {
    trap - TERM INT
    [ -z "$task_tunnel_pid" ] || kill "$task_tunnel_pid" 2>/dev/null || true
    [ -z "$task_sleep_pid" ] || kill "$task_sleep_pid" 2>/dev/null || true
    wait 2>/dev/null || true
    exit 0
  }
  trap tunnel_stop TERM INT
  task_delay=2
  while true; do
    task_started=$SECONDS
    ssh -F /dev/null -N -T \
      -i "$task_ssh_dir/key" -p "${CHROME_SSH_PORT:-22}" \
      -o "UserKnownHostsFile=$task_ssh_dir/known_hosts" -o GlobalKnownHostsFile=/dev/null \
      -o StrictHostKeyChecking=yes -o UpdateHostKeys=no \
      -o BatchMode=yes -o IdentitiesOnly=yes -o PasswordAuthentication=no \
      -o KbdInteractiveAuthentication=no -o ExitOnForwardFailure=yes \
      -o ConnectTimeout=10 -o ServerAliveInterval=15 -o ServerAliveCountMax=3 \
      -L 127.0.0.1:9222:127.0.0.1:9222 -L 127.0.0.1:6080:127.0.0.1:6080 \
      "${CHROME_SSH_USER:-chrome-tunnel}@$CHROME_SSH_HOST" &
    task_tunnel_pid=$!
    wait "$task_tunnel_pid" || true
    task_tunnel_pid=''
    if (( SECONDS - task_started > 60 )); then task_delay=2; fi
    echo "Chrome SSH tunnel disconnected; retrying in ${task_delay}s" >&2
    sleep "$task_delay" &
    task_sleep_pid=$!
    wait "$task_sleep_pid" || true
    task_sleep_pid=''
    task_delay=$(( task_delay * 2 ))
    if (( task_delay > 30 )); then task_delay=30; fi
  done
}

task_tunnel_loop_pid=''
task_api_pid=''
shutdown() {
  trap - TERM INT EXIT
  [ -z "$task_api_pid" ] || kill -TERM "$task_api_pid" 2>/dev/null || true
  # Keep CDP available while Go closes its own tabs.
  [ -z "$task_api_pid" ] || wait "$task_api_pid" 2>/dev/null || true
  [ -z "$task_tunnel_loop_pid" ] || kill -TERM "$task_tunnel_loop_pid" 2>/dev/null || true
  [ -z "$task_tunnel_loop_pid" ] || wait "$task_tunnel_loop_pid" 2>/dev/null || true
  rm -f -- "$task_ssh_dir/key" "$task_ssh_dir/known_hosts"
  rmdir "$task_ssh_dir"
}
trap shutdown TERM INT EXIT
tunnel_loop &
task_tunnel_loop_pid=$!
/app/api &
task_api_pid=$!
set +e
wait -n "$task_api_pid" "$task_tunnel_loop_pid"
task_status=$?
exit "$task_status"
