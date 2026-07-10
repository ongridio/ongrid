export function managerRegistryHostFromCommand(command: string) {
  const matched = command.match(/https:\/\/([^/'"\s]+)\/edge\/k8s\/ongrid-edge\.tgz/);
  return matched?.[1] ?? '<manager>';
}

export function registrySetupCommand(registryHost: string) {
  return (
    `curl -kfsSL 'https://${registryHost}/edge/k8s/registry-setup.sh' | ` +
    `bash -s -- --registry='${registryHost}'`
  );
}
