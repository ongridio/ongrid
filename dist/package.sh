#!/usr/bin/env bash
# ongrid release packager
# ---------------------------------------------------------------------------
# Usage: package.sh <VERSION> <STAGE_DIR> <OUT_DIR>
#
#   VERSION     e.g. v0.1.0
#   STAGE_DIR   staging directory whose basename is "ongrid-<VERSION>-linux-<arch>"
#   OUT_DIR     directory in which the final tarball is written
#
# Produces:
#   <OUT_DIR>/ongrid-<VERSION>-linux-<arch>.tar.xz
#   <OUT_DIR>/ongrid-<VERSION>-linux-<arch>.tar.xz.sha256
#
# Optional env:
#   PACKAGE_TARGET  linux-amd64 (default) or linux-arm64
#   EDGE_TARGETS    edge binary targets to bundle (default linux-amd64)
#   ONGRID_BUNDLE_EMBEDDING_MODEL=0 omits the local embedding model
#
# The script is tolerant of missing deploy/install/* files: it warns and
# continues so the pipeline is testable before the on-target scripts land.
# ---------------------------------------------------------------------------

set -euo pipefail

# --- arg check --------------------------------------------------------------
if [ "$#" -ne 3 ]; then
    echo "usage: $0 <VERSION> <STAGE_DIR> <OUT_DIR>" >&2
    exit 2
fi

VERSION="$1"
STAGE_DIR="$2"
OUT_DIR="$3"

# Resolve repo root: script lives in <repo>/dist/.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"

PACKAGE_TARGET="${PACKAGE_TARGET:-}"
if [[ -z "$PACKAGE_TARGET" ]]; then
    STAGE_BASE="$(basename "$STAGE_DIR")"
    STAGE_PREFIX="ongrid-${VERSION}-"
    if [[ "$STAGE_BASE" == "$STAGE_PREFIX"* ]]; then
        PACKAGE_TARGET="${STAGE_BASE#${STAGE_PREFIX}}"
    else
        PACKAGE_TARGET="linux-amd64"
    fi
fi

case "$PACKAGE_TARGET" in
    linux-amd64|linux-arm64) ;;
    *)
        echo "[pkg] error: unsupported PACKAGE_TARGET=${PACKAGE_TARGET}; expected linux-amd64 or linux-arm64" >&2
        exit 2
        ;;
esac

PKG_NAME="ongrid-${VERSION}-${PACKAGE_TARGET}"
TARBALL="${OUT_DIR}/${PKG_NAME}.tar.xz"
SHAFILE="${TARBALL}.sha256"

# --- pretty print helpers ---------------------------------------------------
log()  { printf '[pkg] %s\n' "$*"; }
warn() { printf '[pkg] warn: %s\n' "$*" >&2; }
die()  { printf '[pkg] error: %s\n' "$*" >&2; exit 1; }

# --- xz presence check ------------------------------------------------------
# The release tarball is xz-compressed (see "tar it up" below).
if ! command -v xz >/dev/null 2>&1; then
    die "xz not found in PATH — required to compress the release tarball (apt/dnf install xz / xz-utils)"
fi

# --- stage dir layout -------------------------------------------------------
log "target ${PACKAGE_TARGET}"
log "staging ${PKG_NAME} into ${STAGE_DIR}"
mkdir -p "${STAGE_DIR}" \
         "${STAGE_DIR}/edge" \
         "${STAGE_DIR}/grafana/provisioning/datasources"

# copy_opt <src> <dst> [chmod-mode]
# Copies src -> dst if src exists; warns and continues otherwise.
copy_opt() {
    local src="$1" dst="$2" mode="${3:-}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        if [ -n "$mode" ]; then
            chmod "$mode" "$dst"
        fi
        log "  + $(basename "$dst")"
    else
        warn "$src missing; skipping"
    fi
}

# --- VERSION file -----------------------------------------------------------
if [ -f "${REPO_ROOT}/VERSION" ]; then
    cp "${REPO_ROOT}/VERSION" "${STAGE_DIR}/VERSION"
    log "  + VERSION"
