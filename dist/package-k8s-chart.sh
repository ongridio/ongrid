#!/usr/bin/env bash

set -euo pipefail

if [[ "$#" -ne 4 ]]; then
    echo "usage: $0 <chart-dir> <output.tgz> <app-version> <image-tag>" >&2
    exit 2
fi

CHART_DIR="$1"
OUTPUT="$2"
APP_VERSION="$3"
CHART_VERSION="${APP_VERSION#v}"
IMAGE_TAG="$4"

if [[ ! "$CHART_VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "invalid chart version: $CHART_VERSION" >&2
    exit 2
fi
if [[ ! "$IMAGE_TAG" =~ ^[0-9A-Za-z_][0-9A-Za-z_.-]{0,127}$ ]]; then
    echo "invalid image tag: $IMAGE_TAG" >&2
    exit 2
fi
if [[ ! -f "$CHART_DIR/Chart.yaml" ]]; then
    echo "chart metadata missing: $CHART_DIR/Chart.yaml" >&2
    exit 2
fi

tmp_dir="$(mktemp -d)"
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$(dirname "$OUTPUT")" "$tmp_dir/ongrid-edge"
cp -R "$CHART_DIR/." "$tmp_dir/ongrid-edge/"
awk -v chart_version="$CHART_VERSION" -v app_version="$APP_VERSION" '
    /^version:/ { print "version: " chart_version; next }
    /^appVersion:/ { print "appVersion: \"" app_version "\""; next }
    { print }
' "$tmp_dir/ongrid-edge/Chart.yaml" > "$tmp_dir/ongrid-edge/Chart.yaml.tmp"
mv "$tmp_dir/ongrid-edge/Chart.yaml.tmp" "$tmp_dir/ongrid-edge/Chart.yaml"

awk -v image_tag="$IMAGE_TAG" '
    /^image:/ { in_image = 1; print; next }
    in_image && /^  tag:/ { print "  tag: \"" image_tag "\""; in_image = 0; next }
    in_image && /^[^[:space:]]/ { in_image = 0 }
    { print }
' "$tmp_dir/ongrid-edge/values.yaml" > "$tmp_dir/ongrid-edge/values.yaml.tmp"
mv "$tmp_dir/ongrid-edge/values.yaml.tmp" "$tmp_dir/ongrid-edge/values.yaml"

COPYFILE_DISABLE=1 tar -C "$tmp_dir" -czf "$OUTPUT" ongrid-edge
