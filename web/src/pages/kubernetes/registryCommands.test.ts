import { spawnSync } from 'node:child_process';

import { describe, expect, it } from 'vitest';

import {
  containerdInsecureRegistryCommand,
  dockerInsecureRegistryCommand,
  managerRegistryHostFromCommand,
} from './registryCommands';

describe('Kubernetes registry commands', () => {
  it('extracts the manager registry and emits valid shell', () => {
    const install = "helm upgrade --install ongrid-edge 'https://manager.example:8443/edge/k8s/ongrid-edge.tgz'";
    const registry = managerRegistryHostFromCommand(install);
    expect(registry).toBe('manager.example:8443');

    for (const command of [
      containerdInsecureRegistryCommand(registry),
      dockerInsecureRegistryCommand(registry),
    ]) {
      const checked = spawnSync('bash', ['-n'], { input: command, encoding: 'utf8' });
      expect(checked.status, checked.stderr).toBe(0);
    }
  });
});