else
    printf '%s\n' "${VERSION}" > "${STAGE_DIR}/VERSION"
    warn "repo VERSION missing; synthesized from arg"
fi

# --- top-level install-time assets (owned by parallel agent) ----------------
copy_opt "${REPO_ROOT}/deploy/install/README.md"           "${STAGE_DIR}/README.md"
copy_opt "${REPO_ROOT}/deploy/install/install.sh"          "${STAGE_DIR}/install.sh"          755
copy_opt "${REPO_ROOT}/deploy/install/uninstall.sh"        "${STAGE_DIR}/uninstall.sh"        755
copy_opt "${REPO_ROOT}/deploy/install/upgrade.sh"          "${STAGE_DIR}/upgrade.sh"          755
copy_opt "${REPO_ROOT}/deploy/install/docker-compose.yml"  "${STAGE_DIR}/docker-compose.yml"
copy_opt "${REPO_ROOT}/deploy/install/.env.example"        "${STAGE_DIR}/.env.example"
copy_opt "${REPO_ROOT}/deploy/install/frontier.yaml"       "${STAGE_DIR}/frontier.yaml"

# --- nginx config + certs scaffold (ADR-008) --------------------------------
# nginx.conf is bind-mounted into the nginx container at runtime; certs/ is
# populated by install.sh on first run (self-signed) or by the operator
# replacing tls.crt + tls.key with a real certificate.
copy_opt "${REPO_ROOT}/deploy/install/nginx.conf"          "${STAGE_DIR}/nginx.conf"
mkdir -p "${STAGE_DIR}/certs"
# Empty placeholder; install.sh generates self-signed cert at first run.
touch "${STAGE_DIR}/certs/.gitkeep"

# --- prometheus config ------------------------------------------------------
# ADR-009 staged the canonical Compose prometheus.yml under deploy/install/.
# The compose model bind-mounts it flat at the install root.
copy_opt "${REPO_ROOT}/deploy/install/prometheus.yml" \
         "${STAGE_DIR}/prometheus.yml"
# ADR-026 self-obs alert rules — bind-mounted at /etc/prometheus/rules.yml.
copy_opt "${REPO_ROOT}/deploy/install/prometheus-rules.yml" \
         "${STAGE_DIR}/prometheus-rules.yml"

# --- grafana provisioning ---------------------------------------------------
if [[ -d "${REPO_ROOT}/deploy/install/grafana" ]]; then
    mkdir -p "${STAGE_DIR}/grafana"
    cp -rf "${REPO_ROOT}/deploy/install/grafana/." "${STAGE_DIR}/grafana/"
    log "  + grafana/"
else
    warn "${REPO_ROOT}/deploy/install/grafana missing; skipping"
fi

# --- searxng config ---------------------------------------------------------
# Bind-mounted by docker-compose into the searxng container at /etc/searxng;
# settings.yml ships in deploy/install/searxng/. Without it the searxng
# container falls back to the image's stock config which lacks our LIMITER
# tweaks and starts in a degraded mode (web_search skill calls would fail).
if [[ -d "${REPO_ROOT}/deploy/install/searxng" ]]; then
    mkdir -p "${STAGE_DIR}/searxng"
    cp -rf "${REPO_ROOT}/deploy/install/searxng/." "${STAGE_DIR}/searxng/"
    log "  + searxng/"
else
    warn "${REPO_ROOT}/deploy/install/searxng missing; skipping"
fi

