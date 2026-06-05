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

# Stop + disable units unconditionally. The previous "list-unit-files
# | grep -q ^name.service" precondition silently skipped the stop on
# hosts where the output formatting (leading whitespace, deprecated/
# masked decorations, paged output) confused the anchored grep. The
# uninstaller then went on to rm -f the binary while the supervisor
# was still running — agent + every subprocess survived, printed [OK].
# `systemctl stop` is safe to call on an unknown unit (logs to stderr,
# returns non-zero) so we just suppress + ignore failures.
systemctl stop    ongrid-edge ongrid-node-exporter ongrid-process-exporter 2>/dev/null || true
systemctl disable ongrid-edge ongrid-node-exporter ongrid-process-exporter 2>/dev/null || true

# Defensive: if systemd never actually managed the agent (manual
# install, broken unit file, etc.), kill the supervisor and any
# subprocess plugins by binary path. Matches every plugin (promtail,
# otelcol-contrib, node_exporter, process_exporter, ...) without
# enumerating them.
pkill -9 -f '/usr/local/bin/ongrid-edge|/usr/local/lib/ongrid-edge/' 2>/dev/null || true

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
