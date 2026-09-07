#!/usr/bin/env bash
set -euo pipefail
# TestAlertDwellPromtool supplies: test rules <fixture>.
[[ "$1" == test && "$2" == rules && -f "$3" ]]
exec docker run --rm --network none --entrypoint /bin/promtool \
  -v "$(dirname "$3"):/test:ro" prom/prometheus:v2.54.0 test rules "/test/$(basename "$3")"
