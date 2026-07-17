#!/usr/bin/env bash
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 <jq-filter> <image>..." >&2
  exit 2
fi

filter=$1
shift

if [[ ! -f "$filter" ]]; then
  echo "release manifest filter not found: $filter" >&2
  exit 2
fi

for image in "$@"; do
  echo "[verify] $image"
  verified=false
  for ((attempt = 1; attempt <= 5; attempt++)); do
    if docker buildx imagetools inspect --raw "$image" \
      | jq -e -f "$filter" >/dev/null; then
      verified=true
      break
    fi
    if ((attempt < 5)); then
      echo "[verify] attempt $attempt failed; retrying $image" >&2
      sleep 3
    fi
  done
  if [[ "$verified" != true ]]; then
    echo "failed to verify linux/amd64 and linux/arm64 manifest entries: $image" >&2
    exit 1
  fi
done
