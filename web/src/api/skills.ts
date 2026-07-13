import { request } from './client';
import { tr as trInline } from '@/i18n/locale';

export type SkillClass = 'safe' | 'mutating' | 'dangerous';

// SkillScope mirrors backend skillcore.Scope. "host" = runs on the device
// agent (needs device_id), "manager" = runs in-process on the cloud
// (web_search style, no device_id).
export type SkillScope = 'host' | 'manager';

export type SkillParamType = 'string' | 'int' | 'float' | 'bool' | 'duration' | 'enum';

export type SkillParamDef = {
  name: string;
  type: SkillParamType;
  required?: boolean;
  default?: unknown;
  desc?: string;
  enum?: string[];
};

export type SkillSummary = {
  key: string;
  name: string;
  description: string;
  class: SkillClass;
  scope?: SkillScope;
  category?: string;
  params: SkillParamDef[];
  result_preview?: string;
  // source: "" / "builtin" = shipped; "git"/"tarball"/"local"/"marketplace"
  // = installed via the marketplace. UI badges installed skills.
  source?: string;
  // inventory_only: skill is listed for visibility but has no
  // hand-renderable form (raw JSON Schema only). UI should hide the
  // execute button and point the user at chat as the intended caller.
  inventory_only?: boolean;
};

export type SkillListResp = { items: SkillSummary[]; total: number };

export type ExecuteResult = {
  result?: unknown;
  error?: string;
};

export function listSkills(category?: string) {
  const qs = category ? `?category=${encodeURIComponent(category)}` : '';
  return request<SkillListResp>('GET', `/skills${qs}`);
}

export function getSkill(key: string) {
  return request<SkillSummary>('GET', `/skills/${encodeURIComponent(key)}`);
}