# --- bundled local-embedding ONNX model (ADR-027 Phase-2 offline RAG) -------
# Stage the fastembed-go BGE-small-zh-v1.5 cache so air-gapped installs
# don't need HuggingFace reach to bring up the local embedder. The
# manager process reads from $ONGRID_EMBEDDING_CACHE_DIR
# (default /var/lib/ongrid/embeddings) — install.sh stages this
# bundle there on first install.
#
# Off-switch: ONGRID_BUNDLE_EMBEDDING_MODEL=0 drops the model from
# the tarball (-97MB) for installs that already have a working API key.
BUNDLE_EMB="${ONGRID_BUNDLE_EMBEDDING_MODEL:-1}"
if [[ "$BUNDLE_EMB" == "1" ]]; then
    EMB_CACHE_HOST="${REPO_ROOT}/.cache/embedding-models/fast-bge-small-zh-v1.5"
    if [[ ! -f "$EMB_CACHE_HOST/model_optimized.onnx" ]]; then
        warn "bundled embedding model not pre-cached at $EMB_CACHE_HOST"
        warn "  run dist/fetch-embedding-model.sh once on a network-friendly host first"
        warn "  skipping — installs without LLM key will fall back to download-on-first-use"
    else
        mkdir -p "${STAGE_DIR}/embeddings/fast-bge-small-zh-v1.5"
        cp -rf "$EMB_CACHE_HOST/." "${STAGE_DIR}/embeddings/fast-bge-small-zh-v1.5/"
        log "  + embeddings/fast-bge-small-zh-v1.5/ ($(du -sh "$EMB_CACHE_HOST" | awk '{print $1}'))"
    fi
fi

# --- edge binaries -----------------------------------------------------------
# Edges are amd64-only in our deployments, so both compatibility-named server
# packages carry the same linux/amd64 Edge payload. Override EDGE_TARGETS to
# bundle more Edge architectures when the deployment policy changes.
EDGE_TARGETS="${EDGE_TARGETS:-linux-amd64}"
for target in ${EDGE_TARGETS}; do
    src="${REPO_ROOT}/bin/${target}/ongrid-edge"
    dst="${STAGE_DIR}/edge/ongrid-edge-${target}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        chmod 755 "$dst"
        log "  + edge/ongrid-edge-${target}"
    else
        warn "edge binary ${src} missing; skipping"
    fi
done

# --- bundled plugin binaries (ADR-015) --------------------------------------
# promtail (logs plugin) ships next to ongrid-edge so install-edge.sh can
# install it under /usr/local/lib/ongrid-edge/promtail.
for target in ${EDGE_TARGETS}; do
    src="${REPO_ROOT}/bin/${target}/promtail"
    dst="${STAGE_DIR}/edge/promtail-${target}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        chmod 755 "$dst"
        log "  + edge/promtail-${target}"
    else
        warn "promtail binary ${src} missing; logs plugin won't work on ${target}. Run 'make fetch-promtail'."
    fi
done

# otelcol-contrib (traces plugin, ADR-013) ships next to ongrid-edge so
# install-edge.sh can install it under /usr/local/lib/ongrid-edge/otelcol-contrib.
# Linux-only: upstream doesn't publish darwin builds in the contrib stream;
# darwin edges will see the traces plugin disabled (warned by install-edge.sh).
for target in ${EDGE_TARGETS}; do
    src="${REPO_ROOT}/bin/${target}/otelcol-contrib"
    dst="${STAGE_DIR}/edge/otelcol-contrib-${target}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        chmod 755 "$dst"
        log "  + edge/otelcol-contrib-${target}"
    else
        warn "otelcol-contrib binary ${src} missing; traces plugin won't work on ${target}. Run 'make fetch-otelcol'."
    fi
done

# node_exporter (host metric source for the metrics plugin) ships next
# to ongrid-edge so install-edge.sh stands up a systemd-managed
# node_exporter on the host. Without this, fresh installs land without
# a metric source and Monitor stays empty until an operator manually
# installs node_exporter.
for target in ${EDGE_TARGETS}; do
    src="${REPO_ROOT}/bin/${target}/node_exporter"
    dst="${STAGE_DIR}/edge/node_exporter-${target}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        chmod 755 "$dst"
        log "  + edge/node_exporter-${target}"
    else
        warn "node_exporter binary ${src} missing; host metrics won't flow on ${target}. Run 'make fetch-node-exporter'."
    fi
done

