function runtimeConfigPrivilegePrelude() {
  return [
    'if [ "$(id -u)" -eq 0 ]; then',
    "  SUDO=''",
    'elif command -v sudo >/dev/null 2>&1; then',
    "  SUDO='sudo'",
    'else',
    '  echo "root or sudo is required to configure the container runtime" >&2',
    '  exit 1',
    'fi',
  ];
}

export function managerRegistryHostFromCommand(command: string) {
  const matched = command.match(/https:\/\/([^/'"\s]+)\/edge\/k8s\/ongrid-edge\.tgz/);
  return matched?.[1] ?? '<manager>';
}

export function containerdInsecureRegistryCommand(registryHost: string) {
  return [
    'set -eu',
    `REGISTRY='${registryHost}'`,
    ...runtimeConfigPrivilegePrelude(),
    '${SUDO} mkdir -p /etc/containerd',
    '${SUDO} mkdir -p "/etc/containerd/certs.d/${REGISTRY}"',
    '${SUDO} tee "/etc/containerd/certs.d/${REGISTRY}/hosts.toml" >/dev/null <<EOF',
    'server = "https://${REGISTRY}"',
    '',
    '[host."https://${REGISTRY}"]',
    '  capabilities = ["pull", "resolve"]',
    '  skip_verify = true',
    'EOF',
    '',
    'command -v containerd >/dev/null 2>&1 || { echo "containerd is required" >&2; exit 1; }',
    '${SUDO} test -f /etc/containerd/config.toml || containerd config default | ${SUDO} tee /etc/containerd/config.toml >/dev/null',
    'BACKUP="/etc/containerd/config.toml.bak.$(date +%s)"',
    'TMP="/etc/containerd/config.toml.ongrid.$$"',
    '${SUDO} cp /etc/containerd/config.toml "${BACKUP}"',
    'CONTAINERD_MAJOR="$(containerd --version | awk \'{v=$3; sub(/^v/, "", v); split(v, p, "."); print p[1]}\')"',
    'if [ "${CONTAINERD_MAJOR}" -ge 2 ]; then',
    '  PLUGIN_KEY="io.containerd.cri.v1.images"',
    '  SECTION=\'[plugins."io.containerd.cri.v1.images".registry]\'',
    'else',
    '  PLUGIN_KEY="io.containerd.grpc.v1.cri"',
    '  SECTION=\'[plugins."io.containerd.grpc.v1.cri".registry]\'',
    'fi',
    '${SUDO} awk -v plugin="${PLUGIN_KEY}" -v section="${SECTION}" \'',
    'function emit_path() { if (in_target && !path_written) { print "  config_path = \\"/etc/containerd/certs.d\\""; path_written=1 } }',
    '/^\\[/ {',
    '  if (in_target) emit_path()',
    '  in_target=(index($0, plugin) > 0 && $0 ~ /\\.registry\\]$/)',
    '  if (in_target) { found=1; path_written=0 }',
    '  print; next',
    '}',
    'in_target && /^[[:space:]]*config_path[[:space:]]*=/ { if (!path_written) print "  config_path = \\"/etc/containerd/certs.d\\""; path_written=1; next }',
    '{ print }',
    'END { if (in_target) emit_path(); if (!found) { print ""; print section; print "  config_path = \\"/etc/containerd/certs.d\\"" } }',
    '\' /etc/containerd/config.toml | ${SUDO} tee "${TMP}" >/dev/null',
    'if ! ${SUDO} containerd --config "${TMP}" config dump >/dev/null; then',
    '  ${SUDO} rm -f "${TMP}"',
    '  echo "generated containerd config is invalid; original config kept at ${BACKUP}" >&2',
    '  exit 1',
    'fi',
    '${SUDO} mv "${TMP}" /etc/containerd/config.toml',
    '',
    '${SUDO} systemctl restart containerd',
  ].join('\n');
}

export function dockerInsecureRegistryCommand(registryHost: string) {
  return [
    'set -eu',
    `REGISTRY='${registryHost}'`,
    ...runtimeConfigPrivilegePrelude(),
    'command -v python3 >/dev/null 2>&1 || { echo "python3 is required to update /etc/docker/daemon.json" >&2; exit 1; }',
    '${SUDO} mkdir -p /etc/docker',
    '${SUDO} test ! -f /etc/docker/daemon.json || ${SUDO} cp /etc/docker/daemon.json /etc/docker/daemon.json.bak.$(date +%s)',
    "${SUDO} env REGISTRY=\"${REGISTRY}\" python3 - <<'PY'",
    'import json',
    'import os',
    'from pathlib import Path',
    '',
    "path = Path('/etc/docker/daemon.json')",
    "registry = os.environ['REGISTRY']",
    'data = {}',
    'if path.exists() and path.read_text().strip():',
    '    data = json.loads(path.read_text())',
    "registries = data.setdefault('insecure-registries', [])",
    'if registry not in registries:',
    '    registries.append(registry)',
    'path.write_text(json.dumps(data, indent=2, sort_keys=True) + "\\n")',
    'PY',
    '',
    '${SUDO} systemctl restart docker',
  ].join('\n');
}
