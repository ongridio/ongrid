import type { PluginHealth } from '@/api/integrations';

type Listener = NonNullable<PluginHealth['candidates']>[number];
export type DiscoveredProcess = Omit<Listener, 'port'> & { ports: number[] };

// Only listeners with the same executable and reported PID belong together.
// Without a PID, keep the listener separate rather than merge unrelated apps.
export function groupDiscoveredProcesses(candidates: Listener[] = []): DiscoveredProcess[] {
  const processes = new Map<string, DiscoveredProcess>();
  for (const candidate of candidates) {
    const key = JSON.stringify([candidate.executable, candidate.pid > 0 ? candidate.pid : `port:${candidate.port}`]);
    let process = processes.get(key);
    if (!process) {
      process = { executable: candidate.executable, pid: candidate.pid, ports: [] };
      processes.set(key, process);
    }
    if (!process.ports.includes(candidate.port)) process.ports.push(candidate.port);
  }
  return [...processes.values()].map(process => ({ ...process, ports: process.ports.sort((a, b) => a - b) }));
}

export function matchesProcess(target: Pick<Listener, 'executable' | 'port'>, process: DiscoveredProcess): boolean {
  return target.executable === process.executable && process.ports.includes(target.port);
}
