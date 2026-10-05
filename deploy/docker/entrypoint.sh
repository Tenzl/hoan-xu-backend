#!/bin/bash
set -euo pipefail
if [ "$(id -u)" = 0 ]; then
  mkdir -p /var/data/chrome-profile /var/data/files
  chown hoanxu:hoanxu /var/data /var/data/chrome-profile /var/data/files
  chmod 700 /var/data /var/data/chrome-profile /var/data/files
  exec gosu hoanxu "$0" "$@"
fi

if [ "${REMOTE_BROWSER_ENABLED:-false}" != true ]; then
  exec /app/api
fi
: "${REMOTE_BROWSER_ORIGIN:?Set REMOTE_BROWSER_ORIGIN to the backend HTTPS origin}"
if [ "${CHROME_HEADLESS:-true}" != false ]; then
  echo 'Remote Chrome requires CHROME_HEADLESS=false' >&2
  exit 1
fi
# A persistent profile can retain process locks after a forced container stop.
# This deployment has one instance, so no other Chrome may own these locks.
case "${CHROME_PROFILE:-}" in
  /var/data/*) ;;
  *) echo 'Remote Chrome profile must live under /var/data/' >&2; exit 1 ;;
esac
mkdir -p "$CHROME_PROFILE"
rm -f -- "$CHROME_PROFILE/SingletonLock" "$CHROME_PROFILE/SingletonSocket" "$CHROME_PROFILE/SingletonCookie"
# These are container-local files; stale locks can survive a forced restart.
if [ "$DISPLAY" = ':99' ]; then
  rm -f -- /tmp/.X99-lock /tmp/.X11-unix/X99
fi
# A page inside Chromium must not bypass API authentication by connecting to
# the loopback WebSocket bridge. Only the Go proxy receives these credentials.
export REMOTE_BROWSER_BRIDGE_PASSWORD="$(python3 -c 'import secrets; print(secrets.token_urlsafe(32))')"

pids=()
shutdown() {
  trap - TERM INT EXIT
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
websockify --web=/usr/share/novnc --auth-plugin=BasicHTTPAuth --auth-source="hoanxu:$REMOTE_BROWSER_BRIDGE_PASSWORD" 127.0.0.1:6080 127.0.0.1:5900 &
pids+=("$!")
/app/api &
pids+=("$!")
# Any failed core process stops the container instead of leaving a broken display.
set +e
wait -n "${pids[@]}"
status=$?
if [ "$status" = 0 ]; then status=1; fi
exit "$status"