# process-exporter (per-process metrics — backs the "Top N processes
# timeline" PromQL panel). Same systemd-managed deploy model as
# node_exporter. Without this, the process timeline panel stays empty.
for target in ${EDGE_TARGETS}; do
    src="${REPO_ROOT}/bin/${target}/process_exporter"
    dst="${STAGE_DIR}/edge/process_exporter-${target}"
    if [ -f "$src" ]; then
        cp "$src" "$dst"
        chmod 755 "$dst"
        log "  + edge/process_exporter-${target}"
    else
        warn "process_exporter binary ${src} missing; per-process metrics won't flow on ${target}. Run 'make fetch-process-exporter'."
    fi
done

# Database exporters for the databasemetrics plugin. These are edge-managed
# subprocesses; the manager stores only the edge-local secret file path.
for exporter in mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
    for target in ${EDGE_TARGETS}; do
        src="${REPO_ROOT}/bin/${target}/${exporter}"
        dst="${STAGE_DIR}/edge/${exporter}-${target}"
        if [ -f "$src" ]; then
            cp "$src" "$dst"
            chmod 755 "$dst"
            log "  + edge/${exporter}-${target}"
        else
            warn "${exporter} binary ${src} missing; related databasemetrics sources won't work on ${target}. Run 'make fetch-db-exporters'."
        fi
    done
done

# --- loki config (ADR-012) --------------------------------------------------
copy_opt "${REPO_ROOT}/deploy/install/loki-config.yaml" \
         "${STAGE_DIR}/loki-config.yaml"

# --- tempo config (ADR-013) -------------------------------------------------
copy_opt "${REPO_ROOT}/deploy/install/tempo-config.yaml" \
         "${STAGE_DIR}/tempo-config.yaml"

# --- edge install assets ----------------------------------------------------
# install.sh is the curl-pipe network installer; nginx serves it at
# https://<host>/install.sh. install-edge.sh is the offline variant for
# operators who already have the tarball extracted on the target host.
copy_opt "${REPO_ROOT}/deploy/install/edge/install.sh" \
         "${STAGE_DIR}/edge/install.sh" 755
copy_opt "${REPO_ROOT}/deploy/install/edge/uninstall.sh" \
         "${STAGE_DIR}/edge/uninstall.sh" 755
copy_opt "${REPO_ROOT}/deploy/install/edge/install-edge.sh" \
         "${STAGE_DIR}/edge/install-edge.sh" 755
copy_opt "${REPO_ROOT}/deploy/install/edge/ongrid-edge.env.example" \
         "${STAGE_DIR}/edge/ongrid-edge.env.example"
copy_opt "${REPO_ROOT}/deploy/install/edge/ongrid-edge.service" \
         "${STAGE_DIR}/edge/ongrid-edge.service"
# ADR-024 privileged apply oneshot — ongrid-edge.service pulls it via Wants=
# so apply-pending-upgrade.sh runs as root before each agent start. Required
# on systemd 219 (CentOS 7) where the old ExecStartPre `+` prefix was ignored.
copy_opt "${REPO_ROOT}/deploy/install/edge/ongrid-edge-upgrade.service" \
         "${STAGE_DIR}/edge/ongrid-edge-upgrade.service"

# C11 Phase-B / ADR-024 remote upgrade — apply script runs as root (via the
# ongrid-edge-upgrade.service oneshot) before each ongrid-edge start; swaps a
# sha256-verified staged bundle into place. install-edge.sh installs this to
# /usr/local/lib/ongrid-edge/.
copy_opt "${REPO_ROOT}/deploy/install/apply-pending-upgrade.sh" \
         "${STAGE_DIR}/edge/apply-pending-upgrade.sh" 755

# Host-side ADR-024 bundle rebuilder (see note below). install.sh /
# upgrade.sh run it post-extract to reassemble the upgrade bundle from
# the loose binaries already staged in edge/.
copy_opt "${REPO_ROOT}/deploy/install/edge/build-edge-bundle.sh" \
         "${STAGE_DIR}/edge/build-edge-bundle.sh" 755

