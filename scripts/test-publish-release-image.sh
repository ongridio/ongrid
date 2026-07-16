#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
publisher="$repo_root/scripts/publish-release-image.sh"
filter="$repo_root/scripts/release-manifest-platforms.jq"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/bin"
export PATH="$tmp_dir/bin:$PATH"
export DOCKER_FAKE_COUNT="$tmp_dir/docker-count"
export BUILD_MARKER="$tmp_dir/build-marker"
export RELEASE_IMAGE_CHECK_ATTEMPTS=2
export RELEASE_IMAGE_CHECK_RETRY_DELAY=0

cat >"$tmp_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 5 || $1 != buildx || $2 != imagetools || $3 != inspect || $4 != --raw ]]; then
  echo "unexpected fake docker invocation: $*" >&2
  exit 2
fi

count=0
if [[ -f "$DOCKER_FAKE_COUNT" ]]; then
  count=$(<"$DOCKER_FAKE_COUNT")
fi
count=$((count + 1))
printf '%s\n' "$count" >"$DOCKER_FAKE_COUNT"

case "$DOCKER_FAKE_MODE" in
  present)
    printf '%s\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}'
    ;;
  partial)
    printf '%s\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}'
    ;;
  missing)
    echo "ERROR: example.invalid/release:v1.0.0: not found" >&2
    exit 1
    ;;
  transient-then-present)
    if [[ $count -eq 1 ]]; then
      echo 'ERROR: registry request failed: EOF' >&2
      exit 1
    fi
    printf '%s\n' '{"manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}'
    ;;
  transient)
    echo 'ERROR: registry request failed: EOF' >&2
    exit 1
    ;;
  *)
    echo "unknown DOCKER_FAKE_MODE: $DOCKER_FAKE_MODE" >&2
    exit 2
    ;;
esac
EOF
chmod +x "$tmp_dir/bin/docker"

run_publisher() {
  bash "$publisher" example.invalid/release:v1.0.0 "$filter" -- \
    bash -c 'printf built >"$BUILD_MARKER"'
}

reset_case() {
  rm -f "$DOCKER_FAKE_COUNT" "$BUILD_MARKER"
}

reset_case
export DOCKER_FAKE_MODE=present
run_publisher
test ! -e "$BUILD_MARKER"

reset_case
export DOCKER_FAKE_MODE=missing
run_publisher
test -s "$BUILD_MARKER"

reset_case
export DOCKER_FAKE_MODE=partial
if run_publisher; then
  echo "partial immutable manifest unexpectedly passed" >&2
  exit 1
fi
test ! -e "$BUILD_MARKER"

reset_case
export DOCKER_FAKE_MODE=transient-then-present
run_publisher
test "$(<"$DOCKER_FAKE_COUNT")" -eq 2
test ! -e "$BUILD_MARKER"

reset_case
export DOCKER_FAKE_MODE=transient
if run_publisher; then
  echo "persistent registry error unexpectedly triggered a build" >&2
  exit 1
fi
test ! -e "$BUILD_MARKER"
