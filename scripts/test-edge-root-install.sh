#!/usr/bin/env bash
# Run through make test-edge-root-install: account fixtures stay in a disposable container.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
fail() { printf 'edge root install test failed: %s\n' "$*" >&2; exit 1; }
[[ $EUID -eq 0 && -f /.dockerenv ]] || fail 'requires a disposable root Linux container'
tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT
mkdir -p "$tmp_dir/bin" "$tmp_dir/bundle"
export EDGE_TEST_DIR="$tmp_dir"

# Downloads and systemd are stubbed; ownership and modes use real filesystem operations.
cat > "$tmp_dir/bin/curl" <<'EOF'
#!/bin/bash
set -eu
while [[ $# -gt 0 ]]; do
    if [[ $1 == -o ]]; then dest=$2; shift; fi
    shift
done
cp "$EDGE_TEST_DIR/bundle/ongrid-edge" "$dest"
EOF
cat > "$tmp_dir/bundle/ongrid-edge" <<'EOF'
#!/bin/bash
set -eu
for arg in "$@"; do
    if [[ $arg == --output=* ]]; then
        printf 'ONGRID_EDGE_ACCESS_KEY=test\nONGRID_EDGE_SECRET_KEY=test\n' > "${arg#*=}"
    fi
done
EOF
cat > "$tmp_dir/bin/systemctl" <<'EOF'
#!/bin/bash
printf '%s\n' "$*" >> "$EDGE_TEST_DIR/systemctl.log"
if [[ $1 == show ]]; then printf '%s\n' "${EDGE_TEST_RUNTIME_USER:-}"; fi
EOF
cat > "$tmp_dir/bin/journalctl" <<'EOF'
#!/bin/bash
if [[ $* == '-n 1 --no-pager' && ${EDGE_TEST_JOURNAL_FAIL:-0} == 1 ]]; then exit 55; fi
printf 'ongrid-edge v0.0.0 starting\nagent: registered with cloud {"edge_id":1}\n'
EOF
cat > "$tmp_dir/bin/account-command" <<'EOF'
#!/bin/bash
printf '%s %s\n' "${0##*/}" "$*" >> "$EDGE_TEST_DIR/accounts.log"
exit 97
EOF
for cmd in useradd usermod userdel groupadd groupmod groupdel; do
    ln -s account-command "$tmp_dir/bin/$cmd"
done
for cmd in sleep logger; do printf '#!/bin/bash\nexit 0\n' > "$tmp_dir/bin/$cmd"; done
for cmd in chown install; do
    cat > "$tmp_dir/bin/$cmd" <<'EOF'
#!/bin/bash
cmd=${0##*/}
if [[ ${EDGE_TEST_FAIL_COMMAND:-} == "$cmd" ]]; then exit 42; fi
exec "/usr/bin/$cmd" "$@"
EOF
done
cat > "$tmp_dir/bin/mktemp" <<'EOF'
#!/bin/bash
set -eu
if [[ ${EDGE_TEST_FAIL_COMMAND:-} == mktemp ]]; then exit 42; fi
path=$(/usr/bin/mktemp "$@")
printf '%s\n' "$path" >> "$EDGE_TEST_DIR/temps.log"
printf '%s\n' "$path"
EOF
chmod +x "$tmp_dir/bin/"* "$tmp_dir/bundle/ongrid-edge"
export PATH="$tmp_dir/bin:$PATH"
export ONGRID_CLOUD_ADDR=test.invalid:40012 EDGE_ACCESS_KEY=test EDGE_SECRET_KEY=test
export NO_COLOR=1 ONGRID_INSTALL_WAIT=5

for plugin in obi otelcol-contrib node_exporter process_exporter mysqld_exporter postgres_exporter redis_exporter mongodb_exporter; do
    cp "$tmp_dir/bundle/ongrid-edge" "$tmp_dir/bundle/$plugin-linux-$(dpkg --print-architecture)"
done
cp "$repo_root/deploy/install/edge/ongrid-edge.env.example" \
    "$repo_root/deploy/install/edge/ongrid-edge.service" \
    "$repo_root/deploy/install/edge/ongrid-edge-upgrade.service" \
    "$repo_root/deploy/install/apply-pending-upgrade.sh" "$tmp_dir/bundle/"

prepare_installer() {
    target="$tmp_dir/$scenario/$1"
    mkdir -p "$target/bundle" "$target/root/usr/local/bin" "$target/root/etc/systemd/system"
    cp -R "$tmp_dir/bundle/." "$target/bundle/"
    # Redirect only managed paths; execute the complete production installer.
    sed -e "s|/usr/local/bin|$target/root/usr/local/bin|g" \
        -e "s|/usr/local/lib/ongrid-edge|$target/root/usr/local/lib/ongrid-edge|g" \
        -e "s|/etc/ongrid-edge|$target/root/etc/ongrid-edge|g" \
        -e "s|/etc/systemd/system|$target/root/etc/systemd/system|g" \
        -e "s|/var/log/ongrid-edge|$target/root/var/log/ongrid-edge|g" \
        -e "s|/var/lib/ongrid-edge|$target/root/var/lib/ongrid-edge|g" \
        "$repo_root/deploy/install/edge/$1" > "$target/bundle/installer.sh"
    args=()
    if [[ $1 == install.sh ]]; then
        args=(--access-key=test --secret-key=test --server-edge-addr=test.invalid:40012 --server-http-addr=test.invalid)
    fi
}

check_permissions() {
    local path expected
    for path in etc/ongrid-edge var/log/ongrid-edge var/lib/ongrid-edge etc/ongrid-edge/ongrid-edge.env; do
        expected=750
        [[ $path != var/lib/ongrid-edge ]] || expected=755
        [[ $path != *.env ]] || expected=640
        [[ $(stat -c '%u:%g:%a' "$target/root/$path") == "0:0:$expected" ]] \
            || fail "$scenario/$installer wrong owner/mode: $path"
    done
    if [[ $installer == install-edge.sh ]]; then
        for path in plugins .upgrade; do
            [[ $(stat -c '%u:%g:%a' "$target/root/var/lib/ongrid-edge/$path") == 0:0:750 ]] \
                || fail "$scenario/$installer wrong state directory owner/mode: $path"
        done
    fi
}

run_installer() {
    : > "$tmp_dir/systemctl.log"
    if ! bash "$target/bundle/installer.sh" "${args[@]}" > "$target/install.log" 2>&1; then
        cat "$target/install.log" >&2
        fail "$scenario/$installer installation failed"
    fi
    grep -Fq 'self-check passed' "$target/install.log" || { cat "$target/install.log" >&2; fail 'self-check failed'; }
    grep -Fxq 'restart ongrid-edge' "$tmp_dir/systemctl.log" || fail 'service was not restarted'
    check_permissions
}

for scenario in fresh-yes fresh-no user-only group-only legacy; do
    if getent passwd ongrid-edge >/dev/null; then /usr/sbin/userdel ongrid-edge; fi
    if getent group ongrid-edge >/dev/null; then /usr/sbin/groupdel ongrid-edge; fi
    sed -i 's/^USERGROUPS_ENAB.*/USERGROUPS_ENAB no/' /etc/login.defs
    case "$scenario" in
        fresh-yes) sed -i 's/^USERGROUPS_ENAB.*/USERGROUPS_ENAB yes/' /etc/login.defs ;;
        user-only) /usr/sbin/useradd --system --no-user-group --gid users ongrid-edge ;;
        group-only) /usr/sbin/groupadd --system ongrid-edge ;;
        legacy)
            /usr/sbin/groupadd --system ongrid-edge
            /usr/sbin/useradd --system --gid ongrid-edge ongrid-edge
            ;;
    esac
    cp /etc/passwd "$tmp_dir/passwd.before"
    cp /etc/group "$tmp_dir/group.before"
    : > "$tmp_dir/accounts.log"
    for installer in install.sh install-edge.sh; do
        prepare_installer "$installer"
        run_installer
        # Reinstall must preserve data and settings while repairing old ownership.
        printf 'keep\n' > "$target/root/var/lib/ongrid-edge/keep"
        printf '\nONGRID_EDGE_METRICS_ADDR=:19101\n' >> "$target/root/etc/ongrid-edge/ongrid-edge.env"
        /usr/bin/chown -R 65534:65534 "$target/root/etc/ongrid-edge" "$target/root/var/lib/ongrid-edge" "$target/root/var/log/ongrid-edge"
        EDGE_TEST_RUNTIME_USER=root run_installer
        grep -Fxq keep "$target/root/var/lib/ongrid-edge/keep" || fail 'reinstall lost state'
        grep -Fxq 'ONGRID_EDGE_METRICS_ADDR=:19101' "$target/root/etc/ongrid-edge/ongrid-edge.env" || fail 'reinstall lost metrics setting'
        if [[ $installer == install.sh ]]; then
            args=(--enrollment-token=test --server-edge-addr=test.invalid:40012 --server-http-addr=test.invalid)
            run_installer
        fi
    done
    bash "$repo_root/scripts/test-apply-pending-upgrade.sh" > "$tmp_dir/upgrade.log" 2>&1 \
        || { cat "$tmp_dir/upgrade.log" >&2; fail 'upgrade/rollback failed'; }
    [[ ! -s "$tmp_dir/accounts.log" ]] || { cat "$tmp_dir/accounts.log" >&2; fail 'touched legacy accounts'; }
    cmp /etc/passwd "$tmp_dir/passwd.before"
    cmp /etc/group "$tmp_dir/group.before"
    printf 'PASS %s: online/offline install, reinstall, enrollment, upgrade and rollback\n' "$scenario"
