#!/usr/bin/env bash
# Build direct-download CNB Release attachments for Edge installation.

set -euo pipefail

MODE=${1:?usage: build-edge-attachments.sh <deps|edge> <tag-or-version> <out-dir> <target...>}
IDENTIFIER=${2:?tag or version}
OUT_DIR=${3:?output directory}
shift 3
(( $# > 0 )) || { echo "build-edge-attachments: at least one target is required" >&2; exit 2; }

SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)
BIN_ROOT=${EDGE_BIN_ROOT:-$REPO_ROOT/bin}

command -v sha256sum >/dev/null 2>&1 || { echo "sha256sum is required" >&2; exit 1; }
command -v tar >/dev/null 2>&1 || { echo "tar is required" >&2; exit 1; }
command -v xz >/dev/null 2>&1 || { echo "xz is required" >&2; exit 1; }

deps=(
    node_exporter process_exporter mysqld_exporter postgres_exporter
    redis_exporter mongodb_exporter otelcol-contrib
)

mkdir -p "$OUT_DIR"
work=$(mktemp -d "${TMPDIR:-/tmp}/ongrid-edge-attachments.XXXXXX")
trap 'rm -rf "$work"' EXIT

write_sidecar() {
    local file=$1
    (cd "$(dirname "$file")" && sha256sum "$(basename "$file")" > "$(basename "$file").sha256")
}

for target in "$@"; do
    case "$target" in
        linux-amd64|linux-arm64) ;;
        *) echo "build-edge-attachments: unsupported target $target" >&2; exit 2 ;;
    esac

    case "$MODE" in
        deps)
            : "${OTELCOL_VERSION:?OTELCOL_VERSION is required}"
            : "${NODE_EXPORTER_VERSION:?NODE_EXPORTER_VERSION is required}"
            : "${PROCESS_EXPORTER_VERSION:?PROCESS_EXPORTER_VERSION is required}"
            : "${MYSQLD_EXPORTER_VERSION:?MYSQLD_EXPORTER_VERSION is required}"
            : "${POSTGRES_EXPORTER_VERSION:?POSTGRES_EXPORTER_VERSION is required}"
            : "${REDIS_EXPORTER_VERSION:?REDIS_EXPORTER_VERSION is required}"
            : "${MONGODB_EXPORTER_VERSION:?MONGODB_EXPORTER_VERSION is required}"

            stage="$work/deps-$target"
            mkdir -p "$stage"
            for component in "${deps[@]}"; do
                src="$BIN_ROOT/$target/$component"
                [[ -s "$src" ]] || { echo "build-edge-attachments: missing $src" >&2; exit 1; }
                install -m 0755 "$src" "$stage/$component"
            done
            printf '%s\n' "$target" > "$stage/TARGET"
            cat > "$stage/DEPENDENCIES" <<EOF
layout=2
otelcol-contrib=${OTELCOL_VERSION}
node_exporter=${NODE_EXPORTER_VERSION}
process_exporter=${PROCESS_EXPORTER_VERSION}
mysqld_exporter=${MYSQLD_EXPORTER_VERSION}
postgres_exporter=${POSTGRES_EXPORTER_VERSION}
redis_exporter=${REDIS_EXPORTER_VERSION}
mongodb_exporter=${MONGODB_EXPORTER_VERSION}
release_tag=${IDENTIFIER}
EOF
            (cd "$stage" && sha256sum TARGET DEPENDENCIES "${deps[@]}" > MANIFEST.sha256)
            archive_files=(TARGET DEPENDENCIES MANIFEST.sha256 "${deps[@]}")
            chmod 0644 "$stage/TARGET" "$stage/DEPENDENCIES" "$stage/MANIFEST.sha256"
            TZ=UTC touch -t 200001010000.00 "${archive_files[@]/#/$stage/}"
            output="$OUT_DIR/edge-deps-${target}.tar.xz"
            if tar --version 2>/dev/null | grep -q 'GNU tar'; then
                tar --format=ustar --owner=0 --group=0 --numeric-owner \
                    -C "$stage" -cf - "${archive_files[@]}"
            else
                COPYFILE_DISABLE=1 tar --format ustar --uid 0 --gid 0 --uname root --gname root \
                    -C "$stage" -cf - "${archive_files[@]}"
            fi | xz -T0 --block-size=64MiB -9 -c > "$output"
            write_sidecar "$output"
            ;;
        edge)
            src="$BIN_ROOT/$target/ongrid-edge"
            [[ -s "$src" ]] || { echo "build-edge-attachments: missing $src" >&2; exit 1; }
            output="$OUT_DIR/ongrid-edge-${target}-${IDENTIFIER}"
            install -m 0755 "$src" "$output"
            write_sidecar "$output"
            ;;
        *)
            echo "build-edge-attachments: mode must be deps or edge" >&2
            exit 2
            ;;
    esac
    echo "built $output"
done
