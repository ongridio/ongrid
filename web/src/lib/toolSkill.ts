// toolSkill — the single source of truth for grouping tools into skills.
//
// A "skill" here is the organising layer above atomic tools (the node /
// execution model is unchanged). Both the workflow tool palette and the
// Skills page group by these helpers — there is no separate "category" /
// CATEGORY_ORDER concept anymore.
//
// A group is one of:
//   - a curated built-in skill (6 of them), keyed by the tool's WIRE name;
//   - an MCP server: each server is its own group (tag "mcp");
//   - a SKILL.md extension pack (category === "skill"): its own group (tag
//     "ext").
// Everything maps to a real group — "other" is a last-resort that should stay
// empty in practice.

export const SKILL_ORDER = ['observe', 'device', 'fleet', 'incident', 'knowledge', 'cloud', 'other'] as const;

const SKILL_LABEL: Record<string, { zh: string; en: string }> = {
  observe: { zh: '观测', en: 'Observability' },
  device: { zh: '设备管理', en: 'Devices' },
  fleet: { zh: '集群与拓扑', en: 'Fleet & topology' },
  incident: { zh: '告警与事件', en: 'Incidents & alerts' },
  knowledge: { zh: '知识', en: 'Knowledge' },
  cloud: { zh: '云端管理', en: 'Cloud' },
  other: { zh: '其他', en: 'Other' },
};

// toolGroupKey returns the group key for a tool. `wire` is the wire name (the
// tool's `key` on the Skills page, or FlowToolMeta.name on the palette — both
// are the unsanitized identifier). source/category are optional (the Skills
// page has them; the flow palette doesn't).
export function toolGroupKey(wire: string, source?: string, category?: string): string {
  if (wire.startsWith('mcp__') || source === 'mcp') {
    const server = wire.startsWith('mcp__') ? wire.split('__')[1] || 'mcp' : 'mcp';
    return 'mcp:' + server;
  }
  if (category === 'skill') return 'skill:' + wire; // a SKILL.md pack = its own group
  if (/^(get_)?host_/.test(wire) || wire.includes('restart_service')) return 'device';
  if (/^query_(promql|logql|traceql)$/.test(wire) || wire === 'list_metric_catalog' || wire === 'list_database_sources' || wire === 'analyze_database_status') return 'observe';
  if (wire.includes('topology') || wire === 'query_devices' || wire === 'query_edges' || wire === 'rank_edges' || wire === 'find_outlier_edges' || wire === 'get_edge_summary') return 'fleet';
  if (wire.includes('incident') || wire.includes('alert') || wire === 'query_change_events') return 'incident';
  if (wire === 'query_knowledge' || wire === 'web_search' || wire.includes('source')) return 'knowledge';
  if (wire === 'cloud_bash' || wire.includes('config_change')) return 'cloud';
  return 'other';
}

// groupTag returns the type badge for a group key: 'mcp' for MCP servers,
// 'ext' for extension packs, '' for built-in skills.
export function groupTag(key: string): '' | 'mcp' | 'ext' {
  if (key.startsWith('mcp:')) return 'mcp';
  if (key.startsWith('skill:')) return 'ext';
  return '';
}

// groupTitle resolves a group key to its section title. MCP servers show the
// server name, extension packs show the pack's display name (passed in via
// fallback), curated skills use the fixed label.
export function groupTitle(key: string, zh: boolean, fallback?: string): string {
  if (key.startsWith('mcp:')) return key.slice(4);
  if (key.startsWith('skill:')) return fallback || key.slice(6);
  const l = SKILL_LABEL[key];
  return l ? (zh ? l.zh : l.en) : fallback || key;
}

// orderedGroupKeys: curated skills first (fixed order), then MCP servers
// (alphabetical), then extension packs (alphabetical), then anything else.
export function orderedGroupKeys(keys: Iterable<string>): string[] {
  const set = new Set(keys);
  const curated = SKILL_ORDER.filter((k) => set.has(k));
  const mcp = [...set].filter((k) => k.startsWith('mcp:')).sort();
  const ext = [...set].filter((k) => k.startsWith('skill:')).sort();
  const used = new Set<string>([...curated, ...mcp, ...ext]);
  const rest = [...set].filter((k) => !used.has(k)).sort();
  return [...curated, ...mcp, ...ext, ...rest];
}
