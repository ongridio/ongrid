import { spawnSync } from 'node:child_process';
import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';

import { describe, expect, it } from 'vitest';

import {
  managerRegistryHostFromCommand,
  registrySetupCommand,
} from './registryCommands';

describe('Kubernetes registry commands', () => {
  it('extracts the manager registry and emits valid shell', () => {
    const install = "helm upgrade --install ongrid-edge 'https://manager.example:8443/edge/k8s/ongrid-edge.tgz'";
    const registry = managerRegistryHostFromCommand(install);
    expect(registry).toBe('manager.example:8443');

    const command = registrySetupCommand(registry);
    expect(command).toBe(
      "curl -kfsSL 'https://manager.example:8443/edge/k8s/registry-setup.sh' | " +
        "bash -s -- --registry='manager.example:8443'",
    );
    const checked = spawnSync('bash', ['-n'], { input: command, encoding: 'utf8' });
    expect(checked.status, checked.stderr).toBe(0);
  });

  it('ships an executable runtime auto-detection script', () => {
    const script = readFileSync(resolve(process.cwd(), '../deploy/kubernetes/registry-setup.sh'), 'utf8');
    const checked = spawnSync('bash', ['-n'], { input: script, encoding: 'utf8' });

    expect(checked.status, checked.stderr).toBe(0);
    expect(script).toContain('configure_rancher_runtime');
    expect(script).toContain('configure_k3d');
    expect(script).toContain('rke2-server');
    expect(script).toContain('/etc/rancher/rke2/registries.yaml');
    expect(script).toContain('configure_containerd');
    expect(script).toContain('configure_docker');
  });
});
