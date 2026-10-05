#!/bin/sh
# Render does not provide privileged user namespaces for Chromium's OS sandbox.
# Chromium runs as the unprivileged service user inside the container.
exec /usr/bin/chromium --no-sandbox --disable-dev-shm-usage --window-size=1440,900 "$@"