# --- edge bundle for ADR-024 one-button upgrade -----------------------------
# We deliberately do NOT pack edge-bundle-<arch>-<version>.tar.gz into the
# release tarball anymore. Those bundles are byte-for-byte copies of the loose
# linux binaries already staged above, and being pre-gzipped they added
# ~120 MB+ of incompressible payload to every release (they are still published
# as standalone GitHub release assets by `make build-edge-bundle`).
# install.sh / upgrade.sh now reassemble it on the manager host via
# edge/build-edge-bundle.sh after extracting STAGE_DIR/edge/* to
# /opt/ongrid/edge/, where docker-compose bind-mounts it into ongrid-web's
# nginx html and it is served from /edge/ exactly as before.

# --- manifest ---------------------------------------------------------------
log "manifest:"
( cd "$(dirname "${STAGE_DIR}")" && find "$(basename "${STAGE_DIR}")" -type f -print0 \
  | sort -z | xargs -0 -I{} bash -c 'printf "  %10d  %s\n" "$(wc -c < "{}")" "{}"' ) || true

# --- tar it up --------------------------------------------------------------
# xz -9e over gzip: the staged tree is mostly stripped Edge/plugin binaries and
# the optional embedding model, which xz packs tighter than gzip. `tar xf` on the
# target auto-detects xz (xz-utils is ubiquitous on Linux), so the operator
# extract command is unchanged. -T0 parallelises across cores; the slight
# ratio cost vs single-thread is worth the faster release builds. We pipe
# explicitly rather than rely on `tar -J`/`-I` so the invocation is portable
# across GNU tar and bsdtar build hosts. set -o pipefail surfaces failures.
mkdir -p "${OUT_DIR}"
log "creating ${TARBALL}"
STAGE_PARENT="$(dirname "${STAGE_DIR}")"
STAGE_BASE="$(basename "${STAGE_DIR}")"

# Scrub macOS xattrs + AppleDouble metadata before tarring. macOS bsdtar will
# otherwise leak LIBARCHIVE.xattr.com.apple.provenance, com.apple.quarantine
# etc into the archive, which makes GNU tar on the target machine spew
# warnings on every extracted file. Belt-and-braces: scrub on the filesystem
# AND tell tar not to capture xattrs (via --no-xattrs on GNU tar / new bsdtar,
# and COPYFILE_DISABLE=1 for older bsdtar that doesn't know the flag).
if [[ "${OSTYPE:-}" == "darwin"* ]]; then
    log "stripping macOS xattrs from stage (darwin host)"
    xattr -rc "${STAGE_DIR}" 2>/dev/null || true
    find "${STAGE_DIR}" -name '.DS_Store' -delete 2>/dev/null || true
fi

TAR_STDERR="$(mktemp -t ongrid-tar-stderr.XXXXXX)"
COPYFILE_DISABLE=1 tar --no-xattrs -cf - -C "${STAGE_PARENT}" "${STAGE_BASE}" 2>"${TAR_STDERR}" \
    | xz -9e -T0 -c > "${TARBALL}"
# Suppress just the "unknown flag" chatter from older bsdtar (--no-xattrs is
# not universal); surface anything else so real tar errors aren't masked.
if [ -s "${TAR_STDERR}" ]; then
    grep -vE 'unknown extended|--no-xattrs|unrecognized option' "${TAR_STDERR}" >&2 || true
fi
rm -f "${TAR_STDERR}"

# --- sha256 sidecar ---------------------------------------------------------
log "computing sha256"
if command -v sha256sum >/dev/null 2>&1; then
    ( cd "${OUT_DIR}" && sha256sum "$(basename "${TARBALL}")" > "$(basename "${SHAFILE}")" )
elif command -v shasum >/dev/null 2>&1; then
    ( cd "${OUT_DIR}" && shasum -a 256 "$(basename "${TARBALL}")" > "$(basename "${SHAFILE}")" )
else
    warn "no sha256sum/shasum found; skipping checksum sidecar"
fi

# --- summary ----------------------------------------------------------------
if command -v du >/dev/null 2>&1; then
    SIZE="$(du -h "${TARBALL}" | cut -f1)"
else
    SIZE="$(wc -c < "${TARBALL}") bytes"
fi
log "done"
log "  tarball : ${TARBALL} (${SIZE})"
if [ -f "${SHAFILE}" ]; then
    log "  sha256  : ${SHAFILE}"
fi
