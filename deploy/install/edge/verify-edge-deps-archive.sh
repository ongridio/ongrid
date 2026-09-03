#!/usr/bin/env bash
# Validate and optionally extract one immutable Edge dependency archive.

set -euo pipefail

ARCHIVE=${1:?usage: verify-edge-deps-archive.sh <archive> <target> <release-tag> [extract-dir]}
TARGET=${2:?target}
EXPECTED_TAG=${3:?release tag}
EXTRACT_DIR=${4:-}

die() {
    printf 'verify-edge-deps: %s\n' "$*" >&2
    exit 1
}

case "$TARGET" in
    linux-amd64|linux-arm64) ;;
    *) die "unsupported target: $TARGET" ;;
esac
[[ "$EXPECTED_TAG" =~ ^[0-9A-Za-z][0-9A-Za-z._-]*$ ]] \
    || die "invalid dependency release tag: $EXPECTED_TAG"
[[ -s "$ARCHIVE" && -f "$ARCHIVE" && ! -L "$ARCHIVE" ]] \
    || die "archive is missing or is not a regular file: $ARCHIVE"

command -v awk >/dev/null 2>&1 || die "awk is required"
command -v sha256sum >/dev/null 2>&1 || die "sha256sum is required"
command -v tar >/dev/null 2>&1 || die "tar is required"

work=""
if [[ -z "$EXTRACT_DIR" ]]; then
    work=$(mktemp -d "${TMPDIR:-/tmp}/verify-edge-deps.XXXXXX")
    trap 'rm -rf "$work"' EXIT
    EXTRACT_DIR="$work/extract"
fi
mkdir -p "$EXTRACT_DIR"
if find "$EXTRACT_DIR" -mindepth 1 -print -quit | grep -q .; then
    die "extract directory must be empty: $EXTRACT_DIR"
fi

components=(
    node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib
)
required=(TARGET DEPENDENCIES "${components[@]}")

if ! archive_entries=$(tar -tJf "$ARCHIVE"); then
    die "cannot list dependency archive: $ARCHIVE"
fi
while IFS= read -r entry; do
    entry=${entry#./}
    case "$entry" in
        ""|TARGET|DEPENDENCIES|MANIFEST.sha256|node_exporter|process_exporter|mysqld_exporter|postgres_exporter|redis_exporter|mongodb_exporter|otelcol-contrib) ;;
        *) die "archive contains unexpected path: $entry" ;;
    esac
done <<<"$archive_entries"
tar -xJf "$ARCHIVE" -C "$EXTRACT_DIR" \
    || die "cannot extract dependency archive: $ARCHIVE"

manifest="$EXTRACT_DIR/MANIFEST.sha256"
[[ -f "$manifest" && ! -L "$manifest" && -s "$manifest" ]] \
    || die "archive is missing a regular MANIFEST.sha256"
line_count=$(awk 'NF {count++} END {print count + 0}' "$manifest")
[[ "$line_count" == "${#required[@]}" ]] \
    || die "dependency manifest does not contain the required file set"

verify_manifest_entry() {
    local name=$1 file="$EXTRACT_DIR/$name" line expected_sha recorded_name extra actual_sha
    [[ -f "$file" && ! -L "$file" && -s "$file" ]] \
        || die "archive entry is missing or is not a regular file: $name"
    if ! line=$(awk -v expected="$name" '
        NF {
            recorded=$2
            sub(/^\*/, "", recorded)
            if (recorded == expected) {
                line=$0
                count++
            }
        }
        END {
            if (count != 1) exit 1
            print line
        }
    ' "$manifest"); then
        die "dependency manifest entry is missing or duplicated: $name"
    fi
    IFS=' ' read -r expected_sha recorded_name extra <<<"$line"
    recorded_name=${recorded_name#\*}
    [[ "$expected_sha" =~ ^[0-9a-f]{64}$ \
        && "$recorded_name" == "$name" \
        && -z "${extra:-}" ]] \
        || die "dependency manifest entry is malformed: $name"
    actual_sha=$(sha256sum "$file" | awk 'NR == 1 {print $1}')
    [[ "$actual_sha" == "$expected_sha" ]] \
        || die "dependency manifest checksum mismatch: $name"
}

for name in "${required[@]}"; do
    verify_manifest_entry "$name"
done

[[ "$(tr -d '[:space:]' < "$EXTRACT_DIR/TARGET")" == "$TARGET" ]] \
    || die "dependency archive target does not match $TARGET"

metadata="$EXTRACT_DIR/DEPENDENCIES"
metadata_lines=$(awk 'NF {count++} END {print count + 0}' "$metadata")
[[ "$metadata_lines" == 9 ]] || die "DEPENDENCIES must contain exactly 9 entries"
metadata_value() {
    local key=$1
    awk -F= -v expected="$key" '
        $1 == expected {
            value=substr($0, index($0, "=") + 1)
            count++
        }
        END {
            if (count != 1 || value == "") exit 1
            print value
        }
    ' "$metadata"
}

layout=$(metadata_value layout) || die "DEPENDENCIES has invalid layout metadata"
otelcol=$(metadata_value otelcol-contrib) || die "DEPENDENCIES has invalid otelcol-contrib metadata"
node_exporter=$(metadata_value node_exporter) || die "DEPENDENCIES has invalid node_exporter metadata"
process_exporter=$(metadata_value process_exporter) || die "DEPENDENCIES has invalid process_exporter metadata"
mysqld_exporter=$(metadata_value mysqld_exporter) || die "DEPENDENCIES has invalid mysqld_exporter metadata"
postgres_exporter=$(metadata_value postgres_exporter) || die "DEPENDENCIES has invalid postgres_exporter metadata"
redis_exporter=$(metadata_value redis_exporter) || die "DEPENDENCIES has invalid redis_exporter metadata"
mongodb_exporter=$(metadata_value mongodb_exporter) || die "DEPENDENCIES has invalid mongodb_exporter metadata"
release_tag=$(metadata_value release_tag) || die "DEPENDENCIES has invalid release_tag metadata"


computed_tag="edge-deps-layout${layout}-o${otelcol}-n${node_exporter}-pr${process_exporter}-my${mysqld_exporter}-pg${postgres_exporter}-r${redis_exporter}-m${mongodb_exporter}"
[[ "$release_tag" == "$EXPECTED_TAG" ]] \
    || die "dependency archive release tag does not match $EXPECTED_TAG"
[[ "$computed_tag" == "$EXPECTED_TAG" ]] \
    || die "dependency metadata does not reconstruct release tag $EXPECTED_TAG"

printf 'verified Edge dependency archive %s for %s\n' "$(basename "$ARCHIVE")" "$TARGET"
