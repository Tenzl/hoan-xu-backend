#!/bin/bash
set -euo pipefail
curl --noproxy '*' --fail --silent --max-time 3 http://127.0.0.1:9222/json/version > /dev/null
xdpyinfo -display "$DISPLAY" > /dev/null 2>&1
curl --noproxy '*' --fail --silent --max-time 3 http://127.0.0.1:6080/ > /dev/null