done

# Fail before and after binary installation; cleanup must only remove its own temp files.
installer=install.sh
prepare_installer "$installer"
args=(--enrollment-token=test --server-edge-addr=test.invalid:40012 --server-http-addr=test.invalid)
printf 'keep\n' > "$target/unrelated"
for cmd in mktemp install chown; do
    : > "$tmp_dir/temps.log"
    rc=0
    TMP_BIN="$target/unrelated" PENDING_ENV_FILE="$target/unrelated" EDGE_TEST_FAIL_COMMAND="$cmd" \
        bash "$target/bundle/installer.sh" "${args[@]}" > "$target/error.log" 2>&1 || rc=$?
    [[ $rc == 42 ]] || fail "lost $cmd exit code: $rc"
    grep -Fq '(exit 42)' "$target/error.log" || { cat "$target/error.log" >&2; fail 'wrong error log exit code'; }
    while IFS= read -r path; do [[ ! -e $path ]] || fail "leaked temp file: $path"; done < "$tmp_dir/temps.log"
    grep -Fxq keep "$target/unrelated" || fail 'cleanup removed an inherited path'
done
for installer in install.sh install-edge.sh; do
    prepare_installer "$installer"
    EDGE_TEST_JOURNAL_FAIL=1 bash "$target/bundle/installer.sh" "${args[@]}" > "$target/journal.log" 2>&1
    grep -Fq 'root cannot read the journal' "$target/journal.log" || fail 'journal failure checked the wrong identity'
    if grep -Fq 'self-check passed' "$target/journal.log"; then fail 'journal failure was hidden'; fi
done
echo 'PASS failure diagnostics: original exit status, temp cleanup and root journal access'
