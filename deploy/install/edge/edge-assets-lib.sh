#!/usr/bin/env bash
# Shared target-selection and embedded-asset validation for Manager installers.

ongrid_edge_config_value() {
    local file=$1 key=$2
    [[ -f "$file" ]] || return 1
    sed -n "s/^${key}=//p" "$file" | tail -n 1
}

ongrid_detect_host_edge_target() {
    local arch=${1:-}
    [[ -n "$arch" ]] || arch=$(uname -m)
    case "$arch" in
        x86_64|amd64) printf '%s\n' linux-amd64 ;;
        aarch64|arm64) printf '%s\n' linux-arm64 ;;
        *)
            printf '[ERROR] unsupported host architecture: %s\n' "$arch" >&2
            return 1
            ;;
    esac
}

ongrid_normalize_edge_targets() {
    local raw=${1:-linux-amd64} target normalized=""
    local targets=()
    read -r -a targets <<<"$raw"
    (( ${#targets[@]} > 0 )) || targets=(linux-amd64)
    for target in "${targets[@]}"; do
        case "$target" in
            linux-amd64|linux-arm64) ;;
            *) return 1 ;;
        esac
        case " $normalized " in
            *" $target "*) ;;
            *) normalized="${normalized:+$normalized }$target" ;;
        esac
    done
    printf '%s\n' "$normalized"
}

ongrid_edge_targets_from_directory() {
    local edge_dir=$1 file target targets=""
    [[ -d "$edge_dir" ]] || return 1
    for file in "$edge_dir"/ongrid-edge-linux-*; do
        [[ -f "$file" ]] || continue
        target=${file##*/ongrid-edge-}
        case "$target" in
            linux-amd64|linux-arm64)
                targets="${targets:+$targets }$target"
                ;;
        esac
    done
    [[ -n "$targets" ]] || return 1
    ongrid_normalize_edge_targets "$targets"
}

ongrid_resolve_edge_targets() {
    local explicit=$1 package_config=$2 installed_config=$3 existing_edge_dir=$4 raw=""
    if [[ -n "$explicit" ]]; then
        raw=$explicit
    else
        # The current package declares the artifacts required by its manager
        # version and must supersede an older generated selection. Operators
        # can still deliberately narrow it with the explicit environment var.
        raw=$(ongrid_edge_config_value "$package_config" ONGRID_EDGE_TARGETS 2>/dev/null || true)
        if [[ -z "$raw" ]]; then
            raw=$(ongrid_edge_config_value "$installed_config" ONGRID_EDGE_TARGETS 2>/dev/null || true)
        fi
        if [[ -z "$raw" ]]; then
            raw=$(ongrid_edge_targets_from_directory "$existing_edge_dir" 2>/dev/null || true)
        fi
    fi
    if [[ -z "$raw" ]]; then
        raw=$(ongrid_detect_host_edge_target) || return 1
    fi
    ongrid_normalize_edge_targets "$raw"
}

ongrid_write_edge_artifact_config() {
    local file=$1 deps_tag=$2 targets=$3 tmp
    tmp=$(mktemp "${file}.XXXXXX") || return 1
    {
        [[ -z "$deps_tag" ]] || printf 'ONGRID_EDGE_DEPS_TAG=%s\n' "$deps_tag"
        printf 'ONGRID_EDGE_TARGETS=%s\n' "$targets"
    } > "$tmp"
    chmod 0644 "$tmp"
    mv "$tmp" "$file"
}

ongrid_validate_embedded_edge_assets() {
    local edge_dir=$1 raw_targets=$2 target component
    local targets=() components=(
        ongrid-edge node_exporter process_exporter mysqld_exporter postgres_exporter
        redis_exporter mongodb_exporter otelcol-contrib
    )
    read -r -a targets <<<"$raw_targets"
    for target in "${targets[@]}"; do
        for component in "${components[@]}"; do
            [[ -f "$edge_dir/${component}-${target}" \
                && ! -L "$edge_dir/${component}-${target}" \
                && -s "$edge_dir/${component}-${target}" ]] || {
                printf '[ERROR] embedded Edge package is missing regular file %s-%s\n' \
                    "$component" "$target" >&2
                return 1
            }
        done
    done
}
