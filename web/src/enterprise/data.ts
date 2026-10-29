export type Bilingual = { zh: string; en: string };
export type Resource = {
  id: string;
  name: string;
  group: string;
  ip: string;
  kind: 'server' | 'database' | 'network';
  cpu: number;
  memory: number;
  status: 'healthy' | 'warning';
};
export type Incident = {
  id: string;
  title: Bilingual;
  resourceId: string;
  severity: 'critical' | 'warning' | 'info';
  status: 'open' | 'acknowledged' | 'closed';
  time: string;
};
export type Article = {
  id: string;
  title: Bilingual;
  body: Bilingual;
  category: Bilingual;
};
export type Flow = {
  id: string;
  title: Bilingual;
  description: Bilingual;
  steps: Bilingual[];
};
export type Run = {
  id: string;
  flowId: string;
  time: string;
  status: 'success';
  steps: number;
};
export type Sandbox = {
  version: 1;
  anchor: string;
  resources: Resource[];
  incidents: Incident[];
  articles: Article[];
  runs: Run[];
};
export const STORAGE_KEY = 'enterprise-ai.sandbox.v1';
export const flows: Flow[] = [
  {
    id: 'daily',
    title: { zh: '每日健康巡检', en: 'Daily health inspection' },
    description: {
      zh: '检查核心资源、汇总风险并生成巡检记录',
      en: 'Inspect core resources, summarize risks and create a record',
    },
    steps: [
      { zh: '读取资源清单', en: 'Read inventory' },
      { zh: '检查 CPU 与内存', en: 'Check CPU and memory' },
      { zh: '关联未关闭告警', en: 'Correlate active incidents' },
      { zh: '生成巡检报告', en: 'Generate inspection report' },
    ],
  },
  {
    id: 'cpu',
    title: { zh: '高 CPU 应急排查', en: 'High CPU investigation' },
    description: {
      zh: '采样负载、分析热点并给出处置建议',
      en: 'Sample load, analyze hotspots and recommend actions',
    },
    steps: [
      { zh: '定位异常主机', en: 'Locate affected host' },
      { zh: '模拟采样热点进程', en: 'Simulate process sampling' },
      { zh: '检查应用变更', en: 'Check application changes' },
      { zh: '输出扩容与限流建议', en: 'Recommend scaling and rate limits' },
    ],
  },
  {
    id: 'latency',
    title: { zh: '接口延迟诊断', en: 'API latency diagnosis' },
    description: {
      zh: '关联应用指标与数据库依赖，定位慢请求',
      en: 'Correlate application metrics and database dependencies',
    },
    steps: [
      { zh: '读取请求分位延迟', en: 'Read request latency percentiles' },
      { zh: '关联数据库连接', en: 'Correlate database connections' },
      { zh: '模拟慢查询分析', en: 'Simulate slow query analysis' },
      { zh: '生成优化建议', en: 'Generate optimization advice' },
    ],
  },
];
const titles: Bilingual[] = [
  { zh: '订单服务 CPU 持续高于阈值', en: 'Order service CPU above threshold' },
  { zh: '支付接口 P95 延迟升高', en: 'Payment API P95 latency increased' },
  {
    zh: '数据库连接池使用率偏高',
    en: 'Database connection pool utilization high',
  },
  {
    zh: '应用主机内存使用率升高',
    en: 'Application host memory usage increased',
  },
  { zh: '核心交换机端口流量异常', en: 'Core switch port traffic anomaly' },
  { zh: '备份任务执行时间超限', en: 'Backup execution exceeded time limit' },
  { zh: '证书有效期不足三十天', en: 'Certificate expires within thirty days' },
  { zh: '巡检发现磁盘增长趋势', en: 'Inspection detected disk growth' },
  { zh: '缓存命中率低于基线', en: 'Cache hit rate below baseline' },
  { zh: '应用实例健康检查恢复', en: 'Application health check recovered' },
  { zh: '数据库复制延迟恢复', en: 'Database replication lag recovered' },
  { zh: '网络链路抖动已恢复', en: 'Network jitter recovered' },
];
export function seed(now = new Date()): Sandbox {
  const anchor = now.toISOString();
  const resources: Resource[] = Array.from({ length: 24 }, (_, i) => ({
    id: `res-${i + 1}`,
    name: `${['order-api', 'payment-api', 'account-api', 'inventory-api', 'mysql-primary', 'core-switch'][i % 6]}-${String(Math.floor(i / 6) + 1).padStart(2, '0')}`,
    group: ['commerce', 'finance', 'platform'][i % 3],
    ip: `10.42.${Math.floor(i / 8) + 1}.${i + 11}`,
    kind: i % 6 === 4 ? 'database' : i % 6 === 5 ? 'network' : 'server',
    cpu: i === 0 ? 94 : 17 + ((i * 13) % 57),
    memory: 32 + ((i * 7) % 53),
    status: i < 3 ? 'warning' : 'healthy',
  }));
  return {
    version: 1,
    anchor,
    resources,
    incidents: titles.map((title, i) => ({
      id: `INC-${1201 + i}`,
      title,
      resourceId: resources[[0, 1, 4, 3, 5, 6, 7, 8, 9, 10, 16, 17][i]].id,
      severity: i < 2 ? 'critical' : i < 7 ? 'warning' : 'info',
      status: i < 6 ? 'open' : i < 9 ? 'acknowledged' : 'closed',
      time: new Date(now.getTime() - (i + 1) * 840000).toISOString(),
    })),
    articles: [
      '高 CPU 排查指南',
      '接口延迟分析方法',
      '每日巡检操作规范',
      '数据库连接池调优',
      '内存容量规划',
      '网络异常检查',
      '告警分级处置',
      '应用发布回滚',
    ].map((title, i) => ({
      id: `kb-${i + 1}`,
      title: {
        zh: title,
        en: [
          'High CPU troubleshooting',
          'API latency analysis',
          'Daily inspection procedure',
          'Database pool tuning',
          'Memory capacity planning',
          'Network incident checks',
          'Incident severity handling',
          'Application rollback',
        ][i],
      },
      category: {
        zh: i < 3 ? '操作规程' : '技术指南',
        en: i < 3 ? 'Procedure' : 'Technical guide',
      },
      body: {
        zh: `适用范围：星澜企业示例环境。\n\n1. 从资源台账确认受影响资源及所属业务。\n2. 对照指标监测查看最近 24 小时趋势，确认异常开始时间。\n3. 在告警中心确认告警并记录影响范围。\n4. 使用智能运维助手分析，必要时模拟执行关联流程。\n5. 核对执行记录与恢复结果，再关闭告警。\n\n验收标准：风险原因和处置建议均有记录。本文不构成真实环境的自动执行授权。`,
        en: 'Applies to the fictional Starlane enterprise environment.\n\n1. Identify the affected resource and business group in Inventory.\n2. Review the last 24 hours in Metrics and identify the onset.\n3. Acknowledge the incident and record its impact.\n4. Consult the AI assistant and simulate the related workflow.\n5. Review execution records and recovery before closing the incident.\n\nAcceptance: record the cause and recommended action. This document does not authorize execution in a real environment.',
      },
    })),
    runs: Array.from({ length: 6 }, (_, i) => ({
      id: `RUN-${1006 - i}`,
      flowId: flows[i % 3].id,
      time: new Date(now.getTime() - (i + 1) * 3600000).toISOString(),
      status: 'success',
      steps: 4,
    })),
  };
}
// Validate stored data before trusting it. Invalid versions are preserved until the
// operator explicitly resets; callers receive a visible warning and fresh memory state.
export function load(storage: Pick<Storage, 'getItem'>): {
  data: Sandbox;
  error: boolean;
} {
  try {
    const raw = storage.getItem(STORAGE_KEY);
    if (!raw) return { data: seed(), error: false };
    const data = JSON.parse(raw) as Sandbox;
    if (
      data.version !== 1 ||
      !Number.isFinite(Date.parse(data.anchor)) ||
      !Array.isArray(data.resources) ||
      !Array.isArray(data.incidents) ||
      !Array.isArray(data.articles) ||
      !Array.isArray(data.runs)
    )
      throw new Error('Invalid sandbox');
    if (
      data.resources.length !== 24 ||
      !data.resources.every(
        (r) =>
          typeof r.id === 'string' &&
          typeof r.name === 'string' &&
          typeof r.cpu === 'number' &&
          typeof r.memory === 'number' &&
          ['healthy', 'warning'].includes(r.status),
      ) ||
      !data.incidents.every(
        (i) =>
          data.resources.some((r) => r.id === i.resourceId) &&
          titles.some((t) => t.zh === i.title?.zh) &&
          ['open', 'acknowledged', 'closed'].includes(i.status),
      ) ||
      !data.articles.every(
        (a) =>
          typeof a.id === 'string' &&
          typeof a.title?.zh === 'string' &&
          typeof a.title?.en === 'string' &&
          typeof a.body?.zh === 'string' &&
          typeof a.body?.en === 'string' &&
          typeof a.category?.zh === 'string' &&
          typeof a.category?.en === 'string',
      ) ||
      !data.runs.every(
        (r) =>
          typeof r.id === 'string' &&
          flows.some((f) => f.id === r.flowId) &&
          Number.isFinite(Date.parse(r.time)) &&
          r.status === 'success',
      )
    )
      throw new Error('Invalid records');
    return {
      data: {
        ...data,
        articles: data.articles.map((article) => ({
          ...article,
          body: {
            zh: article.body.zh.replace(
              '本文为演示知识，不构成真实环境的自动执行授权。',
              '本文不构成真实环境的自动执行授权。',
            ),
            en: article.body.en.replace(
              'This demonstration document does not authorize',
              'This document does not authorize',
            ),
          },
        })),
      },
      error: false,
    };
  } catch {
    return { data: seed(), error: true };
  }
}
export function save(
  storage: Pick<Storage, 'setItem'>,
  data: Sandbox,
): boolean {
  try {
    storage.setItem(STORAGE_KEY, JSON.stringify(data));
    return true;
  } catch {
    return false;
  }
}
export function updateIncident(
  data: Sandbox,
  id: string,
  status: Incident['status'],
): Sandbox {
  return {
    ...data,
    incidents: data.incidents.map((i) =>
      i.id === id &&
      i.status !== 'closed' &&
      (status === 'closed' || status === 'acknowledged')
        ? { ...i, status }
        : i,
    ),
  };
}
export function executeFlow(
  data: Sandbox,
  flowId: string,
  now = new Date(),
): Sandbox {
  const flow = flows.find((f) => f.id === flowId);
  if (!flow) return data;
  return {
    ...data,
    runs: [
      {
        id: `RUN-${uniqueId()}`,
        flowId,
        time: now.toISOString(),
        status: 'success' as const,
        steps: flow.steps.length,
      },
      ...data.runs,
    ].slice(0, 100),
  };
}
export function metricPoints(
  cpu: number,
  hours: number,
): { hour: string; cpu: number; memory: number }[] {
  return Array.from({ length: hours + 1 }, (_, i) => ({
    hour: `${i - hours}h`,
    cpu: Math.max(
      8,
      Math.min(
        99,
        Math.round(cpu - 12 + Math.sin(i * 0.8) * 11 + (i / hours) * 12),
      ),
    ),
    memory: Math.round(58 + Math.sin(i * 0.4) * 9),
  }));
}

// getRandomValues works on private-network HTTP; randomUUID requires HTTPS.
export function uniqueId(): string {
  return Array.from(crypto.getRandomValues(new Uint8Array(12)), (v) =>
    v.toString(16).padStart(2, '0'),
  ).join('');
}
