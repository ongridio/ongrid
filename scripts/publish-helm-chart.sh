#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 6 ]]; then
  echo "usage: $0 <chart-ref> <version> <package> <push-target> <registry> <username>" >&2
  exit 2
fi

chart_ref=$1
version=$2
package=$3
push_target=$4
registry=$5
username=$6
attempts=${RELEASE_CHART_CHECK_ATTEMPTS:-5}
retry_delay=${RELEASE_CHART_CHECK_RETRY_DELAY:-3}

if ! command -v helm >/dev/null 2>&1; then
  echo "helm is required" >&2
  exit 2
fi
if [[ ! -s "$package" ]]; then
  echo "Helm chart package not found: $package" >&2
  exit 2
fi
if [[ ! $attempts =~ ^[1-9][0-9]*$ || ! $retry_delay =~ ^[0-9]+$ ]]; then
  echo "release chart check retry settings must be non-negative integers and attempts must be positive" >&2
  exit 2
fi

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
error_file="$tmp_dir/helm-show.err"

for ((attempt = 1; attempt <= attempts; attempt++)); do
  if helm show chart "$chart_ref" --version "$version" >/dev/null 2>"$error_file"; then
    echo "[publish] $chart_ref:$version already exists; skipping immutable chart version"
    exit 0
  fi

  if grep -Eiq '(^|[^[:alpha:]])(404|not found)([^[:alpha:]]|$)' "$error_file"; then
    if [[ -z ${CNB_TOKEN:-} ]]; then
      echo "CNB_TOKEN is required to publish a missing Helm chart" >&2
      exit 2
    fi
    echo "[publish] $chart_ref:$version does not exist; publishing"
    printf '%s' "$CNB_TOKEN" | helm registry login "$registry" \
      --username "$username" \
      --password-stdin
    helm push "$package" "$push_target"
    helm show chart "$chart_ref" --version "$version" >/dev/null
    exit 0
  fi

  if ((attempt < attempts)); then
    echo "[publish] chart inspect attempt $attempt failed; retrying $chart_ref:$version" >&2
    sleep "$retry_delay"
  fi
done

cat "$error_file" >&2
echo "[publish] unable to determine immutable chart state: $chart_ref:$version" >&2
exit 1