// BUILTIN_SKILL_I18N: per-skill overrides for the skills shipped with
// ongrid. `name` is single-string and intentionally always English —
// skill keys are technical identifiers (host_bash / query_promql /
// etc.) and Chinese localisations of the name field made the table
// inconsistent (some rows in zh, some in en) and the LLM tool routing
// schema is English-keyed anyway. `description` and `category` still
// follow the user's locale so the human-readable explanation flips.
const BUILTIN_SKILL_I18N: Record<string, { name: string; desc: { zh: string; en: string }; category?: { zh: string; en: string } }> = {
  host_bash: {
    name: 'host_bash',
    desc: {
      zh: '设备上跑只读 shell 命令做诊断探索（沙箱化 / read-only policy）',
      en: "Run a read-only shell command on a device for diagnostic exploration (sandboxed, read-only policy).",
    },
    category: { zh: '主机', en: 'Host' },
  },
  host_find_large_files: {
    name: 'host_find_large_files',
    desc: {
      zh: '返回设备上指定目录下最大的 N 个文件（按大小降序）。支持一次传入多个路径。',
      en: 'Return the top-N largest files under one or more paths on a device, sorted by size descending. Accepts a batch of paths.',
    },
    category: { zh: '主机', en: 'Host' },
  },
  host_du_summary: {
    name: 'host_du_summary',
    desc: {
      zh: '返回设备上指定目录的逐级磁盘占用（按大小降序）。支持一次传入多个路径。',
      en: 'Return per-subdirectory disk usage under one or more paths on a device, sorted by size descending. Accepts a batch of paths.',
    },
    category: { zh: '主机', en: 'Host' },
  },
  host_stat_file: {
    name: 'host_stat_file',
    desc: {
      zh: '返回一个或多个文件 / 目录的 size / mtime / atime / mode / owner。支持一次传入多个路径。',
      en: 'Return size / mtime / atime / mode / owner for one or more files or directories on a device. Accepts a batch of paths.',
    },
    category: { zh: '主机', en: 'Host' },
  },
  host_restart_service: {
    name: 'host_restart_service',
    desc: {
      zh: '远程重启目标设备上指定的 systemd 服务（mutating，需要 reviewer 二审）。',
      en: 'Restart a systemd service on the target device (mutating — requires reviewer second pass).',
    },
    category: { zh: '主机', en: 'Host' },
  },
  host_netns_inspect: {
    name: 'host_netns_inspect',
    desc: {
      zh: '列出 /var/run/netns 下的所有 network namespace 并对每个 namespace 报告 IP 地址 / 路由 / 接口状态。填补 host_bash 不支持 `ip -n <ns>` 的盲区。仅 read-only。',
      en: 'List every network namespace under /var/run/netns and report IP / route / interface status for each. Read-only.',
    },
    category: { zh: '网络', en: 'Network' },
  },
  host_probe_dns: {
    name: 'host_probe_dns',
    desc: {
      zh: 'DNS 解析目标 host，返回 A/AAAA 记录',
      en: 'Resolve a hostname via DNS and return A / AAAA records.',
    },
    category: { zh: '网络', en: 'Network' },
  },
  host_probe_http: {
    name: 'host_probe_http',
    desc: {
      zh: '对 URL 发起 HEAD/GET 请求，返回状态码 + 延迟 + 内容长度',
      en: 'Issue a HEAD / GET to a URL and return status code, latency, and content length.',
    },
    category: { zh: '网络', en: 'Network' },
  },
  host_probe_tcp: {
    name: 'host_probe_tcp',
    desc: {
      zh: '对目标 host:port 发起 TCP 连接，返回连通状态 + 延迟',
      en: 'Open a TCP connection to host:port and return reachability + latency.',
    },
    category: { zh: '网络', en: 'Network' },
  },
  host_read_journal: {
    name: 'host_read_journal',
    desc: {
      zh: '读 systemd-journald 日志（journalctl 包装），仅 Linux 支持',
      en: 'Read systemd-journald logs (wraps journalctl). Linux-only.',
    },
    category: { zh: '文件系统', en: 'filesystem' },
  },
  host_tail_file: {
    name: 'host_tail_file',
    desc: {
      zh: '读取文件最后 N 行（类似 tail -n）',
      en: 'Read the last N lines of a file (analogous to tail -n).',
    },
    category: { zh: '文件系统', en: 'filesystem' },
  },
  get_host_load: {
    name: 'get_host_load',
    desc: {
      zh: '获取设备的 CPU / 内存 / load 即时快照（按需采集）。',
      en: 'Get a real-time snapshot of CPU / memory / load on a device (on-demand sampling).',
    },
    category: { zh: '主机', en: 'Host' },
  },
  get_host_processes: {
    name: 'get_host_processes',
    desc: {
      zh: '获取设备上的进程列表（CPU / 内存 排序）。',
      en: 'List processes on a device (sorted by CPU or memory).',
    },
    category: { zh: '主机', en: 'Host' },
  },
  query_promql: {
    name: 'query_promql',
    desc: {
      zh: '在 Prometheus 上执行 PromQL 查询（瞬时 / 范围）。',
      en: 'Run a PromQL query on Prometheus (instant or range).',
    },
    category: { zh: '观测', en: 'Observability' },
  },
  query_logql: {
    name: 'query_logql',
    desc: {
      zh: '在 Loki 上执行 LogQL 查询。',
      en: 'Run a LogQL query on Loki.',
    },
    category: { zh: '观测', en: 'Observability' },
  },
  query_traceql: {
    name: 'query_traceql',
    desc: {
      zh: '在 Tempo 上执行 TraceQL 查询。',
      en: 'Run a TraceQL query on Tempo.',
    },
    category: { zh: '观测', en: 'Observability' },
  },
  query_knowledge: {
    name: 'query_knowledge',
    desc: {
      zh: '在团队知识库（运维 playbook + 同步的 git 仓库）做语义检索。',
      en: "Semantic search over the operator's knowledge base (curated playbooks + synced git repos).",
    },
    category: { zh: '知识', en: 'Knowledge' },
  },
  query_devices: {
    name: 'query_devices',
    desc: {
      zh: '按条件检索设备清单（角色 / 在线状态 / 主机名 / device_id 等）。',
      en: 'Search the device inventory by role / online status / hostname / device_id, etc.',
    },
    category: { zh: '平台', en: 'Platform' },
  },
  query_incidents: {
    name: 'query_incidents',
    desc: {
      zh: '检索告警 incident 列表（按 severity / status / 时间窗）。',
      en: 'Search alert incidents (by severity / status / time window).',
    },
    category: { zh: '告警', en: 'Alerts' },
  },
  query_alert_rules: {
    name: 'query_alert_rules',
    desc: {
      zh: '检索告警规则配置（按 kind / 启用状态 / 范围）。',
      en: 'Search alert rule definitions (by kind / enabled / scope).',
    },
    category: { zh: '告警', en: 'Alerts' },
  },
  get_incident_detail: {
    name: 'get_incident_detail',
    desc: {
      zh: '获取一条 incident 的完整详情（规则 / 事件 / 标签 / 关联设备）。',
      en: 'Fetch the full detail of one incident (rule / events / labels / related device).',
    },
    category: { zh: '告警', en: 'Alerts' },
  },
  correlate_incident: {
    name: 'correlate_incident',
    desc: {
      zh: '为一条 incident 做 metric / log / trace / edge 多源关联分析。',
      en: 'Run a metric / log / trace / edge correlation for one incident.',
    },
    category: { zh: '告警', en: 'Alerts' },
  },
  get_topology: {
    name: 'get_topology',
    desc: {
      zh: '返回集群拓扑（设备 / 角色 / 关系）的简要视图。',
      en: 'Return a brief view of the cluster topology (devices / roles / relations).',
    },
    category: { zh: '平台', en: 'Platform' },
  },
  get_edge_summary: {
    name: 'get_edge_summary',
    desc: {
      zh: '获取一台 edge 的概览（在线状态 / 角色 / 最近指标 / 最近事件）。',
      en: 'Get an overview of one edge (online status / roles / recent metrics / recent events).',
    },
    category: { zh: '平台', en: 'Platform' },
  },
  find_outlier_edges: {
    name: 'find_outlier_edges',
    desc: {
      zh: '基于 z-score 找出指标偏离基线最远的设备（异常 outlier）。',
      en: 'Find devices whose metrics drift farthest from baseline by z-score (outliers).',
    },
    category: { zh: '主机', en: 'Host' },
  },
  rank_edges: {
    name: 'rank_edges',
    desc: {
      zh: '按指标对设备排序（top-N，多指标加权）。',
      en: 'Rank devices by metric (top-N, optionally weighted).',
    },
    category: { zh: '主机', en: 'Host' },
  },
  web_search: {
    name: 'web_search',
    desc: {
      zh: '联网搜索（SearXNG / Tavily / Brave，由设置选择 provider）。',
      en: 'Web search (SearXNG / Tavily / Brave — provider chosen in settings).',
    },
    category: { zh: '通用', en: 'General' },
  },
};

/** Returns a localized copy of a skill summary for the current locale. */
export function localizedSkill(s: SkillSummary): SkillSummary {
  const m = BUILTIN_SKILL_I18N[s.key];
  if (!m) return s;
  return {
    ...s,
    // Name is always English — see BUILTIN_SKILL_I18N comment for rationale.
    name: m.name,
    description: trInline(m.desc.zh, m.desc.en),
    category: m.category ? trInline(m.category.zh, m.category.en) : s.category,
  };
}

export function executeSkill(
  key: string,
  // null = manager-scope skill (no edge_id required)
  edgeID: string | number | null,
  params: Record<string, unknown>,
) {
  const body: Record<string, unknown> = { params };
  if (edgeID !== null && edgeID !== '' && edgeID !== undefined) {
    body.edge_id = typeof edgeID === 'number' ? String(edgeID) : edgeID;
  }
  return request<ExecuteResult>('POST', `/skills/${encodeURIComponent(key)}/execute`, body);
}
