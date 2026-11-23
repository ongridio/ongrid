import { request } from './client';
import { tr as trInline } from '@/i18n/locale';

// AgentSummary mirrors backend handler/aiops::agentDTO. Trimmed view of
// chatruntime.Agent — enough to render the inventory page card and the
// Side Panel agent picker.
export type AgentSource = 'builtin' | 'disk' | 'user';

export type AgentSummary = {
  name: string;
  description: string;
  when_to_use?: string;
  tools?: string[];
  disallowed_tools?: string[];
  permission_mode?: string;
  model?: string;
  max_turns?: number;
  system_prompt?: string;
  critical_reminder?: string;
  source?: AgentSource;
};

// CreateUserAgentInput mirrors backend userAgentReq.
export type UserAgentInput = {
  name?: string;
  description: string;
  when_to_use?: string;
  system_prompt: string;
  critical_reminder?: string;
  allowed_tools?: string[];
  disallowed_tools?: string[];
  permission_mode?: string;
  model?: string;
  max_turns?: number;
};

export type AgentListResp = { items: AgentSummary[]; total: number };

const AGENT_LABELS_ZH: Record<string, string> = {
  default: '默认助理',
  'incident-investigator': '故障诊断',
  'specialist-sre': 'SRE 专家',
  'specialist-ops': '运维专家',
  'specialist-compute': '计算专家',
  'specialist-network': '网络专家',
  'specialist-disk': '磁盘专家',
  reviewer: '审核员',
};
const AGENT_LABELS_EN: Record<string, string> = {
  default: 'Default',
  'incident-investigator': 'Incident investigator',
  'specialist-sre': 'SRE specialist',
  'specialist-ops': 'Ops specialist',
  'specialist-compute': 'Compute specialist',
  'specialist-network': 'Network specialist',
  'specialist-disk': 'Disk specialist',
  reviewer: 'Reviewer',
};

export function agentLabel(name: string) {
  const zh = AGENT_LABELS_ZH[name];
  return zh ? trInline(zh, AGENT_LABELS_EN[name] ?? zh) : name;
}

// BUILTIN_AGENT_I18N: bilingual overrides for built-in agent personas.
// Backend agents/*.md files store Chinese descriptions; we localize the
// /agents page card body without modifying the source-of-truth markdown.
const BUILTIN_AGENT_I18N: Record<string, { desc: { zh: string; en: string }; whenToUse?: { zh: string; en: string } }> = {
  default: {
    desc: {
      zh: 'Coordinator 默认助理，访问全套技能，适合临时探索 / 通用排查。',
      en: 'Default coordinator assistant with access to the full skill set; ideal for ad-hoc exploration and general troubleshooting.',
    },
  },
  'incident-investigator': {
    desc: {
      zh: '故障诊断专家：把一条 incident 走完 metric / log / trace / edge 关联，输出现象 / 关联信号 / 假设。',
      en: 'Incident investigator: takes one incident end-to-end across metric / log / trace / edge correlation, outputs symptoms / correlated signals / hypotheses.',
    },
  },
  'specialist-sre': {
    desc: {
      zh: 'SRE 专家：黄金四信号 / SLO / 错误预算 / 告警优先级 / 趋势异常，判断"系统现在到底好不好"。',
      en: 'SRE specialist: golden signals / SLO / error budget / alert priority / trend anomalies — answers "is the system actually healthy right now".',
    },
  },
  'specialist-ops': {
    desc: {
      zh: '运维专家：服务状态 / 启停重启 / 进程占用 / 计划任务 / 容量决策，处理"这一台机器现在该不该动"的运营问题。',
      en: 'Ops specialist: service status / start-stop-restart / process usage / cron / capacity calls — handles "should we act on this single host right now" operational questions.',
    },
  },
  'specialist-compute': {
    desc: {
      zh: '计算专家：CPU / 内存 / load / 进程调度 / 上下文切换 / OOM / NUMA / 内核参数，定位"谁在吃计算资源"。',
      en: 'Compute specialist: CPU / memory / load / process scheduling / context switches / OOM / NUMA / kernel tunables — pinpoints "who is burning compute resources".',
    },
  },
  'specialist-network': {
    desc: {
      zh: '网络专家：处理 DNS / 路由 / iptables / conntrack / TLS / MTU / OVS / eBPF 等网络层问题。',
      en: 'Network specialist: handles DNS / routing / iptables / conntrack / TLS / MTU / OVS / eBPF and other networking-layer issues.',
    },
  },
  'specialist-disk': {
    desc: {
      zh: '磁盘专家：处理空间不足 / inode 耗尽 / I/O 瓶颈 / 文件系统异常等磁盘相关问题。',
      en: 'Disk specialist: handles low space / inode exhaustion / I/O bottlenecks / filesystem anomalies and related disk issues.',
    },
  },
  reviewer: {
    desc: {
      zh: '审核员：在执行 mutating 技能（重启服务 / 改配置 / 删数据）前做二审，把关风险。',
      en: 'Reviewer: gates mutating skills (service restart / config change / data delete) with a second-pass risk check.',
    },
  },
  reporter: {
    desc: {
      zh: '定时运维报告 worker：把已算好的事实数据写成带叙事的周期运维报告，聚焦资源趋势与监控覆盖，不只盯故障；不计算、不发明任何数字。',
      en: 'Scheduled-report worker: turns pre-computed facts into a narrative periodic ops report, focused on resource trends + monitoring coverage (not just incidents); never computes or invents any numbers.',
    },
  },
};

/** Returns a localized copy of an agent summary for the current locale. */
export function localizedAgent(a: AgentSummary): AgentSummary {
  const m = BUILTIN_AGENT_I18N[a.name];
  if (!m) return a;
  return {
    ...a,
    description: trInline(m.desc.zh, m.desc.en),
    when_to_use: m.whenToUse ? trInline(m.whenToUse.zh, m.whenToUse.en) : a.when_to_use,
  };
}

export function listAgents() {
  return request<AgentListResp>('GET', '/agents');
}

export function getAgent(name: string) {
  return request<AgentSummary>('GET', `/agents/${encodeURIComponent(name)}`);
}

export function createUserAgent(input: UserAgentInput) {
  return request<AgentSummary>('POST', '/agents/custom', input);
}

export function updateUserAgent(name: string, input: UserAgentInput) {
  return request<AgentSummary>(
    'PATCH',
    `/agents/custom/${encodeURIComponent(name)}`,
    input,
  );
}

export function deleteUserAgent(name: string) {
  return request<void>('DELETE', `/agents/custom/${encodeURIComponent(name)}`);
}

// deleteAgent is the generic delete — works on any non-builtin /
// non-default agent (disk-source = session-scoped, user-source = DB
// row removed).
export function deleteAgent(name: string) {
  return request<void>('DELETE', `/agents/${encodeURIComponent(name)}`);
}
