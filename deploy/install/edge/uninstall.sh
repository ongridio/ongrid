#!/usr/bin/env bash
# ongrid-edge curl-pipe uninstaller.
#
# Usage:
#   curl -k -sSL https://<server>/uninstall.sh | bash
#
# Wipes the agent end-to-end: systemd units (agent + bundled exporters),
# binary, env, logs, bundled plugin binaries, plugin work dir (rendered
# configs + subprocess logs + .upgrade stage dir), and the service user.
# Idempotent — safe to re-run.

set -euo pipefail

INSTALL_DIR="/usr/local/bin"
ENV_DIR="/etc/ongrid-edge"
SERVICE_FILE="/etc/systemd/system/ongrid-edge.service"
LOG_DIR="/var/log/ongrid-edge"
SERVICE_USER="ongrid-edge"
# Wholesale plugin dirs: bundled binaries (promtail, node_exporter,
# process_exporter, ...) and plugin work state (configs + textfile
# producer outputs + .upgrade stage). Both are agent-owned; leaving
# either behind makes reinstall non-deterministic.
PLUGIN_BIN_DIR="/usr/local/lib/ongrid-edge"
PLUGIN_WORK_DIR="/var/lib/ongrid-edge"

if [[ $EUID -ne 0 ]]; then
    echo "[INFO] re-executing with sudo"
    exec sudo -E bash "$0" "$@"
fi

# Stop + disable the unit; ignore errors (e.g. unit not installed).
if systemctl list-unit-files | grep -q '^ongrid-edge\.service'; then
    systemctl disable --now ongrid-edge 2>/dev/null || true
fi

# Also stop the bundled exporters (installed alongside edge by
# install-edge.sh). Best-effort — fine if either was never installed.
if systemctl list-unit-files | grep -q '^ongrid-node-exporter\.service'; then
    systemctl disable --now ongrid-node-exporter 2>/dev/null || true
fi
if systemctl list-unit-files | grep -q '^ongrid-process-exporter\.service'; then
    systemctl disable --now ongrid-process-exporter 2>/dev/null || true
fi

rm -f "$SERVICE_FILE" "$INSTALL_DIR/ongrid-edge"
rm -f /etc/systemd/system/ongrid-node-exporter.service
rm -f /etc/systemd/system/ongrid-process-exporter.service
rm -rf "$ENV_DIR"
rm -rf "$LOG_DIR"
rm -rf "$PLUGIN_BIN_DIR"
rm -rf "$PLUGIN_WORK_DIR"

systemctl daemon-reload 2>/dev/null || true

# Remove the dedicated service user (best-effort).
if id -u "$SERVICE_USER" >/dev/null 2>&1; then
    userdel "$SERVICE_USER" 2>/dev/null || true
fi

echo "[OK] ongrid-edge uninstalled"
