#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
publisher="$repo_root/scripts/publish-helm-chart.sh"
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

mkdir -p "$tmp_dir/bin"
touch "$tmp_dir/chart.tgz"
printf 'chart' >"$tmp_dir/chart.tgz"
export PATH="$tmp_dir/bin:$PATH"
export HELM_FAKE_COUNT="$tmp_dir/helm-count"
export HELM_PUSH_MARKER="$tmp_dir/push-marker"
export HELM_LOGIN_MARKER="$tmp_dir/login-marker"
export RELEASE_CHART_CHECK_ATTEMPTS=2
export RELEASE_CHART_CHECK_RETRY_DELAY=0
export CNB_TOKEN=test-token

cat >"$tmp_dir/bin/helm" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail

if [[ $1 == show && $2 == chart ]]; then
  count=0
  [[ -f "$HELM_FAKE_COUNT" ]] && count=$(<"$HELM_FAKE_COUNT")
  count=$((count + 1))
  printf '%s\n' "$count" >"$HELM_FAKE_COUNT"
  case "$HELM_FAKE_MODE" in
    present) exit 0 ;;
    missing)
      if [[ -f "$HELM_PUSH_MARKER" ]]; then exit 0; fi
      echo 'Error: chart version: not found' >&2
      exit 1
      ;;
    transient-then-present)
      if [[ $count -eq 1 ]]; then
        echo 'Error: registry request failed: EOF' >&2
        exit 1
      fi
      exit 0
      ;;
    transient)
      echo 'Error: registry request failed: EOF' >&2
      exit 1
      ;;
  esac
fi
if [[ $1 == registry && $2 == login ]]; then
  test "$(cat)" = test-token
  printf login >"$HELM_LOGIN_MARKER"
  exit 0
fi
if [[ $1 == push ]]; then
  printf pushed >"$HELM_PUSH_MARKER"
  exit 0
fi
echo "unexpected fake helm invocation: $*" >&2
exit 2
EOF
chmod +x "$tmp_dir/bin/helm"

run_publisher() {
  bash "$publisher" \
    oci://example.invalid/ongrid-edge 0.10.1 "$tmp_dir/chart.tgz" \
    oci://example.invalid example.invalid cnb
}
reset_case() {
  rm -f "$HELM_FAKE_COUNT" "$HELM_PUSH_MARKER" "$HELM_LOGIN_MARKER"
}

reset_case
export HELM_FAKE_MODE=present
run_publisher
test ! -e "$HELM_PUSH_MARKER"
test ! -e "$HELM_LOGIN_MARKER"

reset_case
export HELM_FAKE_MODE=missing
run_publisher
test -s "$HELM_PUSH_MARKER"
test -s "$HELM_LOGIN_MARKER"

reset_case
export HELM_FAKE_MODE=transient-then-present
run_publisher
test "$(<"$HELM_FAKE_COUNT")" -eq 2
test ! -e "$HELM_PUSH_MARKER"

reset_case
export HELM_FAKE_MODE=transient
if run_publisher; then
  echo "persistent registry error unexpectedly triggered a Helm push" >&2
  exit 1
fi
test ! -e "$HELM_PUSH_MARKER"
test ! -e "$HELM_LOGIN_MARKER"
