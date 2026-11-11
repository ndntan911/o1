#!/usr/bin/env bash
# Poll a URL until it answers 2xx or the timeout expires.
#
#   healthcheck.sh URL [TIMEOUT_SECONDS] [INTERVAL_SECONDS]
#
# Defaults are 300 seconds and 5 seconds. Exits 0 only after a 2xx response.
set -euo pipefail

url=${1:?usage: healthcheck.sh URL [TIMEOUT_SECONDS] [INTERVAL_SECONDS]}
timeout=${2:-300}
interval=${3:-5}
deadline=$((SECONDS + timeout))

until curl -fsS --max-time 5 "$url" >/dev/null 2>&1; do
  (( SECONDS < deadline )) || { echo "healthcheck: $url not ready after ${timeout}s" >&2; exit 1; }
  sleep "$interval"
done
