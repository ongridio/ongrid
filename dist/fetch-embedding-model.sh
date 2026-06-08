#!/usr/bin/env bash
# fetch-embedding-model.sh — pre-cache the BGE-small-zh-v1.5 ONNX model
# into .cache/embedding-models/ so dist/package.sh can bundle it into
# the install tarball. Run once on a host with HuggingFace reach;
# subsequent `make package` runs pick up the cache.
#
# Why a separate script: the model is ~55MB download + ~97MB on disk
# after extraction, slow over CN networks; pinning a build step on
# network is brittle. dist/package.sh warns + skips if not pre-cached.

set -euo pipefail

SCRIPT_DIR=$(cd -- "$(dirname "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/.." && pwd)
DEST="$REPO_ROOT/.cache/embedding-models"
mkdir -p "$DEST"

log()  { printf '[fetch-emb] %s\n' "$*"; }
warn() { printf '[fetch-emb] warn: %s\n' "$*" >&2; }
die()  { printf '[fetch-emb] error: %s\n' "$*" >&2; exit 1; }

# The bundle layout fastembed-go expects under CacheDir/<EmbeddingModel>/
# is provided by Qdrant's fastembed tarball. Keep this in sync with
# github.com/anush008/fastembed-go retrieveModel/downloadFromGcs.
MODEL=fast-bge-small-zh-v1.5
MODEL_URL=${ONGRID_FASTEMBED_MODEL_URL:-https://storage.googleapis.com/qdrant-fastembed/$MODEL.tar.gz}
TARGET="$DEST/$MODEL"

mkdir -p "$TARGET"
if [[ -s "$TARGET/model_optimized.onnx" && -s "$TARGET/tokenizer.json" ]]; then
    log "$MODEL already present — skipping"
else
    tmp=$(mktemp "$DEST/$MODEL.XXXXXX.tar.gz")
    cleanup() { rm -f "$tmp"; }
    trap cleanup EXIT
    log "fetching $MODEL_URL"
    curl -fL --retry 3 --connect-timeout 15 -o "$tmp" "$MODEL_URL" || die "failed to fetch $MODEL_URL"
    log "extracting $MODEL"
    tar -xzf "$tmp" -C "$DEST"
fi

log "cached $TARGET ($(du -sh "$TARGET" | awk '{print $1}'))"
log "next \`make package\` will bundle this under embeddings/$MODEL/"
