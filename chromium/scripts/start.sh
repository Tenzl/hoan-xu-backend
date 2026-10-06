#!/bin/bash
set -euo pipefail
: "${REMOTE_BROWSER_BRIDGE_PASSWORD:?Set the shared noVNC bridge password}"
# BasicHTTPAuth uses ':' as a delimiter; forbid whitespace/newlines as well.
[[ "$REMOTE_BROWSER_BRIDGE_PASSWORD" =~ ^[a-zA-Z0-9_-]{32,}$ ]] || { echo 'Bridge password must be 32+ URL-safe characters' >&2; exit 1; }
if [ "$(id -u)" = 0 ]; then
  mkdir -p "$CHROME_PROFILE"
  chown chromium:chromium "$CHROME_PROFILE"
  chmod 700 "$CHROME_PROFILE"
  exec gosu chromium "$0" "$@"
fi

# Hold an OS lock for this entire supervisor lifetime. Never delete Chrome's
# Singleton locks blindly: another browser could still own the profile.
exec 9>"$CHROME_PROFILE/.supervisor.lock"
flock -n 9 || { echo 'Chrome profile already in use' >&2; exit 1; }

pids=()
chrome_pid=''
shutdown() {
  trap - TERM INT EXIT
  if [ -n "$chrome_pid" ]; then
    kill -TERM "$chrome_pid" 2>/dev/null || true
    # Save the profile while the display and window manager remain available.
    wait "$chrome_pid" 2>/dev/null || true
  fi
  if [ "${#pids[@]}" -gt 0 ]; then
    kill -TERM "${pids[@]}" 2>/dev/null || true
    wait "${pids[@]}" 2>/dev/null || true
  fi
}
trap shutdown TERM INT EXIT

Xvfb "$DISPLAY" -screen 0 1440x900x24 -nolisten tcp &
pids+=("$!")
for attempt in {1..50}; do
  if xdpyinfo -display "$DISPLAY" > /dev/null 2>&1; then break; fi
  sleep 0.1
done
xdpyinfo -display "$DISPLAY" > /dev/null
openbox --sm-disable &
pids+=("$!")
x11vnc -display "$DISPLAY" -localhost -rfbport 5900 -forever -shared -nopw -noxdamage -quiet &
pids+=("$!")
websockify --web=/usr/share/novnc --auth-plugin=BasicHTTPAuth \
  --auth-source="hoanxu:$REMOTE_BROWSER_BRIDGE_PASSWORD" 0.0.0.0:6080 127.0.0.1:5900 &
pids+=("$!")

# Chromium itself listens on loopback, even on builds that ignore debugging-
# address flags. Relay only inside the isolated container; host publish is local.
socat TCP-LISTEN:9222,bind=0.0.0.0,reuseaddr,fork TCP:127.0.0.1:9223 &
pids+=("$!")
/usr/bin/chromium \
  --remote-debugging-address=127.0.0.1 --remote-debugging-port=9223 \
  --user-data-dir="$CHROME_PROFILE" --window-size=1440,900 \
  --no-first-run --no-default-browser-check \
  https://affiliate.shopee.vn/dashboard &
chrome_pid=$!
set +e
wait -n "$chrome_pid" "${pids[@]}"
status=$?
if [ "$status" = 0 ]; then status=1; fi
exit "$status"
